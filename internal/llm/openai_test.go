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
		})
	}
}
