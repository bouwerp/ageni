package llm

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// OpenAIAdapter implements Adapter against the OpenAI Chat Completions API
// (covers OpenAI itself and any OpenAI-compatible endpoint via base URL).
//
// Note: OpenAI does prompt caching automatically (no cache_control to set);
// it's reported back as prompt_tokens_details.cached_tokens. We sort tools
// alphabetically and keep system prompt position stable to maximize hits.
type OpenAIAdapter struct {
	client openai.Client
}

func NewOpenAIAdapter(apiKey, baseURL string) *OpenAIAdapter {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(4),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &OpenAIAdapter{client: openai.NewClient(opts...)}
}

func (o *OpenAIAdapter) Provider() string { return "openai" }

func (o *OpenAIAdapter) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	params := o.buildParams(req)
	stream := o.client.Chat.Completions.NewStreaming(ctx, params)
	out := make(chan StreamEvent, 16)

	go func() {
		defer close(out)
		// Tool-call deltas come per-index. Accumulate until finish_reason fires.
		type pendingTool struct {
			id   string
			name string
			args string
		}
		pending := make(map[int64]*pendingTool)
		var usage Usage

		emitTools := func() {
			for _, t := range pending {
				out <- StreamEvent{
					Type: StreamEventToolCall,
					ToolCall: &ToolCall{
						ID:        t.id,
						Name:      t.name,
						Arguments: sanitizeArgs([]byte(t.args)),
					},
				}
			}
			pending = make(map[int64]*pendingTool)
		}

		for stream.Next() {
			chunk := stream.Current()
			if chunk.Usage.TotalTokens > 0 {
				usage.InputTokens = int(chunk.Usage.PromptTokens)
				usage.OutputTokens = int(chunk.Usage.CompletionTokens)
				usage.CacheReadTokens = int(chunk.Usage.PromptTokensDetails.CachedTokens)
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					out <- StreamEvent{Type: StreamEventText, TextDelta: choice.Delta.Content}
				}
				for _, tcd := range choice.Delta.ToolCalls {
					t, ok := pending[tcd.Index]
					if !ok {
						t = &pendingTool{}
						pending[tcd.Index] = t
					}
					if tcd.ID != "" {
						t.id = tcd.ID
					}
					if tcd.Function.Name != "" {
						t.name = tcd.Function.Name
					}
					if tcd.Function.Arguments != "" {
						t.args += tcd.Function.Arguments
					}
				}
				if choice.FinishReason == "tool_calls" {
					emitTools()
				}
			}
		}
		if err := stream.Err(); err != nil {
			out <- StreamEvent{Type: StreamEventError, Err: err}
			return
		}
		// Drain any remaining tool calls (in case finish_reason wasn't on a chunk we saw).
		emitTools()
		u := usage
		out <- StreamEvent{Type: StreamEventDone, Usage: &u}
	}()

	return out, nil
}

func (o *OpenAIAdapter) buildParams(req Request) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model: req.Model,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(req.MaxTokens))
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}

	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		msgs = append(msgs, messageToOpenAI(m))
	}
	params.Messages = msgs

	if len(req.Tools) > 0 {
		params.Tools = make([]openai.ChatCompletionToolParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			var sch shared.FunctionParameters
			if len(t.Schema) > 0 {
				_ = json.Unmarshal(t.Schema, &sch)
			}
			params.Tools = append(params.Tools, openai.ChatCompletionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        t.Name,
					Description: openai.String(t.Description),
					Parameters:  sch,
				},
			})
		}
	}

	return params
}

func messageToOpenAI(m Message) openai.ChatCompletionMessageParamUnion {
	switch m.Role {
	case RoleSystem:
		return openai.SystemMessage(SanitizeText(m.Text))
	case RoleUser:
		return openai.UserMessage(SanitizeText(m.Text))
	case RoleAssistant:
		ap := openai.ChatCompletionAssistantMessageParam{}
		if m.Text != "" {
			ap.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(SanitizeText(m.Text)),
			}
		}
		for _, tc := range m.ToolCalls {
			args := string(sanitizeArgs(tc.Arguments))
			if args == "" {
				args = "{}"
			}
			ap.ToolCalls = append(ap.ToolCalls, openai.ChatCompletionMessageToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		return openai.ChatCompletionMessageParamUnion{OfAssistant: &ap}
	case RoleTool:
		// One tool-result message per result.
		// If multiple results in a single Message, the caller should split them
		// upstream — for now emit only the first.
		if len(m.ToolResults) > 0 {
			tr := m.ToolResults[0]
			return openai.ToolMessage(SanitizeText(tr.Content), tr.ToolCallID)
		}
		return openai.UserMessage("")
	}
	return openai.UserMessage(SanitizeText(m.Text))
}
