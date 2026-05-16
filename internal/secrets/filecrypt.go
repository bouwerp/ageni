package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/awnumar/memguard"
	"github.com/bouwerp/ageni/internal/homedir"
)

// FileVault is an age-encrypted key-value store that lives at a single file
// on disk. It is used as a fallback when no OS keychain daemon is available
// (e.g. headless servers, Docker containers, CI environments without a
// D-Bus session).
//
// The identity (private key) is stored at ~/.ageni/identity.age by default,
// or read from the AGENI_AGE_IDENTITY_FILE env var. On first use, a new
// identity is generated and saved.
//
// The encrypted vault is stored at ~/.ageni/secrets.age.
type FileVault struct {
	identityPath string
	vaultPath    string
}

// DefaultFileVault returns a FileVault pointing at ~/.ageni/secrets.age
// with the identity at ~/.ageni/identity.age.
func DefaultFileVault() (*FileVault, error) {
	home, err := homedir.Dir()
	if err != nil {
		return nil, err
	}
	identPath := filepath.Join(home, ".ageni", "identity.age")
	if v := os.Getenv("AGENI_AGE_IDENTITY_FILE"); v != "" {
		identPath = v
	}
	vaultPath := filepath.Join(home, ".ageni", "secrets.age")
	return &FileVault{identityPath: identPath, vaultPath: vaultPath}, nil
}

// LoadInto decrypts the vault and loads all secrets into the given Store.
// It is idempotent — existing keyring values take precedence; the vault
// only adds entries that are not already present.
func (fv *FileVault) LoadInto(s *Store) error {
	identity, err := fv.loadOrCreateIdentity()
	if err != nil {
		return fmt.Errorf("filevault: identity: %w", err)
	}

	data, err := os.ReadFile(fv.vaultPath) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return nil // vault doesn't exist yet — nothing to load
	}
	if err != nil {
		return fmt.Errorf("filevault: read: %w", err)
	}

	ar := armor.NewReader(bytes.NewReader(data))
	r, err := age.Decrypt(ar, identity)
	if err != nil {
		return fmt.Errorf("filevault: decrypt: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("filevault: read decrypted: %w", err)
	}

	var kv map[string]string
	if err := json.Unmarshal(plain, &kv); err != nil {
		return fmt.Errorf("filevault: corrupt: %w", err)
	}

	// Zero plaintext after parsing.
	memguard.WipeBytes(plain)

	for alias, val := range kv {
		if !s.Has(alias) { // keyring takes precedence
			if err := s.Set(alias, val); err != nil {
				return err
			}
		}
		kv[alias] = ""
	}
	return nil
}

// Save encrypts the current Store contents and writes them to the vault file.
func (fv *FileVault) Save(s *Store) error {
	identity, err := fv.loadOrCreateIdentity()
	if err != nil {
		return fmt.Errorf("filevault: identity: %w", err)
	}

	// Build plaintext map from store.
	aliases := s.List()
	kv := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		val, err := s.Get(alias)
		if err != nil {
			continue
		}
		kv[alias] = val
	}
	plain, err := json.Marshal(kv)
	// Zero string values immediately.
	for k := range kv {
		kv[k] = ""
	}
	if err != nil {
		return err
	}
	defer memguard.WipeBytes(plain)

	// Encrypt with age.
	recipient := identity.Recipient()

	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, recipient)
	if err != nil {
		return fmt.Errorf("filevault: encrypt: %w", err)
	}
	if _, err := w.Write(plain); err != nil {
		return fmt.Errorf("filevault: write encrypted: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("filevault: close encrypted: %w", err)
	}
	if err := aw.Close(); err != nil {
		return fmt.Errorf("filevault: close armor: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(fv.vaultPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(fv.vaultPath, buf.Bytes(), 0o600)
}

// loadOrCreateIdentity reads the age identity from disk, or generates a new
// one if it doesn't exist yet.
func (fv *FileVault) loadOrCreateIdentity() (*age.X25519Identity, error) {
	data, err := os.ReadFile(fv.identityPath) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return fv.generateIdentity()
	}
	if err != nil {
		return nil, err
	}

	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse identity: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("identity file is empty")
	}
	id, ok := ids[0].(*age.X25519Identity)
	if !ok {
		return nil, fmt.Errorf("unexpected identity type")
	}
	return id, nil
}

// generateIdentity creates a new X25519 identity and persists it at
// identityPath with 0600 permissions.
func (fv *FileVault) generateIdentity() (*age.X25519Identity, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(fv.identityPath), 0o700); err != nil {
		return nil, err
	}
	privStr := identity.String()
	if err := os.WriteFile(fv.identityPath, []byte(privStr+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write identity: %w", err)
	}
	return identity, nil
}
