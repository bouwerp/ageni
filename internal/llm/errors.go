package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go"
)

type ErrorClass string

const (
	ErrorClassUnknown          ErrorClass = "unknown"
	ErrorClassCancelled        ErrorClass = "cancelled"
	ErrorClassDeadlineExceeded ErrorClass = "deadline-exceeded"
	ErrorClassAuth             ErrorClass = "auth"
	ErrorClassPermission       ErrorClass = "permission"
	ErrorClassRateLimit        ErrorClass = "rate-limit"
	ErrorClassPayment          ErrorClass = "payment"
	ErrorClassContextLimit     ErrorClass = "context-limit"
	ErrorClassModelUnsupported ErrorClass = "model-unsupported"
	ErrorClassInvalidRequest   ErrorClass = "invalid-request"
	ErrorClassNotFound         ErrorClass = "not-found"
	ErrorClassServer           ErrorClass = "server"
	ErrorClassNetwork          ErrorClass = "network"
)

// ProviderError adds stable provider/model/class metadata to an underlying LLM
// error so retry and fallback policy can avoid depending on fragile strings.
type ProviderError struct {
	Provider  string
	Model     string
	Operation string
	Class     ErrorClass
	Err       error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "<nil>"
	}
	label := FormatLabel(e.Provider, e.Model)
	if label == "" {
		label = "provider"
	}
	if e.Operation != "" {
		label += " " + e.Operation
	}
	if class := e.errorClass(); class != ErrorClassUnknown {
		return fmt.Sprintf("%s [%s]: %v", label, class, e.Err)
	}
	return fmt.Sprintf("%s: %v", label, e.Err)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProviderError) errorClass() ErrorClass {
	if e == nil {
		return ErrorClassUnknown
	}
	if e.Class != "" && e.Class != ErrorClassUnknown {
		return e.Class
	}
	return classifyUnderlyingError(e.Err)
}

func WrapProviderError(provider, model, operation string, err error) error {
	if err == nil {
		return nil
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		if pe.Provider == "" {
			pe.Provider = provider
		}
		if pe.Model == "" {
			pe.Model = model
		}
		if pe.Operation == "" {
			pe.Operation = operation
		}
		if pe.Class == "" || pe.Class == ErrorClassUnknown {
			pe.Class = classifyUnderlyingError(pe.Err)
		}
		return pe
	}
	return &ProviderError{
		Provider:  provider,
		Model:     model,
		Operation: operation,
		Class:     classifyUnderlyingError(err),
		Err:       err,
	}
}

func ProviderLabel(err error) string {
	var pe *ProviderError
	if !errors.As(err, &pe) {
		return ""
	}
	return FormatLabel(pe.Provider, pe.Model)
}

func ErrorSummary(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	if i := strings.Index(msg, "\n"); i > 0 {
		msg = msg[:i]
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		label := FormatLabel(pe.Provider, pe.Model)
		class := pe.errorClass()
		switch {
		case label != "" && class != ErrorClassUnknown:
			msg = fmt.Sprintf("%s [%s]: %s", label, class, trimProviderPrefix(msg, label))
		case label != "":
			msg = fmt.Sprintf("%s: %s", label, trimProviderPrefix(msg, label))
		case class != ErrorClassUnknown:
			msg = fmt.Sprintf("[%s] %s", class, msg)
		}
	}
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
	return msg
}

func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorClassUnknown
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.errorClass()
	}
	return classifyUnderlyingError(err)
}

