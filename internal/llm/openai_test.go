package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIAdapterLlamaCPPExtraFields(t *testing.T) {
	for _, provider := range []string{"llamacpp", "llamacpp-fleet"} {
		t.Run(provider, func(t *testing.T) {
			a := NewOpenAIAdapter("dummy-key", "http://localhost:8080/v1")
			a.SetProvider(provider)

			req := Request{
				Model: "some-model",
				Tools: []ToolDef{
					{Name: "read_file", Description: "Read a file"},
				},
			}
			params := a.buildParams(req)

			data, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("failed to marshal params: %v", err)
			}

			jsonStr := strings.ReplaceAll(string(data), " ", "")
			expectedSub := `"chat_template_kwargs":{"enable_thinking":false}`
			if !strings.Contains(jsonStr, expectedSub) {
				t.Errorf("expected marshalled json to contain %q, got %q", expectedSub, jsonStr)
			}

			expectedGrammar := `"grammar":"root::=(text|tool_call)*`
			if !strings.Contains(strings.ReplaceAll(jsonStr, "\\n", ""), expectedGrammar) {
				t.Errorf("expected grammar rule to be in json, got %q", jsonStr)
			}
		})
	}
}

func TestLlamaDSLParser(t *testing.T) {
	var parsedCalls []struct {
		name string
		args string
	}
	var texts []string

	parser := &llamaDSLParser{}
	emitText := func(txt string) {
		texts = append(texts, txt)
	}
	emitTool := func(name, args string) {
		parsedCalls = append(parsedCalls, struct{ name, args string }{name, args})
	}

	// Stream 1: plain text
	parser.Feed("Hello world", emitText, emitTool)
	parser.Flush(emitText, emitTool)
	if len(texts) != 1 || texts[0] != "Hello world" {
		t.Errorf("expected 'Hello world', got %v", texts)
	}

	// Reset
	texts = nil
	parsedCalls = nil

	// Stream 2: text followed by a tool call on a newline
	parser.Feed("I will read the file now.\n@call:read_file{\"path\":\"main.go\"}\nDone.", emitText, emitTool)
	parser.Flush(emitText, emitTool)

	if len(parsedCalls) != 1 || parsedCalls[0].name != "read_file" || parsedCalls[0].args != "{\"path\":\"main.go\"}" {
		t.Errorf("failed to parse tool call: %v", parsedCalls)
	}
	combinedText := strings.Join(texts, "")
	if !strings.Contains(combinedText, "I will read the file now.") || !strings.Contains(combinedText, "Done.") {
		t.Errorf("missing text components: %v", combinedText)
	}

	// Reset
	texts = nil
	parsedCalls = nil

	// Stream 3: tool call split across chunks
	parser.Feed("Some text ", emitText, emitTool)
	parser.Feed("@ca", emitText, emitTool)
	parser.Feed("ll:glob", emitText, emitTool)
	parser.Feed("{\"pattern\":\"*.go\"}", emitText, emitTool)
	parser.Feed("\nRest of text", emitText, emitTool)
	parser.Flush(emitText, emitTool)

	if len(parsedCalls) != 1 || parsedCalls[0].name != "glob" || parsedCalls[0].args != "{\"pattern\":\"*.go\"}" {
		t.Errorf("failed to parse split tool call: %v", parsedCalls)
	}
	combinedText = strings.Join(texts, "")
	if !strings.Contains(combinedText, "Some text") || !strings.Contains(combinedText, "Rest of text") {
		t.Errorf("missing split text components: %v", combinedText)
	}

	// Reset
	texts = nil
	parsedCalls = nil

	// Stream 4: tool call with literal newlines inside JSON arguments
	parser = &llamaDSLParser{}
	parser.Feed("Some text before\n", emitText, emitTool)
	parser.Feed("@call:shell_exec{\"id\":\"shell_1\",\"command\":\"cat << 'EOF' > test.py\n", emitText, emitTool)
	parser.Feed("print('hello')\n", emitText, emitTool)
	parser.Feed("EOF\n", emitText, emitTool)
	parser.Feed("\"}\n", emitText, emitTool)
	parser.Feed("Some text after\n", emitText, emitTool)
	parser.Flush(emitText, emitTool)

	if len(parsedCalls) != 1 || parsedCalls[0].name != "shell_exec" {
		t.Errorf("failed to parse multi-line tool call: %v", parsedCalls)
	} else {
		expectedArgs := "{\"id\":\"shell_1\",\"command\":\"cat << 'EOF' > test.py\nprint('hello')\nEOF\n\"}"
		if parsedCalls[0].args != expectedArgs {
			t.Errorf("expected args %q, got %q", expectedArgs, parsedCalls[0].args)
		}
	}
	combinedText = strings.Join(texts, "")
	if !strings.Contains(combinedText, "Some text before") || !strings.Contains(combinedText, "Some text after") {
		t.Errorf("missing multi-line text components: %v", combinedText)
	}
}

func TestOpenAIAdapterLlamaCPPHistoryPreprocessing(t *testing.T) {
	for _, provider := range []string{"llamacpp", "llamacpp-fleet"} {
		t.Run(provider, func(t *testing.T) {
			a := NewOpenAIAdapter("dummy-key", "http://localhost:8080/v1")
			a.SetProvider(provider)

			req := Request{
				Model: "some-model",
				Messages: []Message{
					{
						Role: RoleAssistant,
						Text: "Running command...",
						ToolCalls: []ToolCall{
							{
								ID:        "call_1",
								Name:      "run_bash",
								Arguments: json.RawMessage(`{"CommandLine":"ls"}`),
							},
						},
					},
					{
						Role: RoleTool,
						ToolResults: []ToolResult{
							{
								ToolCallID: "call_1",
								Content:    "file1.txt\nfile2.txt",
							},
						},
					},
				},
			}
			params := a.buildParams(req)

			// The system prompt is added at index 0 if it exists, but here req.System is empty.
			// So params.Messages has index 0 as assistant, index 1 as tool result.
			if len(params.Messages) != 2 {
				t.Fatalf("expected 2 messages, got %d", len(params.Messages))
			}

			// Verify assistant message
			assistMsg := params.Messages[0].OfAssistant
			if assistMsg == nil {
				t.Fatalf("expected index 0 to be Assistant message")
			}
			if len(assistMsg.ToolCalls) != 0 {
				t.Errorf("expected no native tool calls in assistant message for llama, got %d", len(assistMsg.ToolCalls))
			}
			// Assist text should contain the DSL call
			foundText := assistMsg.Content.OfString.Or("")
			expectedText := "Running command...\n@call:run_bash{\"CommandLine\":\"ls\"}\n"
			if foundText != expectedText {
				t.Errorf("expected assistant text %q, got %q", expectedText, foundText)
			}

			// Verify tool message has become a user message
			userMsg := params.Messages[1].OfUser
			if userMsg == nil {
				t.Fatalf("expected index 1 to be User message (converted from Tool result)")
			}
			foundUserText := userMsg.Content.OfString.Or("")
			if foundUserText != "file1.txt\nfile2.txt" {
				t.Errorf("expected user text %q, got %q", "file1.txt\nfile2.txt", foundUserText)
			}
		})
	}
}
