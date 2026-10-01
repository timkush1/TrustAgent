package main

import "github.com/gin-gonic/gin"

// mountStudioAPI mounts the decision request, Studio and evidence-suggestion endpoints (SPEC §2.4
// rows 12, 34-38 and 51; P4, GSTUDIO). Step-0 stub: it registers nothing yet.
func (s *server) mountStudioAPI(api *gin.RouterGroup, g apiGuards) {
	_, _ = api, g
}
