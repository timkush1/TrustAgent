package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/truthtable/backend-go/internal/jev"
)

// Where a probe's cost lands (report.CostLedger).
const (
	// ledgerBooked: the gateway made the call, so its cost ledger counts it.
	ledgerBooked = "booked"
	// ledgerNotBooked: jevcheck called the vendor itself; the gateway's ledger never sees it.
	ledgerNotBooked = "not_booked"
)

// dashboardKeyHint is the env-mode hint when the environment holds no decision-model key (D122):
// a key connected in the dashboard lives in the gateway's credential store, out of jevcheck's reach.
const dashboardKeyHint = "A key connected in the dashboard is not visible here; use --gateway or Settings → Test."

// decisionModelTestPath is the gateway's decision-model test (SPEC §2.4, admin scope).
const decisionModelTestPath = "/api/decision-model/test"

// gatewayResult is the gateway's answer to POST /api/decision-model/test (api.v1
// DecisionModelTestResult).
type gatewayResult struct {
	OK             bool     `json:"ok"`
	Backend        string   `json:"backend"`
	EndpointHost   *string  `json:"endpoint_host"`
	ModelRequested *string  `json:"model_requested"`
	Snapshot       *string  `json:"snapshot"`
	LatencyMs      *int64   `json:"latency_ms"`
	InputTokens    *int     `json:"input_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	CostSource     *string  `json:"cost_source"`
	Error          *struct {
		Kind    string `json:"kind"`
		Status  *int   `json:"status"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

// readKeyFile is the first non-empty line of path. The key is never printed.
func readKeyFile(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the key file (--admin-key-file)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k := strings.TrimSpace(line); k != "" {
			return k, nil
		}
	}
	return "", fmt.Errorf("%s holds no key", path)
}

// gatewayURL checks --gateway: an http(s) base URL with a host, no query or fragment.
func gatewayURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("--gateway %q is not an http(s) base URL such as http://localhost:8080", raw)
	}
	return u, nil
}

// checkGateway asks the gateway at base to test its own decision model, with its credentials
// (a key connected in the dashboard included), and turns the answer into a report. The call goes
// through the decision-model transport (internal/jev: no link-local or cloud-metadata address)
// and never follows a redirect, so the admin key reaches only the gateway named.
func checkGateway(ctx context.Context, base *url.URL, adminKey string) report {
	rep := report{Via: viaGateway, Gateway: base.Host}
	client := &http.Client{
		Transport:     jev.NewTransport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String()+decisionModelTestPath, bytes.NewReader(nil))
	if err != nil {
		rep.Error = &reportError{Kind: "usage", Message: err.Error(), Hint: "Check --gateway."}
		return rep
	}
	req.Header.Set("X-TrustAgent-Key", adminKey)
	resp, err := client.Do(req)
	if err != nil {
		rep.Error = &reportError{Kind: "gateway_unreachable", Message: redactKey(err.Error(), adminKey),
			Hint: "Is the gateway running at that address? (`make up`, then --gateway http://localhost:8080)"}
		return rep
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	snippet := redactKey(strings.TrimSpace(string(body[:min(len(body), 300)])), adminKey)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		rep.Error = &reportError{Kind: "gateway_auth", Status: resp.StatusCode, Message: snippet,
			Hint: "The key in --admin-key-file is not an admin key of this gateway (the test needs the admin scope)."}
		return rep
	case resp.StatusCode == http.StatusTooManyRequests:
		rep.Error = &reportError{Kind: "gateway_rate_limited", Status: resp.StatusCode, Message: snippet,
			Hint: "The gateway runs one test per caller every 10 seconds; try again shortly."}
		return rep
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		rep.Error = &reportError{Kind: "gateway_redirect", Status: resp.StatusCode, Message: "the gateway answered with a redirect",
			Hint: "Point --gateway at the gateway itself, not a page that redirects."}
		return rep
	case resp.StatusCode != http.StatusOK:
		rep.Error = &reportError{Kind: "gateway_error", Status: resp.StatusCode, Message: snippet, Hint: "See the gateway's log."}
		return rep
	}
	var res gatewayResult
	if err := json.Unmarshal(body, &res); err != nil || res.Backend == "" {
		rep.Error = &reportError{Kind: "gateway_error", Status: resp.StatusCode, Message: "not a decision-model test result: " + snippet,
			Hint: "Is that address a TrustAgent gateway?"}
		return rep
	}
	rep.Backend, rep.Simulated = res.Backend, res.Backend == jev.BackendMock
	rep.Endpoint, rep.Model, rep.Snapshot = deref(res.EndpointHost), deref(res.ModelRequested), deref(res.Snapshot)
	rep.LatencyMs, rep.Tokens, rep.CostUSD, rep.CostSource = res.LatencyMs, res.InputTokens, res.CostUSD, deref(res.CostSource)
	// A probe the gateway skipped without a call (off, an open breaker, the budget) has no
	// latency: nothing reached a vendor, so nothing was booked. The mock backend answers in
	// process and books nothing either (simulated: nothing was spent).
	if res.LatencyMs != nil {
		rep.Attempts = 1
		rep.CostLedger = ledgerBooked
		if rep.Simulated {
			rep.CostLedger = ledgerNotBooked
		}
	}
	if res.Error != nil {
		rep.Error = &reportError{Kind: res.Error.Kind, Message: res.Error.Message, Hint: res.Error.Hint}
		if res.Error.Status != nil {
			rep.Error.Status = *res.Error.Status
		}
	} else if !res.OK {
		rep.Error = &reportError{Kind: "unknown", Message: "the gateway reported a failed test without an error", Hint: "See the gateway's log."}
	}
	return rep
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// redactKey removes the admin key from text the gateway or the network stack wrote.
func redactKey(text, key string) string {
	if key == "" {
		return text
	}
	return strings.ReplaceAll(text, key, "[redacted]")
}

// errGatewayFlags is the usage error of --gateway with flags that configure a local check.
var errGatewayFlags = errors.New("--gateway tests the gateway's own decision model: -backend, -model, -base-url, -path and -no-key-check do not apply")
