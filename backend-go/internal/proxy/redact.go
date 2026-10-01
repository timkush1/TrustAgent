package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/truthtable/backend-go/internal/upstream"
)

// Upstream error bodies in inject mode (P4-PLAN D42). An upstream that refuses the gateway's own
// key often quotes it back ("Incorrect API key provided: sk-…"); the client never had that key, so
// the gateway masks every secret it knows (Options.Redact) in a non-2xx body before relaying it,
// on the gated routes and on the passthrough ones alike. Only the first maxRedactedErrorBytes are
// relayed: the gateway never relays error bytes it did not mask. In passthrough mode the
// credential is the client's own, and its error bodies pass through as they came.

// maxRedactedErrorBytes is how much of an upstream error body inject mode relays (64 KiB).
const maxRedactedErrorBytes = 64 << 10

// redactErrorBody is an upstream error body as inject mode relays it: at most
// maxRedactedErrorBytes, with every known secret masked.
func (h *Handler) redactErrorBody(body []byte) []byte {
	if len(body) > maxRedactedErrorBytes {
		body = body[:maxRedactedErrorBytes]
	}
	return []byte(h.redact(string(body)))
}

// redactingProxy masks the error bodies of a passthrough reverse proxy whose upstream injects its
// own key: its non-2xx responses are read (decoded, at most maxRedactedErrorBytes), masked and
// sent with their new length. A body that cannot be read fails the call like a failed upstream
// (passthroughFailed, a 502).
func (h *Handler) redactingProxy(c *upstream.Client) http.Handler {
	rp := c.ReverseProxy(h.passthroughFailed)
	if !c.Injecting() {
		return rp
	}
	modify := rp.ModifyResponse
	rp.ModifyResponse = func(resp *http.Response) error {
		if modify != nil {
			if err := modify(resp); err != nil {
				return err
			}
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return nil
		}
		body, _, err := upstream.ReadBody(resp, maxRedactedErrorBytes)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		masked := h.redactErrorBody(body)
		resp.Body = io.NopCloser(bytes.NewReader(masked))
		resp.ContentLength = int64(len(masked))
		resp.Header.Set("Content-Length", strconv.Itoa(len(masked)))
		resp.Header.Del("Content-Encoding")
		return nil
	}
	return rp
}
