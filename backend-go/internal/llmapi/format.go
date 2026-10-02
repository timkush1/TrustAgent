package llmapi

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Gated API paths (SPEC §2.4 row 71).
const (
	PathChatCompletions = "/v1/chat/completions"
	PathCompletions     = "/v1/completions"
	PathMessages        = "/v1/messages"
	// PathResponses is the OpenAI Responses API: proxied, never gated
	// (X-TrustAgent-Status: skipped:unsupported_api, SPEC §2.4 row 73).
	PathResponses = "/v1/responses"
	// PathModels lists models; both API families serve it, so the client's headers pick the
	// upstream (SPEC §2.4 row 72, P4-PLAN D67).
	PathModels = "/v1/models"
)

// Upstream format families: the values of TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT.
const (
	FamilyOpenAI    = "openai"
	FamilyAnthropic = "anthropic"
)

// FormatForPath returns the API format gated on path (a URL path without its query), or "" when
// the path is not gated: /v1/chat/completions is openai_chat, /v1/completions openai_completions
// and /v1/messages anthropic_messages. A trailing slash is tolerated; sub-paths such as
// /v1/messages/count_tokens and the Responses API are not gated.
func FormatForPath(path string) string {
	switch strings.TrimSuffix(path, "/") {
	case PathChatCompletions:
		return FormatOpenAIChat
	case PathCompletions:
		return FormatOpenAICompletions
	case PathMessages:
		return FormatAnthropicMessages
	}
	return ""
}

// IsResponsesAPI reports whether path belongs to the OpenAI Responses API (/v1/responses and its
// sub-paths), which the gateway passes through ungated.
func IsResponsesAPI(path string) bool {
	return path == PathResponses || strings.HasPrefix(path, PathResponses+"/")
}

// FamilyForPath returns the API family a /v1 path belongs to: anthropic for /v1/messages and its
// sub-paths (such as /v1/messages/count_tokens), openai for every other path. It picks the upstream
// of a path that is passed through and the error shape of every path.
func FamilyForPath(path string) string {
	p := strings.TrimSuffix(path, "/")
	if p == PathMessages || strings.HasPrefix(p, PathMessages+"/") {
		return FamilyAnthropic
	}
	return FamilyOpenAI
}

// Family returns the upstream family of an API format ("openai" or "anthropic"), or "" for an
// unknown format.
func Family(format string) string {
	switch format {
	case FormatOpenAIChat, FormatOpenAICompletions:
		return FamilyOpenAI
	case FormatAnthropicMessages:
		return FamilyAnthropic
	}
	return ""
}

// FallbackFormatCompatible reports whether a fallback upstream of the given family can answer a
// request of format. A mismatch turns the fallback into a block with fallback_format_mismatch
// (SPEC §2.12). An empty family means "openai", the TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT default.
func FallbackFormatCompatible(format, family string) bool {
	if family == "" {
		family = FamilyOpenAI
	}
	f := Family(format)
	return f != "" && f == family
}

// IsAnthropic reports whether format is anthropic_messages.
func IsAnthropic(format string) bool { return format == FormatAnthropicMessages }

// unmarshalObject decodes b into v when b is a JSON object.
func unmarshalObject(b []byte, v any) bool {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return false
	}
	return json.Unmarshal(b, v) == nil
}
