package llm

import (
	"context"
	"errors"
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

func ClassifyError(err error) ErrorClass {
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
