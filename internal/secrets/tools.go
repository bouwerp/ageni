package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// NewListSecretsTool creates a ListSecretsTool using the given store.
func NewListSecretsTool(s *Store) ListSecretsTool { return ListSecretsTool{Store: s} }

// NewRunWithSecretTool creates a RunWithSecretTool using the given store.
func NewRunWithSecretTool(s *Store) RunWithSecretTool { return RunWithSecretTool{Store: s} }

// NewHTTPWithAuthTool creates an HTTPWithAuthTool using the given store.
func NewHTTPWithAuthTool(s *Store) HTTPWithAuthTool { return HTTPWithAuthTool{Store: s} }

// ListSecretsTool exposes the names (aliases) of all stored secrets to the
// agent. Values are NEVER returned — only names. Safe to include in LLM
// tool responses.
type ListSecretsTool struct {
	Store *Store
}

func (ListSecretsTool) Name() string        { return "list_secrets" }
func (ListSecretsTool) Description() string {
	return "List the names (aliases) of all secrets stored in the vault. " +
		"Returns names only — values are never exposed. " +
		"Use this to discover which credentials are available before calling run_with_secret or http_with_auth."
}
func (ListSecretsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}
func (t ListSecretsTool) Call(_ context.Context, _ json.RawMessage) (string, error) {
	names := t.Store.List()
	if len(names) == 0 {
		return "No secrets stored. Ask the user to run `ageni init` to configure credentials.", nil
	}
	return "Available secrets (names only):\n" + strings.Join(names, "\n"), nil
}

// RunWithSecretTool executes a shell command with one or more secrets
// injected into the subprocess environment at fork time. Secret values
// are NEVER returned to the agent — only the command's stdout/stderr output
// (which is also scrubbed for accidental leakage).
type RunWithSecretTool struct {
	Store *Store
}

func (RunWithSecretTool) Name() string        { return "run_with_secret" }
func (RunWithSecretTool) Description() string {
	return "Run a shell command with secrets injected as environment variables at execution time. " +
		"Secret values are never loaded into context — only the command output is returned. " +
		"Use this whenever a command needs credentials (API calls, CI scripts, authenticated CLIs). " +
		"The output is automatically scrubbed to prevent accidental secret leakage."
}
func (RunWithSecretTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"command":{"type":"string","description":"Shell command to execute (run via bash -lc)"},
			"inject_secrets":{"type":"array","items":{"type":"string"},"description":"List of secret aliases to inject as env vars (e.g. [\"ANTHROPIC_API_KEY\",\"GITHUB_TOKEN\"])"},
			"timeout_seconds":{"type":"integer","description":"Maximum execution time in seconds (default 60)"}
		},
		"required":["command","inject_secrets"]
	}`)
}

func (t RunWithSecretTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command        string   `json:"command"`
		InjectSecrets  []string `json:"inject_secrets"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("run_with_secret: invalid args: %w", err)
	}
	if p.Command == "" {
		return "", fmt.Errorf("run_with_secret: command is required")
	}
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 60
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()

	// Build env: inherit current env, then inject requested secrets.
	env := os.Environ()
	for _, alias := range p.InjectSecrets {
		val, err := t.Store.Get(alias)
		if err != nil {
			return "", fmt.Errorf("run_with_secret: secret %q not found — check list_secrets", alias)
		}
		env = append(env, alias+"="+val)
		// val will be GC'd; we can't zero a string but we minimise the window.
	}

	cmd := exec.CommandContext(ctx, "bash", "-lc", p.Command)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	output := string(out)

	// Scrub any accidental secret leakage from command output.
	output = t.Store.Redactor().Scrub(output)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("run_with_secret: timed out after %ds", p.TimeoutSeconds)
		}
		return output, fmt.Errorf("run_with_secret: exit %w", err)
	}
	return output, nil
}

// HTTPWithAuthTool makes an authenticated HTTP request using a stored secret.
// The credential is injected into the request headers at call time and is
// never returned to the LLM — only the response body.
type HTTPWithAuthTool struct {
	Store *Store
}

