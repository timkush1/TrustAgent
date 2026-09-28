package llmapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenAIErrorType is the error type of every error TrustAgent generates on an OpenAI-format route.
const OpenAIErrorType = "trustagent_error"

// openAIErrorBody is {"error":{"message","type","param":null,"code"}} in the spec's member order.
type openAIErrorBody struct {
	Error openAIErrorDetail `json:"error"`
}

type openAIErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

// OpenAIError is the body of an error TrustAgent generates on an OpenAI-format /v1 route
// (SPEC §2.12): {"error":{"message","type":"trustagent_error","param":null,"code"}}.
func OpenAIError(code, message string) []byte {
	return marshal(openAIErrorBody{Error: openAIErrorDetail{Message: message, Type: OpenAIErrorType, Code: code}})
}

// Anthropic error types TrustAgent uses on /v1/messages (SPEC §2.12).
const (
	AnthropicAuthenticationError = "authentication_error"
	AnthropicInvalidRequestError = "invalid_request_error"
	AnthropicRateLimitError      = "rate_limit_error"
	AnthropicAPIError            = "api_error"
	AnthropicOverloadedError     = "overloaded_error"
)

// anthropicErrorBody is {"type":"error","error":{"type","message"}}.
type anthropicErrorBody struct {
	Type  string               `json:"type"`
	Error anthropicErrorDetail `json:"error"`
}

type anthropicErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// AnthropicError is the body of an error in the Anthropic shape:
// {"type":"error","error":{"type":<errType>,"message":<message>}}.
func AnthropicError(errType, message string) []byte {
	return marshal(anthropicErrorBody{Type: "error", Error: anthropicErrorDetail{Type: errType, Message: message}})
}

// AnthropicErrorType maps an HTTP status to the Anthropic error type of a TrustAgent error: 401
// authentication_error, 429 rate_limit_error, 503 and 529 overloaded_error, other 5xx api_error,
// anything else invalid_request_error.
func AnthropicErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return AnthropicAuthenticationError
	case status == http.StatusTooManyRequests:
		return AnthropicRateLimitError
	case status == http.StatusServiceUnavailable || status == 529:
		return AnthropicOverloadedError
	case status >= 500:
		return AnthropicAPIError
	}
	return AnthropicInvalidRequestError
}

// ErrorBody is a TrustAgent error on a /v1 route in the shape of the route's API (SPEC §2.1,
// §2.12): the OpenAI shape with code for the OpenAI formats, the Anthropic shape (whose type
// follows status; it has no code member) for anthropic_messages.
func ErrorBody(format string, status int, code, message string) []byte {
	if IsAnthropic(format) {
		return AnthropicError(AnthropicErrorType(status), message)
	}
	return OpenAIError(code, message)
}

// SyntheticIDPrefix prefixes the id of a chat completion the gateway writes itself: the id is
// "chatcmpl-trustagent-" plus the trace id without hyphens (<trace32>).
const SyntheticIDPrefix = "chatcmpl-trustagent-"

// Id prefixes of the other formats' synthetic objects: "cmpl-trustagent-<trace32>" for
// completions and "msg_trustagent_<trace32>" for Anthropic messages (SPEC §2.12).
const (
	SyntheticCompletionIDPrefix = "cmpl-trustagent-"
	SyntheticMessageIDPrefix    = "msg_trustagent_"
)

// SyntheticID is the chat completion id of a response the gateway builds for traceID.
func SyntheticID(traceID string) string {
	return SyntheticIDPrefix + trace32(traceID)
}

// SyntheticIDFor is the id of an object of format the gateway builds for traceID.
func SyntheticIDFor(format, traceID string) string {
	switch format {
	case FormatOpenAICompletions:
		return SyntheticCompletionIDPrefix + trace32(traceID)
	case FormatAnthropicMessages:
		return SyntheticMessageIDPrefix + trace32(traceID)
	}
	return SyntheticID(traceID)
}

func trace32(traceID string) string { return strings.ReplaceAll(traceID, "-", "") }

// SyntheticChatCompletion is the upstream answer test mode serves in place of an upstream call:
// a non-streaming chat completion with one assistant choice holding text.
func SyntheticChatCompletion(traceID, model string, created int64, text string) []byte {
	return marshal(map[string]any{
		"id":      SyntheticID(traceID),
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	})
}

// marshal encodes v without HTML escaping (SPEC §2.1) and without the encoder's trailing newline.
// The values are built here from strings, numbers and raw JSON the package validated, so encoding
// cannot fail.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
