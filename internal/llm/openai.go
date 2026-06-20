package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	client   openai.Client
	provider string
	baseURL  string
}

func NewOpenAIAdapter(apiKey, baseURL string) *OpenAIAdapter {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(4),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
		if strings.Contains(baseURL, "api.z.ai") {
			opts = append(opts, option.WithHeader("User-Agent", "Cursor/0.45.11"))
		}
	}
	return &OpenAIAdapter{
		client:  openai.NewClient(opts...),
		baseURL: baseURL,
	}
}

func (o *OpenAIAdapter) SetProvider(p string) { o.provider = p }

func (o *OpenAIAdapter) Provider() string {
	if o.provider != "" {
		return o.provider
	}
	return "openai"
}

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
		var reasoningContent strings.Builder

		isLlama := o.provider == "llamacpp" || o.provider == "llamacpp-fleet"
		var dslParser *llamaDSLParser
		var dslToolCounter int64
		if isLlama {
			dslParser = &llamaDSLParser{}
		}

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
					if isLlama {
						dslParser.Feed(choice.Delta.Content, func(text string) {
							out <- StreamEvent{Type: StreamEventText, TextDelta: text}
						}, func(name, args string) {
							out <- StreamEvent{
								Type: StreamEventToolCall,
								ToolCall: &ToolCall{
									ID:        fmt.Sprintf("call_%d", dslToolCounter),
									Name:      name,
									Arguments: sanitizeArgs([]byte(args)),
								},
							}
							dslToolCounter++
						})
					} else {
						out <- StreamEvent{Type: StreamEventText, TextDelta: choice.Delta.Content}
					}
				}
				// Capture DeepSeek reasoning_content streamed as an extra field.
				// Extra/unknown fields are marked invalid by the apijson decoder even
				// when the value is perfectly valid JSON, so check Raw() not Valid().
				if rf, ok := choice.Delta.JSON.ExtraFields["reasoning_content"]; ok && rf.Raw() != "" && rf.Raw() != "null" {
					var rc string
					if err := json.Unmarshal([]byte(rf.Raw()), &rc); err == nil && rc != "" {
						reasoningContent.WriteString(rc)
						out <- StreamEvent{Type: StreamEventThinking, TextDelta: rc}
					}
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
			out <- StreamEvent{Type: StreamEventError, Err: WrapProviderError(o.Provider(), req.Model, "stream", err)}
			return
		}
		// Drain any remaining tool calls (in case finish_reason wasn't on a chunk we saw).
		emitTools()
		if isLlama {
			dslParser.Flush(func(text string) {
				out <- StreamEvent{Type: StreamEventText, TextDelta: text}
			}, func(name, args string) {
				out <- StreamEvent{
					Type: StreamEventToolCall,
					ToolCall: &ToolCall{
						ID:        fmt.Sprintf("call_%d", dslToolCounter),
						Name:      name,
						Arguments: sanitizeArgs([]byte(args)),
					},
				}
				dslToolCounter++
			})
		}
		u := usage
		done := StreamEvent{Type: StreamEventDone, Usage: &u}
		if rc := reasoningContent.String(); rc != "" {
			done.ReasoningContent = rc
		}
		out <- done
	}()

	return out, nil
}

