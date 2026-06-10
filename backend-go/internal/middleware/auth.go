package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// APIKeyAuth validates requests against a static key set. Keys are accepted
// via "Authorization: Bearer <key>" or the "X-API-Key" header. With an empty
// key set, authentication is disabled (development mode) — the caller is
// expected to log a startup warning.
//
// Comparison is constant-time per candidate key to avoid timing side channels.
func APIKeyAuth(keys []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(keys) == 0 {
			c.Next()
			return
		}

		if !KeyAllowed(keys, extractKey(c.Request)) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing or invalid API key",
			})
			return
		}
		c.Next()
	}
}

// KeyAllowed reports whether candidate matches any configured key using
// constant-time comparison. Exported for the WebSocket handshake path, which
// authenticates via query parameter (browsers cannot set WS headers).
func KeyAllowed(keys []string, candidate string) bool {
	if candidate == "" {
		return false
	}
	allowed := false
	for _, key := range keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			allowed = true
		}
	}
	return allowed
}

func extractKey(r *http.Request) string {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
