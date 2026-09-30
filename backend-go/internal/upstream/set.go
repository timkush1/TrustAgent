package upstream

import (
	"context"
	"io"
	"net/http"

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

// StreamBody is the body of a streamed (text/event-stream) response for the relay. cancel
// releases the request's context once the relay is done. For now it is resp.Body as it is; the
// idle timeout between reads (TRUTHTABLE_STREAM_IDLE_TIMEOUT) and the defensive gunzip land here
// in P3 (GS-2).
func (c *Client) StreamBody(resp *http.Response, cancel context.CancelFunc) io.ReadCloser {
	return resp.Body
}

// DecodeBody is a response body with a stray Content-Encoding decoded. For now it is resp.Body
// as it is: ReadBody still decodes gzip itself until the decoding moves here (GS-2).
func DecodeBody(resp *http.Response) io.ReadCloser {
	return resp.Body
}
