package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// WriteDeadline sets each response's write deadline to now + deadline(c) (SPEC M12: with
// TRUTHTABLE_WRITE_TIMEOUT=0 the HTTP server has no global write timeout, and each route family
// gets its own deadline, which a handler may extend with http.ResponseController). A deadline of 0
// sets none (the WebSocket hub sets one per frame). net/http clears the deadline after each
// response, so it never carries over to the next request on a keep-alive connection. A
// ResponseWriter that cannot set deadlines (test recorders) is left as it is.
func WriteDeadline(deadline func(c *gin.Context) time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d := deadline(c); d > 0 {
			_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(d))
		}
		c.Next()
	}
}