func (HTTPWithAuthTool) Name() string        { return "http_with_auth" }
func (HTTPWithAuthTool) Description() string {
	return "Make an HTTP request authenticated with a stored secret. " +
		"The credential value is never exposed to context — only the response body is returned. " +
		"Supported auth types: bearer (Authorization: Bearer <value>), " +
		"basic (Authorization: Basic base64(alias:value)), " +
		"header (X-Api-Key: <value>)."
}
func (HTTPWithAuthTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"url":{"type":"string","description":"The URL to request"},
			"method":{"type":"string","enum":["GET","POST","PUT","PATCH","DELETE"],"description":"HTTP method (default GET)"},
			"body":{"type":"string","description":"Request body for POST/PUT/PATCH (optional)"},
			"content_type":{"type":"string","description":"Content-Type header (default application/json)"},
			"secret_alias":{"type":"string","description":"Alias of the secret to use for authentication"},
			"auth_type":{"type":"string","enum":["bearer","basic","header"],"description":"How to pass the secret: bearer=Authorization header, basic=HTTP Basic (alias:secret), header=X-Api-Key header"}
		},
		"required":["url","secret_alias","auth_type"]
	}`)
}

func (t HTTPWithAuthTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL         string `json:"url"`
		Method      string `json:"method"`
		Body        string `json:"body"`
		ContentType string `json:"content_type"`
		SecretAlias string `json:"secret_alias"`
		AuthType    string `json:"auth_type"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("http_with_auth: invalid args: %w", err)
	}
	if p.Method == "" {
		p.Method = "GET"
	}
	if p.ContentType == "" {
		p.ContentType = "application/json"
	}

	secretVal, err := t.Store.Get(p.SecretAlias)
	if err != nil {
		return "", fmt.Errorf("http_with_auth: secret %q not found", p.SecretAlias)
	}

	var bodyReader io.Reader
	if p.Body != "" {
		bodyReader = strings.NewReader(p.Body)
	}

	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("http_with_auth: build request: %w", err)
	}
	if p.Body != "" {
		req.Header.Set("Content-Type", p.ContentType)
	}

	switch p.AuthType {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+secretVal)
	case "basic":
		req.SetBasicAuth(p.SecretAlias, secretVal)
	case "header":
		req.Header.Set("X-Api-Key", secretVal)
	default:
		return "", fmt.Errorf("http_with_auth: unknown auth_type %q", p.AuthType)
	}
	// secretVal is no longer referenced after this point.

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http_with_auth: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return "", fmt.Errorf("http_with_auth: read response: %w", err)
	}

	result := fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(respBody))
	// Scrub any accidental secret echo in the response.
	return t.Store.Redactor().Scrub(result), nil
}

// RequestSecretStoreTool lets the agent ask the user to store a new secret
// without the agent ever seeing the value. A channel-based request is sent
// to the TUI which prompts the user for the value.
//
// This tool returns only "stored" or an error — never the value.
type RequestSecretStoreTool struct {
	Requests chan<- SecretStoreRequest
}

// SecretStoreRequest is sent from the agent to the TUI layer asking the user
// to store a new secret. The result (ok/error) is returned via Reply.
type SecretStoreRequest struct {
	Alias string
	Reply chan<- error
}

func (RequestSecretStoreTool) Name() string        { return "request_secret_store" }
func (RequestSecretStoreTool) Description() string {
	return "Ask the user (via a secure TUI prompt) to store a new secret under a given alias. " +
		"The agent never sees the value — only receives confirmation that it was stored. " +
		"Use this when a required credential is missing from list_secrets."
}
func (RequestSecretStoreTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"alias":{"type":"string","description":"The alias/name for the secret (e.g. GITHUB_TOKEN)"},
			"description":{"type":"string","description":"Human-readable description shown to the user in the prompt"}
		},
		"required":["alias"]
	}`)
}

func (t RequestSecretStoreTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Alias       string `json:"alias"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("request_secret_store: invalid args: %w", err)
	}
	if p.Alias == "" {
		return "", fmt.Errorf("request_secret_store: alias is required")
	}

	reply := make(chan error, 1)
	select {
	case t.Requests <- SecretStoreRequest{Alias: p.Alias, Reply: reply}:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	select {
	case err := <-reply:
		if err != nil {
			return "", fmt.Errorf("request_secret_store: user cancelled or error: %w", err)
		}
		return fmt.Sprintf("Secret %q stored successfully.", p.Alias), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
