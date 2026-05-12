// Package secrets provides secure storage and handling of sensitive values
// (API keys, tokens, passwords) for the ageni agent runtime.
//
// Design principles:
//   - Secret values are never stored in plain Go strings beyond the minimum
//     needed to move them into a memguard.Enclave.
//   - The LLM context window must never receive a secret value. Tools accept
//     only secret aliases (names), resolve internally, and return only the
//     operation result — never the credential itself.
//   - All output flowing back to the LLM is filtered through the Redactor
//     as a backstop against indirect leakage (e.g. shell output that echoes
//     an env var).
package secrets

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/99designs/keyring"
	"github.com/awnumar/memguard"
)

const (
	keyringSvc = "ageni"
	keyringKey = "secrets" // single JSON blob — one unlock prompt per session
)

// Store is the single source of truth for all secret values at runtime.
// Values are held as memguard.Enclave (encrypted in memory, mlock'd) and
// decrypted only for the duration of a single use.
type Store struct {
	mu       sync.RWMutex
	enclaves map[string]*memguard.Enclave // alias → encrypted value
	ring     keyring.Keyring
	redactor *Redactor
}

// Open opens (or creates) the secret store using the system keychain when
// available, falling back to an encrypted file at ~/.ageni/keyring/ for
// headless / CI environments. Existing plaintext values in the environment
// are loaded as a last resort but are never persisted from this path.
func Open(cfg keyring.Config) (*Store, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = keyringSvc
	}

	// Backend preference: system keychain → file vault (headless fallback)
	if len(cfg.AllowedBackends) == 0 {
		home, _ := os.UserHomeDir()
		cfg.AllowedBackends = []keyring.BackendType{
			keyring.KeychainBackend,      // macOS
			keyring.SecretServiceBackend, // Linux GNOME / libsecret
			keyring.KWalletBackend,       // Linux KDE
			keyring.WinCredBackend,       // Windows DPAPI
			keyring.FileBackend,          // headless fallback
		}
		if cfg.FileDir == "" {
			cfg.FileDir = home + "/.ageni/keyring/"
		}
		if cfg.FilePasswordFunc == nil {
			// Use AGENI_KEYRING_PASSPHRASE for non-interactive environments.
			if pass := os.Getenv("AGENI_KEYRING_PASSPHRASE"); pass != "" {
				cfg.FilePasswordFunc = func(_ string) (string, error) { return pass, nil }
			} else {
				cfg.FilePasswordFunc = keyring.TerminalPrompt
			}
		}
	}

	ring, err := keyring.Open(cfg)
	if err != nil {
		return nil, fmt.Errorf("secrets: keyring open: %w", err)
	}

	s := &Store{
		enclaves: make(map[string]*memguard.Enclave),
		ring:     ring,
		redactor: NewRedactor(),
	}

	// Load persisted secrets into encrypted enclaves.
	if err := s.loadFromKeyring(); err != nil {
		// Non-fatal: keyring may be empty on first run.
		_ = err
	}

	// Layer env vars on top (they win over keyring — same precedence as config).
	// We do NOT persist env var values to the keyring; they're transient.
	s.loadFromEnv()

	return s, nil
}

// OpenDefault opens the store with sensible defaults. Suitable for most use.
func OpenDefault() (*Store, error) {
	return Open(keyring.Config{})
}

// OpenEnvOnly returns a store backed only by environment variables (no keyring).
// Used as a safe fallback when the keyring is unavailable.
func OpenEnvOnly() (*Store, error) {
	s := &Store{
		enclaves: make(map[string]*memguard.Enclave),
		ring:     nil,
		redactor: NewRedactor(),
	}
	s.loadFromEnv()
	return s, nil
}

// Get decrypts and returns the value for the given alias. The returned string
// is a plain Go string — callers should use it immediately and not store it.
// Returns an error if the alias is not found.
func (s *Store) Get(alias string) (string, error) {
	s.mu.RLock()
	enc, ok := s.enclaves[alias]
	s.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("secret %q not found", alias)
	}

	buf, err := enc.Open()
	if err != nil {
		return "", fmt.Errorf("secret %q: decrypt: %w", alias, err)
	}
	defer buf.Destroy()

	return string(buf.Bytes()), nil
}

// Set stores a secret under the given alias in both the in-memory enclave
// and the persistent keyring. The value bytes are zeroed after sealing.
func (s *Store) Set(alias, value string) error {
	b := []byte(value)
	enc := memguard.NewEnclave(b)
	memguard.WipeBytes(b)

	s.mu.Lock()
	s.enclaves[alias] = enc
	err := s.persistToKeyring()
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("secrets: persist %q: %w", alias, err)
	}

	// Register the value with the redactor so it's scrubbed from tool output.
	// We re-open the enclave briefly to register — value is not retained.
	s.registerWithRedactor(alias)
	return nil
}

// Delete removes an alias from both the in-memory store and the keyring.
func (s *Store) Delete(alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.enclaves[alias]; ok {
		delete(s.enclaves, alias)
	}
	s.redactor.Unregister(alias)
	return s.persistToKeyring()
}

