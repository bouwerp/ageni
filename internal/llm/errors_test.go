package llm

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go"
)

func TestClassifyErrorOpenAI(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, URL: &url.URL{Scheme: "https", Host: "api.openai.com", Path: "/v1/responses"}}
	res := &http.Response{StatusCode: http.StatusTooManyRequests}
	err := &openai.Error{
		StatusCode: res.StatusCode,
		Request:    req,
		Response:   res,
		Message:    "rate limit exceeded",
		Code:       "rate_limit_exceeded",
	}
	if got := ClassifyError(err); got != ErrorClassRateLimit {
		t.Fatalf("ClassifyError(openai 429) = %q, want %q", got, ErrorClassRateLimit)
	}
}

func TestClassifyErrorAnthropicContextLimit(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, URL: &url.URL{Scheme: "https", Host: "api.anthropic.com", Path: "/v1/messages"}}
	res := &http.Response{StatusCode: http.StatusBadRequest}
	err := &anthropic.Error{
		StatusCode: res.StatusCode,
		Request:    req,
		Response:   res,
	}
	if unmarshalErr := err.UnmarshalJSON([]byte(`{"error":{"type":"invalid_request_error","message":"prompt is too long for this model context window"}}`)); unmarshalErr != nil {
		t.Fatalf("UnmarshalJSON: %v", unmarshalErr)
	}
	if got := ClassifyError(err); got != ErrorClassContextLimit {
		t.Fatalf("ClassifyError(anthropic context limit) = %q, want %q", got, ErrorClassContextLimit)
	}
}

func TestClassifyErrorModelUnsupported(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, URL: &url.URL{Scheme: "https", Host: "api.openai.com", Path: "/v1/responses"}}
	res := &http.Response{StatusCode: http.StatusNotFound}
	err := &openai.Error{
		StatusCode: res.StatusCode,
		Request:    req,
		Response:   res,
		Message:    "The model `gpt-legacy` does not exist",
		Code:       "model_not_found",
	}
	if got := ClassifyError(err); got != ErrorClassModelUnsupported {
		t.Fatalf("ClassifyError(openai missing model) = %q, want %q", got, ErrorClassModelUnsupported)
	}
}

func TestClassifyErrorContextCancellation(t *testing.T) {
	if got := ClassifyError(context.DeadlineExceeded); got != ErrorClassDeadlineExceeded {
		t.Fatalf("ClassifyError(context deadline) = %q, want %q", got, ErrorClassDeadlineExceeded)
	}
}
