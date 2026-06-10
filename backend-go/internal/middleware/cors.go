package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS reflects the request origin only when it appears in the allowlist
// (replacing the previous wildcard "*"). Requests from other origins get no
// CORS headers, so browsers block the cross-origin read.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Request-ID")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// OriginAllowed reports whether a (possibly empty) Origin header is acceptable
// for the WebSocket handshake. Empty origins are allowed: non-browser clients
// (curl, monitoring probes) don't send one, and the header provides no
// protection against them anyway — it only defends browsers against CSWSH.
func OriginAllowed(allowedOrigins []string, origin string) bool {
	if origin == "" {
		return true
	}
	for _, allowed := range allowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}