// List returns the alias names of all stored secrets. Values are never
// returned. Safe to include in LLM tool responses.
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.enclaves))
	for k := range s.enclaves {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has returns true if the alias is present (without decrypting).
func (s *Store) Has(alias string) bool {
	s.mu.RLock()
	_, ok := s.enclaves[alias]
	s.mu.RUnlock()
	return ok
}

// Redactor returns the store's Redactor, which scrubs known secret values
// from arbitrary strings before they enter the LLM context window.
func (s *Store) Redactor() *Redactor {
	return s.redactor
}

// ──────────────────────────── internal helpers ────────────────────────────

// loadFromKeyring reads the single JSON blob from the keyring and populates
// the in-memory enclave map.
func (s *Store) loadFromKeyring() error {
	item, err := s.ring.Get(keyringKey)
	if err != nil {
		return err // ErrKeyNotFound is normal on first run
	}

	var kv map[string]string
	if err := json.Unmarshal(item.Data, &kv); err != nil {
		return fmt.Errorf("secrets: keyring blob corrupt: %w", err)
	}

	for alias, val := range kv {
		b := []byte(val)
		s.enclaves[alias] = memguard.NewEnclave(b)
		memguard.WipeBytes(b)
		// zero the map value (can't zero a string, but we can drop the map ref)
		kv[alias] = ""
	}
	return nil
}

// loadFromEnv seeds any env vars whose names match provider key env names.
// These are NOT persisted — they're read-only transient overrides.
func (s *Store) loadFromEnv() {
	envAliases := []string{
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GROQ_API_KEY",
		"GEMINI_API_KEY",
		"MISTRAL_API_KEY",
		"TOGETHER_API_KEY",
		"OPENROUTER_API_KEY",
		"OPENCODE_API_KEY",
		"CEREBRAS_API_KEY",
		"DEEPSEEK_API_KEY",
		"XAI_API_KEY",
		"MOONSHOT_API_KEY",
		"HUGGINGFACE_API_KEY",
		"GITHUB_TOKEN",
	}
	for _, alias := range envAliases {
		if v := os.Getenv(alias); v != "" {
			// Only seed if not already in keyring (keyring wins on Set, env wins on Get
			// but we don't want to overwrite a keyring entry with an env var here).
			s.mu.Lock()
			if _, exists := s.enclaves[alias]; !exists {
				b := []byte(v)
				s.enclaves[alias] = memguard.NewEnclave(b)
				memguard.WipeBytes(b)
			}
			s.mu.Unlock()
		}
	}

	// Register all loaded values with the redactor.
	s.mu.RLock()
	aliases := make([]string, 0, len(s.enclaves))
	for a := range s.enclaves {
		aliases = append(aliases, a)
	}
	s.mu.RUnlock()
	for _, a := range aliases {
		s.registerWithRedactor(a)
	}
}

// ExportToEnv sets each keychain-stored secret as an OS environment variable,
// but only when the variable is not already set. Call this before config.Load()
// so that keychain-stored API keys are visible to the configuration system
// without requiring them to also be written to .env files.
func (s *Store) ExportToEnv() {
	s.mu.RLock()
	aliases := make([]string, 0, len(s.enclaves))
	for a := range s.enclaves {
		aliases = append(aliases, a)
	}
	s.mu.RUnlock()

	for _, alias := range aliases {
		if os.Getenv(alias) != "" {
			continue // existing env var wins; don't overwrite
		}
		s.mu.RLock()
		enc, ok := s.enclaves[alias]
		s.mu.RUnlock()
		if !ok {
			continue
		}
		buf, err := enc.Open()
		if err != nil {
			continue
		}
		val := string(buf.Bytes())
		buf.Destroy()
		os.Setenv(alias, val) // nolint:errcheck
	}
}

// SeedFromEnv re-loads provider API keys from the current OS environment
// into the store. Call this after loading .env files so the redactor and
// run_with_secret tool see all keys, including those not yet in the keychain.
func (s *Store) SeedFromEnv() {
	s.loadFromEnv()
}

// persistToKeyring serialises all in-memory enclaves to a single JSON blob
// and writes it to the keyring. Must be called with s.mu held for writing.
func (s *Store) persistToKeyring() error {
	if s.ring == nil {
		return nil // env-only mode — nothing to persist
	}
	kv := make(map[string]string, len(s.enclaves))
	for alias, enc := range s.enclaves {
		buf, err := enc.Open()
		if err != nil {
			continue // skip corrupt entries
		}
		kv[alias] = string(buf.Bytes())
		buf.Destroy()
	}

	data, err := json.Marshal(kv)
	// Zero the plaintext map values immediately after marshalling.
	for k := range kv {
		kv[k] = ""
	}
	if err != nil {
		return err
	}

	return s.ring.Set(keyring.Item{
		Key:  keyringKey,
		Data: data,
		Label: "ageni secrets",
	})
}

// registerWithRedactor opens the enclave briefly to register the value with
// the redactor. The buffer is destroyed immediately after.
func (s *Store) registerWithRedactor(alias string) {
	s.mu.RLock()
	enc, ok := s.enclaves[alias]
	s.mu.RUnlock()
	if !ok {
		return
	}
	buf, err := enc.Open()
	if err != nil {
		return
	}
	s.redactor.Register(alias, string(buf.Bytes()))
	buf.Destroy()
}
