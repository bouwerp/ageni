package llm

// OllamaCloudAdapter calls the native Ollama API at https://ollama.com/api/chat
// (not the OpenAI-compatible shim at api.ollama.com). The wire format is
// Ollama's own JSON: tool call arguments arrive as objects (not JSON strings),
// and the streaming protocol uses NDJSON with a "done" boolean rather than
// OpenAI's SSE finish_reason.
//
// Auth: Authorization: Bearer <apiKey>  (same header, different endpoint).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OllamaCloudAdapter implements Adapter against the native Ollama Cloud API.
type OllamaCloudAdapter struct {
	apiKey  string
	baseURL string // e.g. "https://ollama.com"
}

func NewOllamaCloudAdapter(apiKey, baseURL string) *OllamaCloudAdapter {
	if baseURL == "" {
		baseURL = "https://ollama.com"
	}
	return &OllamaCloudAdapter{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (o *OllamaCloudAdapter) Provider() string { return "ollama-cloud" }

// --- wire types ---

type ollamaMessage struct {
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	ToolCalls []ollamaToolCall  `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolCallFunc `json:"function"`
}

type ollamaToolCallFunc struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // Ollama sends an object, not a string
}

type ollamaTool struct {
	Type     string           `json:"type"`
	Function ollamaToolSchema `json:"function"`
}

type ollamaToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
}

type ollamaStreamChunk struct {
	Model   string        `json:"model"`
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	// Usage fields present in the final done=true chunk.
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
	// Error field (non-stream errors).
	Error string `json:"error"`
}

// --- message conversion ---

func (o *OllamaCloudAdapter) buildMessages(req Request) []ollamaMessage {
	var out []ollamaMessage
	if req.System != "" {
		out = append(out, ollamaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			out = append(out, ollamaMessage{Role: "user", Content: SanitizeText(m.Text)})
		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				calls := make([]ollamaToolCall, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					args := sanitizeArgs(tc.Arguments)
					// Ensure arguments is a valid JSON object for Ollama.
					// (OpenAI stores them as a JSON-encoded string sometimes.)
					var obj json.RawMessage
					if err := json.Unmarshal(args, &obj); err != nil {
						obj = json.RawMessage("{}")
					}
					calls = append(calls, ollamaToolCall{
						Function: ollamaToolCallFunc{Name: tc.Name, Arguments: obj},
					})
				}
				out = append(out, ollamaMessage{
					Role:      "assistant",
					Content:   SanitizeText(m.Text),
					ToolCalls: calls,
				})
			} else {
				out = append(out, ollamaMessage{Role: "assistant", Content: SanitizeText(m.Text)})
			}
		case RoleTool:
			for _, tr := range m.ToolResults {
				out = append(out, ollamaMessage{
					Role:    "tool",
					Content: SanitizeText(tr.Content),
				})
			}
		}
	}
	return out
}

func buildOllamaTools(defs []ToolDef) []ollamaTool {
	out := make([]ollamaTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, ollamaTool{
			Type: "function",
			Function: ollamaToolSchema{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Schema,
			},
		})
	}
	return out
}

// --- Stream ---

func (o *OllamaCloudAdapter) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	body := ollamaChatRequest{
		Model:    req.Model,
		Messages: o.buildMessages(req),
		Stream:   true,
		Tools:    buildOllamaTools(req.Tools),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollamacloud: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("ollamacloud: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollamacloud: POST /api/chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body2, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("ollamacloud: POST /api/chat: %d %s", resp.StatusCode, strings.TrimSpace(string(body2)))
	}

	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		// Ollama streams arbitrarily large tool-result lines; give the scanner
		// a large enough buffer.
		scanner.Buffer(make([]byte, 256*1024), 256*1024)

		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var chunk ollamaStreamChunk
			if err := json.Unmarshal(line, &chunk); err != nil {
				out <- StreamEvent{Type: StreamEventError, Err: fmt.Errorf("ollamacloud: decode chunk: %w", err)}
				return
			}
			if chunk.Error != "" {
				out <- StreamEvent{Type: StreamEventError, Err: fmt.Errorf("ollamacloud: %s", chunk.Error)}
				return
			}

			// Emit tool calls if present.
			for _, tc := range chunk.Message.ToolCalls {
				args := sanitizeArgs(tc.Function.Arguments)
				out <- StreamEvent{
					Type: StreamEventToolCall,
					ToolCall: &ToolCall{
						ID:        "", // Ollama doesn't provide tool call IDs
						Name:      strings.TrimSpace(tc.Function.Name),
						Arguments: args,
					},
				}
			}

			// Emit text delta if present.
			if text := chunk.Message.Content; text != "" {
				out <- StreamEvent{Type: StreamEventText, TextDelta: text}
			}

			// Final chunk carries usage counts.
			if chunk.Done {
				out <- StreamEvent{
					Type: StreamEventDone,
					Usage: &Usage{
						InputTokens:  chunk.PromptEvalCount,
						OutputTokens: chunk.EvalCount,
					},
				}
				return
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			out <- StreamEvent{Type: StreamEventError, Err: fmt.Errorf("ollamacloud: read stream: %w", err)}
		}
	}()

	return out, nil
}
