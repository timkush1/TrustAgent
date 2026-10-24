package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/jev"
)

func mockServer(t *testing.T) (*mockupstream.Server, *httptest.Server) {
	t.Helper()
	m, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return m, srv
}

func runCheck(t *testing.T, env map[string]string, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return env[k] }, &out, &errOut)
	return code, out.String() + errOut.String()
}

func decisionCalls(m *mockupstream.Server) int {
	n := 0
	for _, r := range m.Requests() {
		if r.Method == http.MethodPost && (strings.HasSuffix(r.Path, "/decisions") || strings.HasSuffix(r.Path, "/systemone")) {
			n++
		}
	}
	return n
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestReadyWithOpenRouter(t *testing.T) {
	m, srv := mockServer(t)
	code, out := runCheck(t, map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-0123456789", "TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL})
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out,
		"Backend    openrouter-decisions",
		"Why        auto: OPENROUTER_API_KEY is set",
		"Endpoint   "+srv.URL+"/api/alpha/decisions",
		"Model      typesafe/jev-1.13",
		"Credential env",
		"Key        valid · label sk-or-…xxxx · usage $0.0123 · no credit limit",
		"Probe      OK",
		"Snapshot   typesafe/jev-1.13-20260917",
		"Latency", "· 1 attempt\n",
		"input",
		"(reported)",
		"Ready.")
	if strings.Contains(out, "sk-or-v1-test-0123456789") {
		t.Error("the key must never be printed")
	}
	if decisionCalls(m) != 1 {
		t.Errorf("%d decision calls", decisionCalls(m))
	}
}

// OpenRouter's key label is a truncated form of the key (mock-upstream: "sk-or-v1-t…789"): no part
// of it is printed, in text or in JSON (D211).
func TestTheKeyLabelIsNeverPrinted(t *testing.T) {
	_, srv := mockServer(t)
	env := map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-0123456789", "TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL}
	for _, args := range [][]string{nil, {"-json"}} {
		code, out := runCheck(t, env, args...)
		if code != exitOK {
			t.Fatalf("%v: exit %d:\n%s", args, code, out)
		}
		for _, leak := range []string{"sk-or-v1-t", "…789", "0123456789"} {
			if strings.Contains(out, leak) {
				t.Errorf("%v: the output holds %q from the key or its label:\n%s", args, leak, out)
			}
		}
		if !strings.Contains(out, MaskedLabel) {
			t.Errorf("%v: the output lacks the masked label %q", args, MaskedLabel)
		}
	}
}

