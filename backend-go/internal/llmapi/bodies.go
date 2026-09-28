package llmapi

import (
	"bytes"
	"encoding/json"
	"strings"
)

// OpenAIErrorType is the error type of every error TrustAgent generates on an OpenAI-format route.
const OpenAIErrorType = "trustagent_error"

// OpenAIError is the body of an error TrustAgent generates on an OpenAI-format /v1 route
// (SPEC §2.12): {"error":{"message","type":"trustagent_error","param":null,"code"}}.
func OpenAIError(code, message string) []byte {
	return marshal(map[string]any{
		"error": map[string]any{"message": message, "type": OpenAIErrorType, "param": nil, "code": code},
	})
}

// SyntheticIDPrefix prefixes the id of a response the gateway writes itself: the id is
// "chatcmpl-trustagent-" plus the trace id without hyphens (<trace32>).
const SyntheticIDPrefix = "chatcmpl-trustagent-"

// SyntheticID is the chat completion id of a response the gateway builds for traceID.
func SyntheticID(traceID string) string {
	return SyntheticIDPrefix + strings.ReplaceAll(traceID, "-", "")
}

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
// The values are built here from strings and numbers, so encoding cannot fail.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
