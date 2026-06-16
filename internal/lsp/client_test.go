package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClient_CallAndNotify(t *testing.T) {
	// Setup pipes for mock communication
	rIn, wIn := io.Pipe()
	rOut, wOut := io.Pipe()

	notifications := make(chan string, 10)
	onNotify := func(method string, params json.RawMessage) {
		notifications <- method + ":" + string(params)
	}

	client := &Client{
		stdin:    wIn,
		stdout:   rOut,
		pending:  make(map[int64]chan<- *response),
		onNotify: onNotify,
		done:     make(chan struct{}),
	}
	go client.readLoop()
	defer client.Close()

	// Run mock server in a goroutine
	go func() {
		reader := bufio.NewReader(rIn)
		for {
			// Read headers
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

			// Respond to definition request
			if req.Method == "textDocument/definition" {
				res := response{
					JSONRPC: "2.0",
					ID:      &req.ID,
					Result:  json.RawMessage(`{"uri": "file:///test.go", "range": {"start": {"line": 1, "character": 2}}}`),
				}
				data, _ := json.Marshal(res)
				_, _ = wOut.Write([]byte("Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n"))
				_, _ = wOut.Write(data)
			}
		}
	}()

	// 1. Test Client.Call
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var result struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}

	err := client.Call(ctx, "textDocument/definition", map[string]any{"uri": "file:///test.go"}, &result)
	if err != nil {
		t.Fatalf("client.Call failed: %v", err)
	}

	if result.URI != "file:///test.go" {
		t.Errorf("unexpected uri: %s", result.URI)
	}
	if result.Range.Start.Line != 1 || result.Range.Start.Character != 2 {
		t.Errorf("unexpected range: %+v", result.Range)
	}

	// 2. Test Client.Notify (incoming to client)
	notifData := []byte(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///test.go"}}`)
	_, _ = wOut.Write([]byte("Content-Length: " + strconv.Itoa(len(notifData)) + "\r\n\r\n"))
	_, _ = wOut.Write(notifData)

	select {
	case methodMsg := <-notifications:
		if !strings.HasPrefix(methodMsg, "textDocument/publishDiagnostics:") {
			t.Errorf("unexpected notification: %s", methodMsg)
		}
	case <-time.After(2 * time.Second):
		t.Error("timed out waiting for notification")
	}
}
