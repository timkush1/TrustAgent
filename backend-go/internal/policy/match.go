package policy

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/glob"
)

// RouteHeader is the request header that names a route (a match input only).
const RouteHeader = "X-TrustAgent-Route"

// Request is what route matching reads from a request.
type Request struct {
	Path    string
	Model   string
	Headers http.Header
	// KeyLabel is the label of the ingest key that authenticated the request ("" when open).
	KeyLabel string
	// BodyRoute is the trustagent.route body member. It stands in for the X-TrustAgent-Route header
	// when the request does not send that header.
	BodyRoute string
}

// Match returns the first route whose every given matcher matches, or the defaults route.
func (p *Policy) Match(req *Request) *Route {
	for _, r := range p.Routes {
		if r.Match.matches(req) {
			return r
		}
	}
	return p.Default
}

func (m *Match) matches(req *Request) bool {
	for name, want := range m.Headers {
		got := req.Headers.Get(name)
		if got == "" && strings.EqualFold(name, RouteHeader) {
			got = req.BodyRoute
		}
		if got != want {
			return false
		}
	}
	if len(m.Paths) > 0 && !glob.Any(m.Paths, req.Path) {
		return false
	}
	if len(m.Models) > 0 && !glob.Any(m.Models, req.Model) {
		return false
	}
	if len(m.KeyLabels) > 0 && (req.KeyLabel == "" || !slices.Contains(m.KeyLabels, req.KeyLabel)) {
		return false
	}
	return true
}

// Label is the X-TrustAgent-Route value: "<route>@v<policy_version>".
func (p *Policy) Label(r *Route) string {
	return r.Name + "@v" + strconv.FormatInt(p.Version, 10)
}

// modeRank orders modes by strictness for overrides.client_mode stricter_only.
var modeRank = map[string]int{decision.ModeObserve: 0, decision.ModeFlag: 1, decision.ModeBlock: 2, decision.ModeFallback: 2}

// Mode returns the configured mode for a request to route r: the route mode, or the mode the client
// asked for in X-TrustAgent-Mode when overrides.client_mode allows it (stricter_only: only a
// stricter mode). The second result reports whether the client's mode was applied. A client may
// only ask for a mode this build enforces (block and fallback need Env.Enforcing).
func (p *Policy) Mode(r *Route, requested string) (string, bool) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	enforceable := []string{decision.ModeObserve, decision.ModeFlag}
	if p.enforcing {
		enforceable = validModes
	}
	if requested == "" || requested == r.Mode || !slices.Contains(enforceable, requested) {
		return r.Mode, false
	}
	switch p.Overrides.ClientMode {
	case ClientModeAny:
		return requested, true
	case ClientModeStricterOnly:
		if modeRank[requested] > modeRank[r.Mode] {
			return requested, true
		}
	}
	return r.Mode, false
}
