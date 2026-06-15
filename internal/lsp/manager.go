package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Manager coordinates multiple active language servers.
type Manager struct {
	mu          sync.Mutex
	rootPath    string
	clients     map[string]*Client
	openFiles   map[string]int // maps file path -> current version
	diagnostics map[string][]Diagnostic
}

// GlobalLSPManager is the process-wide LSP manager.
var GlobalLSPManager = &Manager{
	clients:     make(map[string]*Client),
	openFiles:   make(map[string]int),
	diagnostics: make(map[string][]Diagnostic),
}

// Init sets the workspace root path for the manager.
func (m *Manager) Init(rootPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	abs, err := filepath.Abs(rootPath)
	if err == nil {
		m.rootPath = abs
	} else {
		m.rootPath = rootPath
	}
}

// CloseAll terminates all active language servers.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = make(map[string]*Client)
	m.openFiles = make(map[string]int)
}

func (m *Manager) detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript"
	default:
		return ""
	}
}

func pathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file://" + filepath.ToSlash(abs)
}

func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	return filepath.FromSlash(strings.TrimPrefix(uri, "file://"))
}

// PathToURI converts a file path to an LSP file:// URI.
func PathToURI(path string) string {
	return pathToURI(path)
}

// URIToPath converts an LSP file:// URI back to a file path.
func URIToPath(uri string) string {
	return uriToPath(uri)
}

// GetClient returns an active client for the given language, starting it if necessary.
func (m *Manager) GetClient(ctx context.Context, lang string) (*Client, error) {
	if lang == "" {
		return nil, fmt.Errorf("unsupported language")
	}
	m.mu.Lock()
	if c, ok := m.clients[lang]; ok {
		m.mu.Unlock()
		return c, nil
	}
	root := m.rootPath
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
			m.rootPath = wd
		}
	}
	m.mu.Unlock()

	var exe string
	var args []string
	switch lang {
	case "go":
		if p, err := exec.LookPath("gopls"); err == nil {
			exe = p
		}
	case "typescript":
		if p, err := exec.LookPath("typescript-language-server"); err == nil {
			exe = p
			args = []string{"--stdio"}
		} else if p, err := exec.LookPath("vtsls"); err == nil {
			exe = p
			args = []string{"--stdio"}
		}
	case "python":
		if p, err := exec.LookPath("pyright-langserver"); err == nil {
			exe = p
			args = []string{"--stdio"}
		} else if p, err := exec.LookPath("jedi-language-server"); err == nil {
			exe = p
			args = []string{"--stdio"}
		}
	}

	if exe == "" {
		return nil, fmt.Errorf("no language server executable found for %s", lang)
	}

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = root

	client, err := NewClient(cmd, func(method string, params json.RawMessage) {
		m.handleNotification(lang, method, params)
	})
	if err != nil {
		return nil, err
	}

	initParams := InitializeParams{
		ProcessID: os.Getpid(),
		RootPath:  root,
		RootURI:   "file://" + filepath.ToSlash(root),
		Capabilities: ClientCapabilities{
			TextDocument: map[string]any{
				"synchronization": map[string]any{
					"dynamicRegistration": true,
					"didSave":             true,
				},
			},
		},
	}

	var initResult InitializeResult
	if err := client.Call(ctx, "initialize", initParams, &initResult); err != nil {
		_ = client.Close()
		return nil, err
	}

	if err := client.Notify("initialized", struct{}{}); err != nil {
		_ = client.Close()
		return nil, err
	}

	m.mu.Lock()
	m.clients[lang] = client
	m.mu.Unlock()

	return client, nil
}

func (m *Manager) handleNotification(lang, method string, params json.RawMessage) {
	if method == "textDocument/publishDiagnostics" {
		var diagParams PublishDiagnosticsParams
		if err := json.Unmarshal(params, &diagParams); err == nil {
			m.PublishDiagnostics(diagParams.URI, diagParams.Diagnostics)
		}
	}
}

// PublishDiagnostics sets the diagnostics for a file URI.
func (m *Manager) PublishDiagnostics(uri string, diags []Diagnostic) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.diagnostics[uri] = diags
}

// OpenFile notifies the server that a file was opened.
func (m *Manager) OpenFile(ctx context.Context, path string, content string) error {
	lang := m.detectLanguage(path)
	client, err := m.GetClient(ctx, lang)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.openFiles[path] = 1
	m.mu.Unlock()

	return client.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        pathToURI(path),
			LanguageID: lang,
			Version:    1,
			Text:       content,
		},
	})
}

