package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/ageni/internal/lsp"
)

func TestLSPTools_Integrations(t *testing.T) {
	// Setup pipes for mock LSP server
	rIn, wIn := io.Pipe()
	rOut, wOut := io.Pipe()

	onNotify := func(method string, params json.RawMessage) {
		if method == "textDocument/publishDiagnostics" {
			var diagParams lsp.PublishDiagnosticsParams
			if err := json.Unmarshal(params, &diagParams); err == nil {
				lsp.GlobalLSPManager.PublishDiagnostics(diagParams.URI, diagParams.Diagnostics)
			}
		}
	}

	client := lsp.NewClientMock(wIn, rOut, onNotify)
	defer client.Close()

	// Inject the mock client into GlobalLSPManager for "go"
	lsp.GlobalLSPManager.CloseAll()
	lsp.GlobalLSPManager.Init("/workspace")
	lsp.GlobalLSPManager.RegisterClient("go", client)

	// Start a mock server processing requests
	go func() {
		reader := bufio.NewReader(rIn)
		for {
			var contentLength int
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					break
				}
				if strings.HasPrefix(strings.ToLower(line), "content-length:") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						val := strings.TrimSpace(parts[1])
						contentLength, _ = strconv.Atoi(val)
					}
				}
			}

			if contentLength <= 0 {
				continue
			}

			body := make([]byte, contentLength)
			if _, err := io.ReadFull(reader, body); err != nil {
				return
			}

			var req struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      int64           `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				continue
			}

			switch req.Method {
			case "textDocument/definition":
				res := struct {
					JSONRPC string       `json:"jsonrpc"`
					ID      *int64       `json:"id"`
					Result  lsp.Location `json:"result"`
				}{
					JSONRPC: "2.0",
					ID:      &req.ID,
					Result: lsp.Location{
						URI: "file:///workspace/target.go",
						Range: lsp.Range{
							Start: lsp.Position{Line: 10, Character: 5},
							End:   lsp.Position{Line: 10, Character: 10},
						},
					},
				}
				data, _ := json.Marshal(res)
				_, _ = wOut.Write([]byte("Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"))
				_, _ = wOut.Write(data)

			case "textDocument/references":
				res := struct {
					JSONRPC string         `json:"jsonrpc"`
					ID      *int64         `json:"id"`
					Result  []lsp.Location `json:"result"`
				}{
					JSONRPC: "2.0",
					ID:      &req.ID,
					Result: []lsp.Location{
						{
							URI: "file:///workspace/ref1.go",
							Range: lsp.Range{
								Start: lsp.Position{Line: 20, Character: 8},
								End:   lsp.Position{Line: 20, Character: 15},
							},
						},
						{
							URI: "file:///workspace/ref2.go",
							Range: lsp.Range{
								Start: lsp.Position{Line: 30, Character: 12},
								End:   lsp.Position{Line: 30, Character: 20},
							},
						},
					},
				}
				data, _ := json.Marshal(res)
				_, _ = wOut.Write([]byte("Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"))
				_, _ = wOut.Write(data)

			case "textDocument/hover":
				res := struct {
					JSONRPC string `json:"jsonrpc"`
					ID      *int64 `json:"id"`
					Result  struct {
						Contents string `json:"contents"`
					} `json:"result"`
				}{
					JSONRPC: "2.0",
					ID:      &req.ID,
					Result: struct {
						Contents string `json:"contents"`
					}{
						Contents: "func Hello() string\n\nSays hello.",
					},
				}
				data, _ := json.Marshal(res)
				_, _ = wOut.Write([]byte("Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"))
				_, _ = wOut.Write(data)
			}
		}
	}()

	ctx := context.Background()

	// 1. Test LSPDefinition.Call
	defTool := LSPDefinition{}
	defArgs, _ := json.Marshal(map[string]any{
		"path":      "test.go",
		"line":      5,
		"character": 10,
	})
	defOut, err := defTool.Call(ctx, defArgs)
	if err != nil {
		t.Fatalf("LSPDefinition failed: %v", err)
	}
	expectedDef := filepath.FromSlash("/workspace/target.go") + ":11:6"
	if !strings.Contains(defOut, expectedDef) {
		t.Errorf("expected definition to contain %q, got: %q", expectedDef, defOut)
	}

	// 2. Test LSPReferences.Call
	refTool := LSPReferences{}
	refArgs, _ := json.Marshal(map[string]any{
		"path":      "test.go",
		"line":      5,
		"character": 10,
	})
	refOut, err := refTool.Call(ctx, refArgs)
	if err != nil {
		t.Fatalf("LSPReferences failed: %v", err)
	}
	expectedRef1 := filepath.FromSlash("/workspace/ref1.go") + ":21:9"
	expectedRef2 := filepath.FromSlash("/workspace/ref2.go") + ":31:13"
	if !strings.Contains(refOut, expectedRef1) || !strings.Contains(refOut, expectedRef2) {
		t.Errorf("expected references to contain %q and %q, got: %q", expectedRef1, expectedRef2, refOut)
	}

	// 3. Test LSPHover.Call
	hovTool := LSPHover{}
	hovArgs, _ := json.Marshal(map[string]any{
		"path":      "test.go",
		"line":      5,
		"character": 10,
	})
	hovOut, err := hovTool.Call(ctx, hovArgs)
	if err != nil {
		t.Fatalf("LSPHover failed: %v", err)
	}
	expectedHover := "func Hello() string\n\nSays hello."
	if hovOut != expectedHover {
		t.Errorf("expected hover output %q, got: %q", expectedHover, hovOut)
	}

	// 4. Test LSPDiagnostics.Call
	diagParams := lsp.PublishDiagnosticsParams{
		URI: "file:///workspace/target.go",
		Diagnostics: []lsp.Diagnostic{
			{
				Range: lsp.Range{
					Start: lsp.Position{Line: 1, Character: 2},
					End:   lsp.Position{Line: 1, Character: 5},
				},
				Severity: lsp.SeverityError,
				Message:  "syntax error",
				Source:   "gopls",
			},
		},
	}
	// Simulate publication of diagnostics from server
	diagData, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  diagParams,
	})
	_, _ = wOut.Write([]byte("Content-Length: " + strconv.Itoa(len(diagData)) + "\r\n\r\n"))
	_, _ = wOut.Write(diagData)

	// Wait briefly for notification loop to handle diagnostics
	time.Sleep(50 * time.Millisecond)

	diagTool := LSPDiagnostics{}
	diagArgs, _ := json.Marshal(map[string]any{
		"path_prefix": "/workspace",
	})
	diagOut, err := diagTool.Call(ctx, diagArgs)
	if err != nil {
		t.Fatalf("LSPDiagnostics failed: %v", err)
	}
	expectedDiag := "[ERROR] Line 2:3 - syntax error (Source: gopls)"
	if !strings.Contains(diagOut, expectedDiag) {
		t.Errorf("expected diagnostics output to contain %q, got: %q", expectedDiag, diagOut)
	}
}
