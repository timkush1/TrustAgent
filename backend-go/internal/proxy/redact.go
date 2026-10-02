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

// redactLookahead is how far past maxRedactedErrorBytes an error body is read and masked before it
// is cut: longer than any secret, so a secret that starts before the cut is masked whole rather
// than cut first and left unmatched (its first characters relayed).
const redactLookahead = 4 << 10

// redactErrorBody is an upstream error body as inject mode relays it: every known secret masked,
// then at most maxRedactedErrorBytes.
func (h *Handler) redactErrorBody(body []byte) []byte {
	if len(body) > maxRedactedErrorBytes+redactLookahead {
		body = body[:maxRedactedErrorBytes+redactLookahead]
	}
	masked := h.redact(string(body))
	if len(masked) > maxRedactedErrorBytes {
		masked = masked[:maxRedactedErrorBytes]
	}
	return []byte(masked)
}

// redactHeaders masks every known secret in the header values of an upstream response, in place
// (inject mode, D42). An upstream that refuses the gateway's own key may quote it in a header
// (WWW-Authenticate, a debug echo) as well as in the body, and the client never had that key.
func (h *Handler) redactHeaders(hdr http.Header) {
	for name, values := range hdr {
		for i, v := range values {
			if masked := h.redact(v); masked != v {
				hdr[name][i] = masked
			}
		}
	}
}

// redactingProxy masks the header values of every response, and the error bodies, of a passthrough
// reverse proxy whose upstream injects its own key: its non-2xx responses are read (decoded, at most maxRedactedErrorBytes plus the
// lookahead), masked, cut and sent with their new length. A body that cannot be read fails the call like a failed upstream
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
		h.redactHeaders(resp.Header)
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return nil
		}
		body, _, err := upstream.ReadBody(resp, maxRedactedErrorBytes+redactLookahead)
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
