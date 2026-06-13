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
}