func (o *OpenAIAdapter) buildParams(req Request) openai.ChatCompletionNewParams {
	isCerebras := strings.Contains(o.baseURL, "cerebras.ai")
	isLlama := o.provider == "llamacpp" || o.provider == "llamacpp-fleet"

	params := openai.ChatCompletionNewParams{
		Model: req.Model,
	}
	if isLlama {
		extra := map[string]any{
			"chat_template_kwargs": map[string]any{
				"enable_thinking": false,
			},
		}
		if len(req.Tools) > 0 {
			extra["grammar"] = buildGBNFGrammar(req.Tools)
		}
		params.SetExtraFields(extra)
	}
	if req.JSONMode {
		fmtParam := shared.NewResponseFormatJSONObjectParam()
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &fmtParam,
		}
	}
	isOpenRouter := o.provider == "openrouter"
	if !isCerebras && !isLlama && !isOpenRouter {
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	if isCerebras || isLlama {
		// Cerebras and llamacpp still expect max_tokens.
		params.MaxTokens = openai.Int(int64(maxTokens))
	} else {
		params.MaxCompletionTokens = openai.Int(int64(maxTokens))
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	} else if isLlama {
		params.Temperature = openai.Float(0.0)
	}

	messagesToProcess := req.Messages
	if isLlama {
		messagesToProcess = make([]Message, 0, len(req.Messages))
		for _, m := range req.Messages {
			if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
				var sb strings.Builder
				sb.WriteString(m.Text)
				for _, tc := range m.ToolCalls {
					if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
						sb.WriteByte('\n')
					}
					sb.WriteString(fmt.Sprintf("@call:%s%s\n", tc.Name, string(tc.Arguments)))
				}
				messagesToProcess = append(messagesToProcess, Message{
					Role:             RoleAssistant,
					Text:             sb.String(),
					ReasoningContent: m.ReasoningContent,
				})
			} else if m.Role == RoleTool {
				var sb strings.Builder
				if len(m.ToolResults) > 0 {
					for _, tr := range m.ToolResults {
						if sb.Len() > 0 {
							sb.WriteString("\n")
						}
						sb.WriteString(tr.Content)
					}
				} else {
					sb.WriteString(m.Text)
				}
				messagesToProcess = append(messagesToProcess, Message{
					Role: RoleUser,
					Text: sb.String(),
				})
			} else {
				messagesToProcess = append(messagesToProcess, m)
			}
		}
	}

	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(messagesToProcess)+1)
	if req.System != "" {
		msgs = append(msgs, openai.SystemMessage(SanitizeText(req.System)))
	}
	for _, m := range messagesToProcess {
		if m.Role == RoleTool && len(m.ToolResults) > 1 {
			// Each tool result must be its own tool message so that every
			// tool_call_id from the preceding assistant message is satisfied.
			for _, tr := range m.ToolResults {
				msgs = append(msgs, openai.ToolMessage(SanitizeText(tr.Content), tr.ToolCallID))
			}
		} else {
			msgs = append(msgs, messageToOpenAI(m))
		}
	}

	// DeepSeek (and strict OpenAI-compat providers) require every assistant
	// tool_calls message to be immediately followed by tool messages that
	// respond to EACH tool_call_id. Two failure modes:
	//   (a) trailing: assistant tool_calls at end with no tool responses
	//       (e.g. streaming error happened before tools executed)
	//   (b) mid-history gap: streaming error on one turn, user sends next turn,
	//       so the bad assistant+partial tools sit in the middle of history
	// Scan the full list; when a gap is found, remove the bad assistant message
	// and any partial tool responses, splicing in whatever comes after.
	for i := 0; i < len(msgs); i++ {
		assist := msgs[i].OfAssistant
		if assist == nil || len(assist.ToolCalls) == 0 {
			continue
		}
		needed := make(map[string]struct{}, len(assist.ToolCalls))
		for _, tc := range assist.ToolCalls {
			needed[tc.ID] = struct{}{}
		}
		j := i + 1
		for j < len(msgs) && msgs[j].OfTool != nil {
			delete(needed, msgs[j].OfTool.ToolCallID)
			j++
		}
		if len(needed) > 0 {
			// Remove the incomplete assistant + partial tool responses; keep rest.
			msgs = append(msgs[:i], msgs[j:]...)
			i-- // recheck position i (now holds what was at j)
		} else {
			i = j - 1 // skip past the valid tool messages
		}
	}

	params.Messages = msgs

	if len(req.Tools) > 0 && !isLlama {
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

// buildGBNFGrammar formats the allowed tools into a schema grammar for llama.cpp.
func buildGBNFGrammar(tools []ToolDef) string {
	if len(tools) == 0 {
		return ""
	}
	var names []string
	for _, t := range tools {
		var sb strings.Builder
		for _, ch := range t.Name {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
				sb.WriteRune(ch)
			}
		}
		cleanName := sb.String()
		if cleanName != "" {
			names = append(names, fmt.Sprintf("%q", cleanName))
		}
	}
	if len(names) == 0 {
		return ""
	}
	toolNameRule := strings.Join(names, " | ")
	return fmt.Sprintf(`root      ::= ( text | tool_call )*
tool_call ::= "@call:" tool_name object "\n"
tool_name ::= %s
object    ::= "{" space ( string ":" value ( "," space string ":" value )* )? "}"
value     ::= string | number | "true" | "false" | "null" | object | array
string    ::= "\"" ( [^\"\\\n\r] | "\\" [\"\\/bfnrt] )* "\""
number    ::= "-"? ( "0" | [1-9] [0-9]* ) ( "." [0-9]+ )? ( [eE] [+-]? [0-9]+ )?
array     ::= "[" space ( value ( "," space value )* )? "]"
space     ::= [ \t\n\r]*
text      ::= [^@]+ | "@" [^c] | "@c" [^a] | "@ca" [^l] | "@cal" [^l] | "@call" [^:]
`, toolNameRule)
}

