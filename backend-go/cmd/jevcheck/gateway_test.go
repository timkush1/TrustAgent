package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/truthtable/contracts"
)

const gatewayAdminKey = "ta-admin-cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

// keyFile writes key to a file and returns its path.
func keyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.key")
	if err := os.WriteFile(path, []byte("\n"+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeGateway answers POST /api/decision-model/test for the admin key with the contracts fixture
// api/DecisionModelTestResult.<fixture>.json, and counts the calls.
func fakeGateway(t *testing.T, fixture string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	body, err := contracts.FS.ReadFile("fixtures/api/DecisionModelTestResult." + fixture + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != decisionModelTestPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-TrustAgent-Key") != gatewayAdminKey {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"this needs the admin scope"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func noKey(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, gatewayAdminKey) {
		t.Errorf("the admin key was printed:\n%s", out)
	}
}

// TestGatewayModeAsksTheGateway: --gateway reads the admin key from the file and asks the gateway
// to test its own decision model; the probe is the gateway's, so its cost ledger books it.
func TestGatewayModeAsksTheGateway(t *testing.T) {
	srv, calls := fakeGateway(t, "ok")
	host := strings.TrimPrefix(srv.URL, "http://")
	code, out := runCheck(t, nil, "--gateway", srv.URL+"/", "--admin-key-file", keyFile(t, gatewayAdminKey))
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out,
		"tested by the gateway at "+host,
		"Backend    openrouter-decisions",
		"Endpoint   openrouter.ai",
		"Model      typesafe/jev",
		"Probe      OK",
		"Snapshot   typesafe/jev-1.13-20260917",
		"Latency    231 ms",
		"Tokens     41 input",
		"Cost       $0.0000100 (reported)",
		"Ledger     booked: the gateway made the call",
		"Ready.")
	noKey(t, out)
	if calls.Load() != 1 {
		t.Errorf("%d gateway calls", calls.Load())
	}

	code, out = runCheck(t, nil, "--gateway", srv.URL, "--admin-key-file", keyFile(t, gatewayAdminKey), "-json")
	var r report
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != exitOK {
		t.Fatalf("exit %d: %v\n%s", code, err, out)
	}
	if !r.OK || r.Via != viaGateway || r.Gateway != host || r.CostLedger != ledgerBooked || r.Snapshot != "typesafe/jev-1.13-20260917" {
		t.Errorf("report %+v", r)
	}
}

// TestGatewayModeFailures: the gateway's failed test with its hint; an admin key the gateway
// refuses; a redirect, which is never followed with the key.
func TestGatewayModeFailures(t *testing.T) {
	srv, _ := fakeGateway(t, "no-credits")
	code, out := runCheck(t, nil, "--gateway", srv.URL, "--admin-key-file", keyFile(t, gatewayAdminKey))
	if code != exitFailed {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Probe      FAILED: credits (HTTP 402)", "OpenRouter answered 402", "Hint       Add credits", "Ledger     booked", "Not ready.")

	code, out = runCheck(t, nil, "--gateway", srv.URL, "--admin-key-file", keyFile(t, "ta-ingest-not-admin"))
	if code != exitFailed {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "not an admin key of this gateway", "Not ready.")

	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+decisionModelTestPath, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	code, out = runCheck(t, nil, "--gateway", redirect.URL, "--admin-key-file", keyFile(t, gatewayAdminKey))
	if code != exitFailed || elsewhere.Load() != 0 {
		t.Fatalf("a redirect: exit %d, %d requests elsewhere:\n%s", code, elsewhere.Load(), out)
	}
	contains(t, out, "redirect")
	noKey(t, out)
}

// TestGatewayModeRefusesMetadataAddresses: the call goes through the decision-model transport
// (internal/jev on internal/netguard), which refuses link-local and cloud-metadata addresses at
// dial time, so the admin key never leaves for one (D96).
func TestGatewayModeRefusesMetadataAddresses(t *testing.T) {
	for _, gw := range []string{"http://169.254.169.254", "http://169.254.169.254:8080", "http://[fe80::1]:8080"} {
		code, out := runCheck(t, nil, "--gateway", gw, "--admin-key-file", keyFile(t, gatewayAdminKey), "-timeout", "5s")
		if code != exitFailed {
			t.Fatalf("%s: exit %d:\n%s", gw, code, out)
		}
		contains(t, out, "gateway_unreachable", "link-local or cloud-metadata")
		noKey(t, out)
	}
}

func TestGatewayModeUsage(t *testing.T) {
	file := keyFile(t, gatewayAdminKey)
	for _, args := range [][]string{
		{"--gateway", "http://localhost:8080"},                                               // no admin key
		{"--admin-key-file", file},                                                           // no gateway
		{"--gateway", "localhost:8080", "--admin-key-file", file},                            // not a URL
		{"--gateway", "http://u:p@localhost:8080", "--admin-key-file", file},                 // credentials in the URL
		{"--gateway", "http://localhost:8080", "--admin-key-file", "/nonexistent"},           // unreadable key file
		{"--gateway", "http://localhost:8080", "--admin-key-file", file, "-backend", "mock"}, // a local-check flag
		{"--gateway", "http://localhost:8080", "--admin-key-file", file, "-no-key-check"},
	} {
		if code, out := runCheck(t, nil, args...); code != exitUsage {
			t.Errorf("%v: exit %d:\n%s", args, code, out)
		}
	}
}

// TestEnvModeSaysWhatItCannotSee: jevcheck's own probe is not booked on the gateway's ledger, and
// with no key in the environment it says a dashboard key is out of its sight (D122).
func TestEnvModeSaysWhatItCannotSee(t *testing.T) {
	code, out := runCheck(t, map[string]string{})
	if code != exitFailed {
		t.Fatalf("exit %d", code)
	}
	contains(t, out, "Note       "+dashboardKeyHint)

	_, srv := mockServer(t)
	code, out = runCheck(t, map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-0123456789", "TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL})
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Ledger     not booked: jevcheck called the vendor itself")
	if strings.Contains(out, dashboardKeyHint) {
		t.Errorf("a key in the environment needs no dashboard hint:\n%s", out)
	}

	code, out = runCheck(t, map[string]string{"TRUTHTABLE_JEV_BACKEND": "mock", "TRUTHTABLE_JEV_MOCK_MODE": "hash"})
	if code != exitOK || strings.Contains(out, dashboardKeyHint) {
		t.Errorf("mock: exit %d:\n%s", code, out)
	}
	contains(t, out, "Ledger     not booked (simulated: nothing was spent)")
}