func TestRejectedKeySkipsTheProbe(t *testing.T) {
	m, srv := mockServer(t)
	code, out := runCheck(t, map[string]string{"OPENROUTER_API_KEY": "sk-or-invalid-1", "TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL})
	if code != exitFailed {
		t.Fatalf("exit %d", code)
	}
	contains(t, out, "Key        check failed: auth (HTTP 401) User not found.", "Probe      FAILED: auth (HTTP 401)", "Hint       Key rejected", "Not ready.")
	if decisionCalls(m) != 0 {
		t.Error("a rejected key needs no paid probe")
	}
}

func TestErrorHints(t *testing.T) {
	cases := []struct {
		name  string
		env   func(url string) map[string]string
		wants []string
	}{
		{"wrong base URL", func(u string) map[string]string {
			return map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-x", "TRUTHTABLE_OPENROUTER_BASE_URL": u + "/fault/html200"}
		}, []string{"Key        check failed: bad_content_type", "Probe      FAILED: bad_content_type (HTTP 200)", "got a web page", "OpenRouter URLs end in /api"}},
		{"no credits", func(u string) map[string]string {
			return map[string]string{"OPENROUTER_API_KEY": "sk-or-nocredits-123456", "TRUTHTABLE_OPENROUTER_BASE_URL": u}
		}, []string{"limit reached", "Probe      FAILED: credits (HTTP 402)", "openrouter.ai/settings/credits"}},
		{"data policy", func(u string) map[string]string {
			return map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-x", "TRUTHTABLE_OPENROUTER_BASE_URL": u + "/fault/404_data_policy", "TRUTHTABLE_JEV_ZDR": "true"}
		}, []string{"FAILED: data_policy (HTTP 404)", "check openrouter.ai/settings/privacy or unset TRUTHTABLE_JEV_ZDR"}},
		{"busy", func(u string) map[string]string {
			return map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-x", "TRUTHTABLE_OPENROUTER_BASE_URL": u + "/fault/529", "TRUTHTABLE_JEV_MAX_RETRIES": "0"}
		}, []string{"FAILED: retryable (HTTP 529)", "Attempts   1", "The provider is busy"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, srv := mockServer(t)
			code, out := runCheck(t, c.env(srv.URL))
			if code != exitFailed {
				t.Errorf("exit %d", code)
			}
			contains(t, out, c.wants...)
		})
	}
}

func TestCompatibleBackendByFlags(t *testing.T) {
	_, srv := mockServer(t)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "jev.key")
	if err := os.WriteFile(keyFile, []byte("gw-key-0123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runCheck(t, map[string]string{"TRUTHTABLE_JEV_API_KEY_FILE": keyFile},
		"-backend", "compatible", "-base-url", srv.URL, "-model", "typesafe-ai/jev")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Backend    compatible", "TRUTHTABLE_JEV_BACKEND=compatible with the key from TRUTHTABLE_JEV_API_KEY",
		"Endpoint   "+srv.URL+"/v1/systemone", "Snapshot   jev-1.13.0", "(computed)", "Ready.")
	if strings.Contains(out, "Key  ") {
		t.Error("no key check outside OpenRouter")
	}
}

func TestOffAndMock(t *testing.T) {
	code, out := runCheck(t, map[string]string{})
	if code != exitFailed {
		t.Errorf("off exits %d", code)
	}
	contains(t, out, "Backend    off", "no decision-model credentials", "Hint       Fast checks are off", "Not ready.")

	code, out = runCheck(t, map[string]string{"TRUTHTABLE_JEV_BACKEND": "compatible", "TRUTHTABLE_JEV_API_KEY": "k"})
	if code != exitFailed {
		t.Errorf("misconfigured exits %d", code)
	}
	contains(t, out, "needs TRUTHTABLE_JEV_MODEL", "Fix the setting named above")

	code, out = runCheck(t, map[string]string{"TRUTHTABLE_JEV_BACKEND": "mock", "TRUTHTABLE_JEV_MOCK_MODE": "hash"})
	if code != exitOK {
		t.Errorf("mock exits %d:\n%s", code, out)
	}
	contains(t, out, "Backend    mock", "Probe      OK (simulated", "(simulated)", "Ready.")

	code, out = runCheck(t, map[string]string{"TRUTHTABLE_DEMO_MODE": "yes"})
	contains(t, out, `TRUTHTABLE_DEMO_MODE="yes" is not a boolean`)
	if code != exitFailed {
		t.Errorf("exit %d", code)
	}
}

func TestJSONOutput(t *testing.T) {
	_, srv := mockServer(t)
	code, out := runCheck(t, map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-x", "TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL,
		"TRUTHTABLE_JEV_BACKEND": "openrouter-systemone"}, "-json", "-no-key-check")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	var r report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !r.OK || r.Backend != jev.BackendOpenRouterSystemOne || r.Snapshot != mockupstream.DefaultSnapshot || r.Key != nil ||
		r.CostUSD == nil || *r.CostUSD <= 0 || r.CostSource != jev.CostReported || r.Tokens == nil || r.LatencyMs == nil || r.Attempts != 1 || r.Error != nil {
		t.Errorf("report %+v", r)
	}
	code, out = runCheck(t, map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-x", "TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL + "/fault/401"}, "-json")
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != exitFailed || r.OK || r.Error == nil || r.Error.Kind != jev.KindAuth || r.Error.Hint == "" {
		t.Errorf("failed report %+v (%v)", r, err)
	}
}

