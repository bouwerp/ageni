// Package mcp loads MCP servers configured at ~/.ageni/mcp.json and exposes
// each remote tool as a local ageni tool implementation. Server processes
// run for the lifetime of the ageni session.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bouwerp/ageni/internal/tools"
)

// ServerConfig is one entry in mcp.json.
type ServerConfig struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// FileConfig is the on-disk layout: { "servers": { "<name>": ServerConfig } }.
type FileConfig struct {
	Servers map[string]ServerConfig `json:"servers"`
}

// Manager owns active MCP sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*mcpsdk.ClientSession
	closers  []func()
}

// LoadAndConnect reads ~/.ageni/mcp.json (if it exists) and starts each
// non-disabled server. Returns the manager and a slice of registered Tool
// implementations to add to the registry.
func LoadAndConnect(ctx context.Context) (*Manager, []tools.Tool, error) {
	path, err := configPath()
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		// Missing config is not an error — MCP is optional.
		if os.IsNotExist(err) {
			return &Manager{sessions: map[string]*mcpsdk.ClientSession{}}, nil, nil
		}
		return nil, nil, err
	}
	var cfg FileConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}

	mgr := &Manager{sessions: map[string]*mcpsdk.ClientSession{}}
	var registered []tools.Tool

	impl := &mcpsdk.Implementation{Name: "ageni", Version: "0.1"}
	for name, sc := range cfg.Servers {
		if sc.Disabled {
			continue
		}
		cmd := exec.Command(sc.Command, sc.Args...) //nolint:gosec
		cmd.Env = os.Environ()
		for k, v := range sc.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport := &mcpsdk.CommandTransport{Command: cmd}
		client := mcpsdk.NewClient(impl, nil)
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ageni: mcp server %q failed to start: %v\n", name, err)
			continue
		}
		mgr.sessions[name] = session
		mgr.closers = append(mgr.closers, func() { _ = session.Close() })

		list, err := session.ListTools(ctx, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ageni: mcp server %q list_tools failed: %v\n", name, err)
			continue
		}
		for _, t := range list.Tools {
			registered = append(registered, &mcpTool{
				server:  name,
				session: session,
				toolDef: t,
			})
		}
	}
	return mgr, registered, nil
}

// Close terminates all MCP server processes.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.closers {
		c()
	}
	m.closers = nil
	m.sessions = nil
}

// mcpTool wraps a remote MCP tool as a local Tool.
type mcpTool struct {
	server  string
	session *mcpsdk.ClientSession
	toolDef *mcpsdk.Tool
}

func (t *mcpTool) Name() string {
	return t.server + "__" + t.toolDef.Name
}

func (t *mcpTool) Description() string {
	return fmt.Sprintf("[mcp:%s] %s", t.server, t.toolDef.Description)
}

func (t *mcpTool) Schema() json.RawMessage {
	if t.toolDef.InputSchema == nil {
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	}
	b, err := json.Marshal(t.toolDef.InputSchema)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	}
	return b
}

func (t *mcpTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var argMap any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return "", err
		}
	}
	res, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.toolDef.Name,
		Arguments: argMap,
	})
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			sb.WriteString(tc.Text)
			sb.WriteByte('\n')
		}
	}
	out := strings.TrimRight(sb.String(), "\n")
	if res.IsError {
		return "", fmt.Errorf("mcp tool error: %s", out)
	}
	return out, nil
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ageni", "mcp.json"), nil
}