// ChangeFile notifies the server that a file was modified.
func (m *Manager) ChangeFile(ctx context.Context, path string, content string) error {
	lang := m.detectLanguage(path)
	client, err := m.GetClient(ctx, lang)
	if err != nil {
		return err
	}
	m.mu.Lock()
	ver := m.openFiles[path] + 1
	m.openFiles[path] = ver
	m.mu.Unlock()

	return client.Notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: TextDocumentIdentifier{URI: pathToURI(path)},
			Version:                ver,
		},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: content},
		},
	})
}

// CloseFile notifies the server that a file was closed.
func (m *Manager) CloseFile(ctx context.Context, path string) error {
	lang := m.detectLanguage(path)
	client, err := m.GetClient(ctx, lang)
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.openFiles, path)
	m.mu.Unlock()

	return client.Notify("textDocument/didClose", DidCloseTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
	})
}

// UpdateFile handles didOpen or didChange automatically based on whether the file is tracked.
func (m *Manager) UpdateFile(ctx context.Context, path string, content string) error {
	m.mu.Lock()
	_, isOpen := m.openFiles[path]
	m.mu.Unlock()

	if !isOpen {
		return m.OpenFile(ctx, path, content)
	}
	return m.ChangeFile(ctx, path, content)
}

// GetDefinition queries textDocument/definition.
func (m *Manager) GetDefinition(ctx context.Context, path string, line, char int) ([]Location, error) {
	lang := m.detectLanguage(path)
	client, err := m.GetClient(ctx, lang)
	if err != nil {
		return nil, err
	}

	type TextDocumentPositionParams struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
		Position     Position               `json:"position"`
	}
	params := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: char},
	}

	var res json.RawMessage
	if err := client.Call(ctx, "textDocument/definition", params, &res); err != nil {
		return nil, err
	}

	if len(res) == 0 || string(res) == "null" {
		return nil, nil
	}

	if res[0] == '[' {
		var locs []Location
		if err := json.Unmarshal(res, &locs); err != nil {
			return nil, err
		}
		return locs, nil
	}

	var loc Location
	if err := json.Unmarshal(res, &loc); err != nil {
		return nil, err
	}
	return []Location{loc}, nil
}

// GetReferences queries textDocument/references.
func (m *Manager) GetReferences(ctx context.Context, path string, line, char int) ([]Location, error) {
	lang := m.detectLanguage(path)
	client, err := m.GetClient(ctx, lang)
	if err != nil {
		return nil, err
	}

	type ReferenceContext struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	}
	type ReferenceParams struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
		Position     Position               `json:"position"`
		Context      ReferenceContext       `json:"context"`
	}
	params := ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: char},
		Context:      ReferenceContext{IncludeDeclaration: true},
	}

	var locs []Location
	if err := client.Call(ctx, "textDocument/references", params, &locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// GetHover queries textDocument/hover.
func (m *Manager) GetHover(ctx context.Context, path string, line, char int) (string, error) {
	lang := m.detectLanguage(path)
	client, err := m.GetClient(ctx, lang)
	if err != nil {
		return "", err
	}

	type TextDocumentPositionParams struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
		Position     Position               `json:"position"`
	}
	params := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: char},
	}

	var res struct {
		Contents any `json:"contents"`
	}
	if err := client.Call(ctx, "textDocument/hover", params, &res); err != nil {
		return "", err
	}

	return parseHoverContents(res.Contents), nil
}

func parseHoverContents(contents any) string {
	if contents == nil {
		return ""
	}
	switch v := contents.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(parseHoverContents(item))
		}
		return sb.String()
	case map[string]any:
		if val, ok := v["value"].(string); ok {
			return val
		}
	}
	data, _ := json.Marshal(contents)
	return string(data)
}

// GetDiagnostics returns list of diagnostics for the given path prefix.
func (m *Manager) GetDiagnostics(pathPrefix string) []Diagnostic {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Diagnostic
	prefixURI := pathToURI(pathPrefix)
	for uri, diags := range m.diagnostics {
		if pathPrefix == "" || strings.HasPrefix(uri, prefixURI) {
			out = append(out, diags...)
		}
	}
	return out
}

// RegisterClient registers a client for a language (primarily for testing).
func (m *Manager) RegisterClient(lang string, client *Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[lang] = client
}