// llamaDSLParser parses streamed character blocks to extract custom Compact DSL format.
type llamaDSLParser struct {
	buffer      strings.Builder
	inCall      bool
	callBuffer  strings.Builder
	currentTool string
}

func (p *llamaDSLParser) Feed(text string, emitText func(string), emitTool func(name, args string)) {
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if !p.inCall {
			if ch == '@' {
				// Flush everything before this '@'
				if p.buffer.Len() > 0 {
					emitText(p.buffer.String())
					p.buffer.Reset()
				}
				p.buffer.WriteByte(ch)
			} else {
				p.buffer.WriteByte(ch)
				bufStr := p.buffer.String()
				// If the buffer starts with '@' but is no longer a prefix of "@call:", flush it.
				if strings.HasPrefix(bufStr, "@") && !strings.HasPrefix("@call:", bufStr) {
					emitText(bufStr)
					p.buffer.Reset()
				}
			}

			// Check if we hit the full prefix
			if p.buffer.String() == "@call:" {
				p.buffer.Reset()
				p.inCall = true
				p.callBuffer.Reset()
				p.currentTool = ""
			}
		} else {
			if ch == '\n' {
				callStr := p.callBuffer.String()
				// If we don't have a brace yet, or if the call is complete (balanced braces and quotes),
				// terminate the call. Otherwise, write the newline to the buffer.
				if !strings.Contains(callStr, "{") || isCallComplete(callStr) {
					p.inCall = false
					p.callBuffer.Reset()
					p.buffer.Reset()
					braceIdx := strings.Index(callStr, "{")
					if braceIdx >= 0 {
						toolName := strings.TrimSpace(callStr[:braceIdx])
						argsJSON := callStr[braceIdx:]
						emitTool(toolName, argsJSON)
					}
				} else {
					p.callBuffer.WriteByte(ch)
				}
			} else {
				p.callBuffer.WriteByte(ch)
			}
		}
	}

	// At the end of Feed, if we are not in a call, and the buffer is NOT a prefix of "@call:",
	// we should flush it so the text is displayed immediately.
	if !p.inCall && p.buffer.Len() > 0 {
		bufStr := p.buffer.String()
		if !strings.HasPrefix("@call:", bufStr) {
			emitText(bufStr)
			p.buffer.Reset()
		}
	}
}

func (p *llamaDSLParser) Flush(emitText func(string), emitTool func(name, args string)) {
	if p.inCall {
		callStr := p.callBuffer.String()
		braceIdx := strings.Index(callStr, "{")
		if braceIdx >= 0 {
			toolName := strings.TrimSpace(callStr[:braceIdx])
			argsJSON := callStr[braceIdx:]
			emitTool(toolName, argsJSON)
		}
		p.inCall = false
		p.callBuffer.Reset()
		p.buffer.Reset()
	} else if p.buffer.Len() > 0 {
		emitText(p.buffer.String())
		p.buffer.Reset()
	}
}

func isCallComplete(s string) bool {
	braceIdx := strings.Index(s, "{")
	if braceIdx < 0 {
		return false
	}
	jsonPart := s[braceIdx:]
	inStr := false
	escaped := false
	braceCount := 0
	hasBrace := false
	for _, r := range jsonPart {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			if inStr {
				escaped = true
			}
			continue
		}
		if r == '"' {
			inStr = !inStr
			continue
		}
		if !inStr {
			if r == '{' {
				braceCount++
				hasBrace = true
			} else if r == '}' {
				braceCount--
			}
		}
	}
	return hasBrace && braceCount <= 0 && !inStr
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
		// Providers that require content or tool_calls (e.g. DeepSeek) reject
		// assistant messages that have neither. If we have no tool calls and no
		// text (e.g. a thinking-only turn), set content to "" so the field is
		// present in the serialized JSON.
		if len(ap.ToolCalls) == 0 && m.Text == "" {
			ap.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(""),
			}
		}
		// DeepSeek thinking mode requires reasoning_content to be echoed back.
		// Other OpenAI-compat providers silently ignore unknown extra fields.
		if m.ReasoningContent != "" {
			ap.SetExtraFields(map[string]any{"reasoning_content": m.ReasoningContent})
		}
		return openai.ChatCompletionMessageParamUnion{OfAssistant: &ap}
	case RoleTool:
		// Single result — the multi-result case is expanded in buildParams.
		if len(m.ToolResults) > 0 {
			tr := m.ToolResults[0]
			return openai.ToolMessage(SanitizeText(tr.Content), tr.ToolCallID)
		}
		return openai.UserMessage("")
	}
	return openai.UserMessage(SanitizeText(m.Text))
}
