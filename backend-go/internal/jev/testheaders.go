package jev

import (
	"net/http"
	"net/url"
)

// forwardTestHeaders sets the test-mode headers of one decision attempt (D119): the
// scenario id as X-TrustAgent-Scenario, and the call's trace id as X-Mock-Key, which mock-upstream
// uses to count a fault spec such as "429@1" per call (the attempt and its hedge share it). They
// are sent only when forwarding is on (HTTPConfig.ForwardScenario, test mode) and the endpoint is
// local (localHost: localhost, a loopback address or a single-label name such as mock-upstream),
// so a test header never reaches openrouter.ai or any other real vendor, even with test mode on.
// A private IP address is not local here.
func forwardTestHeaders(h http.Header, endpoint *url.URL, opt CallOptions, enabled bool) {
	if !enabled || endpoint == nil || !localHost(endpoint.Hostname()) {
		return
	}
	if opt.Scenario != "" {
		h.Set("X-TrustAgent-Scenario", opt.Scenario)
	}
	if opt.TraceID != "" {
		h.Set("X-Mock-Key", opt.TraceID)
	}
}
