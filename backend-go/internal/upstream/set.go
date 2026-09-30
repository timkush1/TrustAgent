package upstream

import (
	"github.com/truthtable/backend-go/internal/llmapi"
)

// Set is the gateway's upstreams (SPEC §2.12 forwarding): OpenAI is the default upstream,
// Anthropic serves /v1/messages, and Fallback, when configured, answers a route's fallback call
// in FallbackFormat (openai or anthropic). Anthropic and Fallback may be nil.
type Set struct {
	OpenAI, Anthropic, Fallback *Client
	FallbackFormat              string
}

// For returns the upstream of an API family (llmapi.Family): the Anthropic upstream for
// anthropic when one is configured, else the OpenAI upstream.
func (s *Set) For(family string) *Client {
	if family == llmapi.FamilyAnthropic && s.Anthropic != nil {
		return s.Anthropic
	}
	return s.OpenAI
}
