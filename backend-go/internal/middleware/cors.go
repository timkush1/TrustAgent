package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ExposedHeaders are the response headers a cross-origin browser may read (SPEC §2.4: ETag,
// Retry-After, X-Request-ID and every X-TrustAgent-* header, listed explicitly, never "*").
var ExposedHeaders = []string{
	"ETag", "Retry-After", "X-Request-ID",
	"X-TrustAgent-Trace-Id", "X-TrustAgent-Route", "X-TrustAgent-Mode", "X-TrustAgent-Action",
	"X-TrustAgent-Would-Action", "X-TrustAgent-Status", "X-TrustAgent-Verdict", "X-TrustAgent-Risk",
	"X-TrustAgent-Calibrated", "X-TrustAgent-Degraded", "X-TrustAgent-Reasons",
	"X-TrustAgent-Checked-Against", "X-TrustAgent-Decision-Model", "X-TrustAgent-Gate-Ms",
	"X-TrustAgent-Overhead-Ms", "X-TrustAgent-Audit", "X-TrustAgent-Source",
}

// allowedHeaders are the request headers a cross-origin browser may send: exactly the SPEC §2.4
// list (P4-PLAN D43). Authorization and X-API-Key are not on it: a cross-origin dashboard
// authenticates with X-TrustAgent-Key, and SDK clients, which send no preflight, still
// authenticate with either (authz/resolve.go).
var allowedHeaders = []string{
	"Content-Type", "If-Match", "X-TrustAgent-Key", "X-TrustAgent-CSRF", "X-TrustAgent-Route",
	"X-TrustAgent-Mode", "X-Request-ID",
}

// allowedMethods are the methods a cross-origin dashboard may use (SPEC §2.4).
var allowedMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// CORS reflects the request origin only when it appears in the allowlist
// (replacing the previous wildcard "*"). Requests from other origins get no
// CORS headers, so browsers block the cross-origin read.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}
	exposed := strings.Join(ExposedHeaders, ", ")
	requestHeaders := strings.Join(allowedHeaders, ", ")
	methods := strings.Join(allowedMethods, ", ")

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", methods)
			c.Header("Access-Control-Allow-Headers", requestHeaders)
			c.Header("Access-Control-Expose-Headers", exposed)
			c.Header("Access-Control-Max-Age", "600")
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