func TestUsage(t *testing.T) {
	if code, _ := runCheck(t, nil, "-nope"); code != exitUsage {
		t.Errorf("unknown flag: %d", code)
	}
	if code, _ := runCheck(t, nil, "extra"); code != exitUsage {
		t.Errorf("positional argument: %d", code)
	}
	if code, _ := runCheck(t, nil, "-timeout", "0s"); code != exitUsage {
		t.Errorf("zero timeout: %d", code)
	}
	if code, _ := runCheck(t, nil, "-h"); code != exitOK {
		t.Errorf("-h: %d", code)
	}
	if code, out := runCheck(t, map[string]string{"TRUTHTABLE_JEV_BACKEND": "mock", "TRUTHTABLE_JEV_MOCK_MODE": "psychic"}); code != exitUsage {
		t.Errorf("an unbuildable backend is a usage error: %d %s", code, out)
	}
}

func TestFormatUSD(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 0.0123: "0.0123", 0.0000139: "0.0000139", 0.00001: "0.0000100", 0.5: "0.50", 12.345: "12.35"} {
		if got := formatUSD(v); got != want {
			t.Errorf("formatUSD(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"TRUTHTABLE_JEV_ATTEMPT_TIMEOUT": "750ms", "TRUTHTABLE_JEV_MAX_RETRIES": "3", "TRUTHTABLE_JEV_RETRY_AFTER_CAP": "2s",
		"TRUTHTABLE_JEV_PRICE_PER_MTOK": "0.05", "TRUTHTABLE_JEV_CHARS_PER_TOKEN": "3.5", "TRUTHTABLE_JEV_ZDR": "true",
		"TRUTHTABLE_JEV_SEND_SESSION_ID": "false", "TRUTHTABLE_JEV_APP_URL": "https://my.app", "TRUTHTABLE_JEV_APP_TITLE": "Mine",
		"TRUTHTABLE_PUBLIC_URL": "https://p.example", "TRUTHTABLE_JEV_MOCK_SEED": "7", "TRUTHTABLE_JEV_MOCK_STRICT": "true",
		"TRUTHTABLE_JEV_PATH": "/x", "TRUTHTABLE_JEV_MODEL": "m", "TYPESAFE_API_KEY": "t", "TRUTHTABLE_JEV_API_KEY": "direct",
		"TRUTHTABLE_JEV_API_KEY_FILE": "/ignored/when/the/key/is/set",
	}
	cfg, warnings := configFromEnv(func(k string) string { return env[k] })
	h := cfg.HTTP
	if len(warnings) != 0 || h.AttemptTimeout.String() != "750ms" || h.MaxRetries != 3 || h.RetryAfterCap.String() != "2s" || h.PricePerMTok != 0.05 ||
		h.CharsPerToken != 3.5 || !h.ZDR || h.SendSessionID || h.AppURL != "https://my.app" || h.AppTitle != "Mine" || h.PublicURL != "https://p.example" ||
		h.KeepAlive != 0 || cfg.Mock.Seed != 7 || !cfg.Mock.Strict || cfg.Path != "/x" || cfg.Model != "m" || cfg.TypeSafeKey != "t" || cfg.JevAPIKey != "direct" {
		t.Errorf("config %+v http %+v warnings %v", cfg, h, warnings)
	}
	bad := map[string]string{"TRUTHTABLE_JEV_ATTEMPT_TIMEOUT": "soon", "TRUTHTABLE_JEV_MAX_RETRIES": "-1", "TRUTHTABLE_JEV_PRICE_PER_MTOK": "free",
		"TRUTHTABLE_JEV_ZDR": "maybe", "TRUTHTABLE_JEV_MOCK_SEED": "x", "TRUTHTABLE_JEV_API_KEY_FILE": "/nonexistent/key"}
	cfg, warnings = configFromEnv(func(k string) string { return bad[k] })
	if len(warnings) != 6 || cfg.HTTP.AttemptTimeout.String() != "3s" || cfg.HTTP.MaxRetries != 1 || cfg.HTTP.ZDR {
		t.Errorf("invalid values keep their defaults: %+v %v", cfg.HTTP, warnings)
	}
}