func classifyUnderlyingError(err error) ErrorClass {
	if err == nil {
		return ErrorClassUnknown
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrorClassCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorClassDeadlineExceeded
	}

	var oerr *openai.Error
	if errors.As(err, &oerr) {
		return classifyHTTPError(oerr.StatusCode, strings.ToLower(oerr.Message), oerr.Code)
	}
	var aerr *anthropic.Error
	if errors.As(err, &aerr) {
		msg := strings.ToLower(aerr.RawJSON())
		switch strings.ToLower(string(aerr.Type())) {
		case "authentication_error":
			return ErrorClassAuth
		case "permission_error":
			return ErrorClassPermission
		case "rate_limit_error":
			return ErrorClassRateLimit
		case "not_found_error":
			if looksLikeModelUnsupported(msg) {
				return ErrorClassModelUnsupported
			}
			return ErrorClassNotFound
		case "invalid_request_error":
			if looksLikeContextLimit(msg) {
				return ErrorClassContextLimit
			}
			if looksLikeModelUnsupported(msg) {
				return ErrorClassModelUnsupported
			}
			return ErrorClassInvalidRequest
		case "overloaded_error", "api_error", "gateway_timeout_error":
			return ErrorClassServer
		}
		return classifyHTTPError(aerr.StatusCode, msg, "")
	}

	msg := strings.ToLower(err.Error())
	if looksLikeContextLimit(msg) {
		return ErrorClassContextLimit
	}
	if looksLikeModelUnsupported(msg) {
		return ErrorClassModelUnsupported
	}
	switch {
	case containsAny(msg, "401", "unauthorized", "authentication failed", "invalid api key"):
		return ErrorClassAuth
	case containsAny(msg, "403", "forbidden", "permission denied"):
		return ErrorClassPermission
	case containsAny(msg, "429", "rate limit", "rate-limit"):
		return ErrorClassRateLimit
	case containsAny(msg, "402", "payment required", "insufficient credits", "can only afford"):
		return ErrorClassPayment
	case containsAny(msg, "404", "not found"):
		return ErrorClassNotFound
	case containsAny(msg, "400", "bad request", "bad_request"):
		return ErrorClassInvalidRequest
	case containsAny(msg, "500", "502", "503", "504", "overloaded", "service unavailable", "temporarily unavailable"):
		return ErrorClassServer
	case containsAny(msg, "connection refused", "connection reset", "broken pipe", "eof", "unexpected eof", "timeout", "timed out", "server closed idle connection", "transport connection broken"):
		return ErrorClassNetwork
	}
	return ErrorClassUnknown
}

func classifyHTTPError(statusCode int, msg, code string) ErrorClass {
	if looksLikeContextLimit(msg) {
		return ErrorClassContextLimit
	}
	if looksLikeModelUnsupported(msg) {
		return ErrorClassModelUnsupported
	}
	switch statusCode {
	case 400:
		return ErrorClassInvalidRequest
	case 401:
		return ErrorClassAuth
	case 402:
		return ErrorClassPayment
	case 403:
		return ErrorClassPermission
	case 404:
		if looksLikeModelUnsupported(msg) || strings.Contains(strings.ToLower(code), "model") {
			return ErrorClassModelUnsupported
		}
		return ErrorClassNotFound
	case 408:
		return ErrorClassNetwork
	case 409, 425, 429:
		return ErrorClassRateLimit
	case 413:
		return ErrorClassContextLimit
	}
	if statusCode >= 500 {
		return ErrorClassServer
	}
	if containsAny(msg, "connection refused", "connection reset", "broken pipe", "eof", "unexpected eof", "timeout", "timed out") {
		return ErrorClassNetwork
	}
	return ErrorClassUnknown
}

func IsRetryableError(err error) bool {
	switch ClassifyError(err) {
	case ErrorClassDeadlineExceeded,
		ErrorClassRateLimit,
		ErrorClassPayment,
		ErrorClassContextLimit,
		ErrorClassModelUnsupported,
		ErrorClassInvalidRequest,
		ErrorClassNotFound,
		ErrorClassServer,
		ErrorClassNetwork,
		ErrorClassAuth:
		return true
	default:
		return false
	}
}

func IsModelUnsupportedError(err error) bool {
	return ClassifyError(err) == ErrorClassModelUnsupported
}

func IsContextLimitError(err error) bool {
	return ClassifyError(err) == ErrorClassContextLimit
}

func ErrorClassTag(err error) string {
	class := ClassifyError(err)
	if class == ErrorClassUnknown {
		return "other"
	}
	return string(class)
}

func looksLikeContextLimit(msg string) bool {
	return containsAny(msg,
		"context_length_exceeded", "context length exceeded",
		"maximum context length", "prompt is too long",
		"too many tokens", "reduce the length",
		"context window", "tokens exceeds", "request too large", "request entity too large")
}

func looksLikeModelUnsupported(msg string) bool {
	return containsAny(msg,
		"model not supported", "model_not_supported",
		"model not found", "modelnotfound",
		"modelerror", "unsupported model",
		"not supported", "unknown model", "does not exist")
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func trimProviderPrefix(msg, label string) string {
	msg = strings.TrimSpace(msg)
	if label == "" {
		return msg
	}
	prefix := label + ": "
	if strings.HasPrefix(msg, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(msg, prefix))
	}
	return msg
}
