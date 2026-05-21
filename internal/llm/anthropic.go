package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicAdapter implements Adapter against the Anthropic Messages API.
//
// Prompt caching: the system prompt is sent with cache_control=ephemeral so
// repeated calls within the cache TTL re-use it. Tools are sorted by name
// (deterministic) so their serialized form is stable across calls — the SDK
// caches them implicitly when the request prefix is byte-identical.
type AnthropicAdapter struct {
	client anthropic.Client
}

func NewAnthropicAdapter(apiKey string) *AnthropicAdapter {
	return &AnthropicAdapter{
		client: anthropic.NewClient(
			option.WithAPIKey(apiKey),
			option.WithMaxRetries(4),
		),
	}
}

func (a *AnthropicAdapter) Provider() string { return "anthropic" }

func (a *AnthropicAdapter) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	params, err := a.buildParams(req)
	if err != nil {
		return nil, err
	}

	// Auto-compaction at 120k input tokens: when the conversation grows
	// past this point Anthropic summarises older turns server-side and
	// only the summary + recent turns are billed for the next request.
	// The summary_prompt is tuned to preserve the load-bearing state for
	// our master/sub-agent flow.
	stream := a.client.Messages.NewStreaming(ctx, params,
		option.WithJSONSet("context_management", map[string]any{
			"edits": []map[string]any{{
				"type": "compact_20260112",
				"trigger": map[string]any{
					"type":  "input_tokens",
					"value": 120000,
				},
				"summary_prompt": "Preserve precisely: the user's original objective, the current todo list with statuses, every file path that has been modified or read, the last test/build outcome, sub-agent IDs and their final outputs, and any unresolved errors. Drop verbose reasoning and intermediate tool-call output that has already informed a later step.",
			}},
		}),
	)
	out := make(chan StreamEvent, 16)

	go func() {
		defer close(out)
		// Per-block accumulators for tool_use blocks.
		type pendingTool struct {
			id    string
			name  string
			input string
		}
		pending := make(map[int64]*pendingTool)
		var usage Usage

		for stream.Next() {
			evt := stream.Current()
			switch v := evt.AsAny().(type) {
			case anthropic.MessageStartEvent:
				if v.Message.Usage.InputTokens != 0 || v.Message.Usage.CacheReadInputTokens != 0 {
					usage.InputTokens = int(v.Message.Usage.InputTokens)
					usage.CacheReadTokens = int(v.Message.Usage.CacheReadInputTokens)
					usage.CacheCreationTokens = int(v.Message.Usage.CacheCreationInputTokens)
				}
			case anthropic.ContentBlockStartEvent:
				if v.ContentBlock.Type == "tool_use" {
					pending[v.Index] = &pendingTool{
						id:   v.ContentBlock.ID,
						name: v.ContentBlock.Name,
					}
				}
			case anthropic.ContentBlockDeltaEvent:
				switch v.Delta.Type {
				case "text_delta":
					if v.Delta.Text != "" {
						out <- StreamEvent{Type: StreamEventText, TextDelta: v.Delta.Text}
					}
				case "input_json_delta":
					if t, ok := pending[v.Index]; ok {
						t.input += v.Delta.PartialJSON
					}
				case "thinking_delta":
					if v.Delta.Thinking != "" {
						out <- StreamEvent{Type: StreamEventThinking, TextDelta: v.Delta.Thinking}
					}
				}
			case anthropic.ContentBlockStopEvent:
				if t, ok := pending[v.Index]; ok {
					out <- StreamEvent{
						Type: StreamEventToolCall,
						ToolCall: &ToolCall{
							ID:        t.id,
							Name:      t.name,
							Arguments: sanitizeArgs([]byte(t.input)),
						},
					}
					delete(pending, v.Index)
				}
			case anthropic.MessageDeltaEvent:
				usage.OutputTokens = int(v.Usage.OutputTokens)
			case anthropic.MessageStopEvent:
				// handled below via stream.Next() returning false
			}
		}
		if err := stream.Err(); err != nil {
			out <- StreamEvent{Type: StreamEventError, Err: WrapProviderError(a.Provider(), req.Model, "stream", err)}
			return
		}
		u := usage
		out <- StreamEvent{Type: StreamEventDone, Usage: &u}
	}()

	return out, nil
}

func (a *AnthropicAdapter) buildParams(req Request) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(req.MaxTokens),
	}
	if req.MaxTokens == 0 {
		params.MaxTokens = 4096
	}
	if req.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Temperature)
	}

	// System prompt — cached.
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         SanitizeText(req.System),
			CacheControl: anthropic.CacheControlEphemeralParam{},
		}}
	}

	// Tools — already sorted alphabetically by registry. Mark the LAST tool
	// with cache_control to extend the cached prefix through all tools.
	if n := len(req.Tools); n > 0 {
		params.Tools = make([]anthropic.ToolUnionParam, 0, n)
		for i, t := range req.Tools {
			tp := anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: rawSchemaToToolInputSchema(t.Schema),
			}
			if i == n-1 {
				tp.CacheControl = anthropic.CacheControlEphemeralParam{}
			}
			params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &tp})
		}
	}

	// Messages.
	params.Messages = make([]anthropic.MessageParam, 0, len(req.Messages))
	for i, m := range req.Messages {
		mp, err := messageToAnthropic(m, i == len(req.Messages)-1)
		if err != nil {
			return params, err
		}
		params.Messages = append(params.Messages, mp)
	}

	return params, nil
}

func messageToAnthropic(m Message, last bool) (anthropic.MessageParam, error) {
	var blocks []anthropic.ContentBlockParamUnion

	if m.Text != "" {
		blocks = append(blocks, anthropic.NewTextBlock(SanitizeText(m.Text)))
	}
	for _, tc := range m.ToolCalls {
		var input any
		clean := sanitizeArgs(tc.Arguments)
		if len(clean) > 0 {
			if err := json.Unmarshal(clean, &input); err != nil {
				return anthropic.MessageParam{}, fmt.Errorf("tool call %s: %w", tc.Name, err)
			}
		} else {
			input = map[string]any{}
		}
		blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
	}
	for _, tr := range m.ToolResults {
		blocks = append(blocks, anthropic.NewToolResultBlock(tr.ToolCallID, SanitizeText(tr.Content), tr.IsError))
	}

	// Mark the last user-turn block with cache_control to checkpoint history.
	if last && len(blocks) > 0 && (m.Role == RoleUser || m.Role == RoleTool) {
		setCacheControlOnLast(blocks)
	}

	switch m.Role {
	case RoleUser, RoleTool:
		return anthropic.NewUserMessage(blocks...), nil
	case RoleAssistant:
		return anthropic.NewAssistantMessage(blocks...), nil
	default:
		return anthropic.MessageParam{}, fmt.Errorf("unsupported role: %s", m.Role)
	}
}

func setCacheControlOnLast(blocks []anthropic.ContentBlockParamUnion) {
	if len(blocks) == 0 {
		return
	}
	b := &blocks[len(blocks)-1]
	cc := anthropic.CacheControlEphemeralParam{}
	switch {
	case b.OfText != nil:
		b.OfText.CacheControl = cc
	case b.OfToolResult != nil:
		b.OfToolResult.CacheControl = cc
	case b.OfToolUse != nil:
		b.OfToolUse.CacheControl = cc
	}
}

func rawSchemaToToolInputSchema(raw json.RawMessage) anthropic.ToolInputSchemaParam {
	if len(raw) == 0 {
		return anthropic.ToolInputSchemaParam{Properties: map[string]any{}}
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		return anthropic.ToolInputSchemaParam{Properties: map[string]any{}}
	}
	out := anthropic.ToolInputSchemaParam{}
	if props, ok := s["properties"]; ok {
		out.Properties = props
	}
	if reqs, ok := s["required"].([]any); ok {
		for _, r := range reqs {
			if rs, ok := r.(string); ok {
				out.Required = append(out.Required, rs)
			}
		}
	}
	return out
}
