package credentials

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
)

// verifyUnmatchedSecret matches no redaction pattern (P4-PLAN §0 rule 8): only the literal
// masking of the candidate secret can hide it.
const verifyUnmatchedSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

// startMockUpstream runs mock-upstream (OpenRouter, TypeSafe and the LLM vendors) in process.
func startMockUpstream(t *testing.T) (*mockupstream.Server, *httptest.Server) {
	t.Helper()
	m, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(m)
	t.Cleanup(hs.Close)
	return m, hs
}

func originOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return jev.CanonicalOrigin(u)
}

// recordingBudget admits every reservation and records it.
type recordingBudget struct {
	mu      sync.Mutex
	reqs    []jev.SpendRequest
	refuse  bool
	settled []float64
}

func (b *recordingBudget) Reserve(r jev.SpendRequest) (jev.Reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqs = append(b.reqs, r)
	if b.refuse {
		return nil, jev.ErrBudgetExhausted
	}
	return settleFunc(func(usd float64) {
		b.mu.Lock()
		b.settled = append(b.settled, usd)
		b.mu.Unlock()
	}), nil
}

type settleFunc func(float64)

func (f settleFunc) Settle(usd float64) { f(usd) }

// costLog records the cost events of the probes.
type costLog struct {
	mu     sync.Mutex
	events []jev.CostEvent
}

func (c *costLog) RecordCost(e jev.CostEvent) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *costLog) all() []jev.CostEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]jev.CostEvent(nil), c.events...)
}

func fastHTTP() *jev.HTTPConfig {
	h := jev.DefaultHTTPConfig()
	h.AttemptTimeout, h.MaxRetries = 2*time.Second, 0
	return &h
}

func openRouterVerifier(base string, budget jev.Budget, costs jev.CostSink) *Verifier {
	return &Verifier{HTTP: fastHTTP(), OpenRouterBaseURL: base, Budget: budget, Costs: costs, Timeout: 5 * time.Second}
}

func TestVerifyOpenRouterOKReadsKeyInfoAndBooksANonEnforcingProbe(t *testing.T) {
	m, hs := startMockUpstream(t)
	budget, costs := &recordingBudget{}, &costLog{}
	v := openRouterVerifier(hs.URL, budget, costs)
	got := v.Verify(context.Background(), VerifyTarget{
		Name: "openrouter", Secret: "sk-or-v1-e2e-good-0123456789abcdef", AllowedOrigins: []string{originOf(t, hs.URL)},
	})
	if !got.OK || got.Status != VerifyOK || !got.Checked {
		t.Fatalf("verification = %+v, want ok", got)
	}
	if got.Hint != HintNoLimit {
		t.Errorf("hint = %q, want %q (the mock key has no spending limit)", got.Hint, HintNoLimit)
	}
	if got.KeyLabel == nil || *got.KeyLabel == "" {
		t.Errorf("key label not filled: %+v", got)
	}
	if got.Backend != jev.BackendOpenRouterDecisions || got.Snapshot == "" || got.InputTokens == nil {
		t.Errorf("probe details missing: %+v", got)
	}
	if got.CostUSD == nil || *got.CostUSD <= 0 || got.CostSource != "reported" {
		t.Errorf("cost = %v %q, want the reported probe cost", got.CostUSD, got.CostSource)
	}
	if got.Latency <= 0 {
		t.Errorf("latency = %v", got.Latency)
	}
	budget.mu.Lock()
	reqs := append([]jev.SpendRequest(nil), budget.reqs...)
	budget.mu.Unlock()
	if len(reqs) == 0 {
		t.Fatal("the probe was not admitted through the shared budget")
	}
	for _, r := range reqs {
		if r.Enforcing {
			t.Errorf("the probe asked for the enforcing reserve: %+v (D82)", r)
		}
	}
	ev := costs.all()
	if len(ev) != 1 || ev[0].Component != jev.ComponentProbe || ev[0].Status != "ok" {
		t.Fatalf("cost events = %+v, want one ok probe event", ev)
	}
	// One key check and one decision call, both on the OpenRouter origin of the mock.
	var paths []string
	for _, r := range m.Requests() {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, ",") != "/api/v1/key,/api/alpha/decisions" {
		t.Errorf("mock-upstream saw %v", paths)
	}
}

func TestVerifyOpenRouterStatusMapping(t *testing.T) {
	_, hs := startMockUpstream(t)
	cases := []struct {
		key, status, hint string
		http              int
		keyInfo           bool
	}{
		// The key check passes (GET /api/v1/key answers 200 with an exhausted limit); the probe
		// gets 402.
		{key: "sk-or-v1-e2e-nocredits-0123456789", status: VerifyNoCredits, hint: HintNoCredits, http: 402, keyInfo: true},
		{key: "sk-or-v1-e2e-forbidden-0123456789", status: VerifyUnauthorized, hint: "", http: 403},
		{key: "sk-or-v1-e2e-invalid-0123456789", status: VerifyUnauthorized, hint: "", http: 401},
	}
	for _, tc := range cases {
		t.Run(tc.status+"/"+tc.key[13:20], func(t *testing.T) {
			v := openRouterVerifier(hs.URL, &recordingBudget{}, nil)
			got := v.Verify(context.Background(), VerifyTarget{Name: "openrouter", Secret: tc.key, AllowedOrigins: []string{originOf(t, hs.URL)}})
			if got.OK || got.Status != tc.status || got.Hint != tc.hint || got.HTTPStatus != tc.http || !got.Checked {
				t.Fatalf("verification = %+v, want status %s hint %q HTTP %d", got, tc.status, tc.hint, tc.http)
			}
			if strings.Contains(got.Message, tc.key) {
				t.Error("the message carries the key")
			}
			if tc.keyInfo {
				if got.KeyLabel == nil || got.LimitUSD == nil || *got.LimitUSD != 1 || got.LimitRemainingUSD == nil || *got.LimitRemainingUSD != 0 {
					t.Errorf("key info = label %v limit %v remaining %v, want the mock's exhausted $1 limit", got.KeyLabel, got.LimitUSD, got.LimitRemainingUSD)
				}
			}
		})
	}
}

func TestVerifyTypeSafeAndCompatibleProbe(t *testing.T) {
	_, hs := startMockUpstream(t)
	origin := originOf(t, hs.URL)
	v := &Verifier{HTTP: fastHTTP(), TypeSafeBaseURL: hs.URL, Timeout: 5 * time.Second}

	got := v.Verify(context.Background(), VerifyTarget{Name: "typesafe", Secret: "sk-test-typesafe-0123456789", AllowedOrigins: []string{origin}})
	if !got.OK || got.Backend != jev.BackendTypeSafe || got.KeyLabel != nil || got.Hint != "" {
		t.Errorf("typesafe = %+v, want ok without key info", got)
	}
	got = v.Verify(context.Background(), VerifyTarget{Name: "typesafe", Secret: "sk-test-forbidden-0123456789", AllowedOrigins: []string{origin}})
	if got.OK || got.Status != VerifyUnauthorized {
		t.Errorf("forbidden typesafe key = %+v, want unauthorized", got)
	}
	got = v.Verify(context.Background(), VerifyTarget{
		Name: "compatible", Secret: "sk-test-gateway-0123456789", BaseURL: hs.URL, Model: "typesafe/jev", AllowedOrigins: []string{origin},
	})
	if !got.OK || got.Backend != jev.BackendCompatible {
		t.Errorf("compatible = %+v, want ok", got)
	}
	// The compatible backend needs a model: nothing is sent without one.
	got = v.Verify(context.Background(), VerifyTarget{Name: "compatible", Secret: "sk-test-gateway-0123456789", BaseURL: hs.URL, AllowedOrigins: []string{origin}})
	if got.OK || got.Status != VerifyUnknown || got.Hint != HintNotChecked || got.Checked || got.Kind != jev.KindOff {
		t.Errorf("compatible without a model = %+v, want unknown/not_checked", got)
	}
}

func TestVerifyNeverSendsTheKeyOutsideItsOrigins(t *testing.T) {
	m, hs := startMockUpstream(t)
	v := openRouterVerifier(hs.URL, &recordingBudget{}, nil)
	got := v.Verify(context.Background(), VerifyTarget{
		Name: "openrouter", Secret: "sk-or-v1-e2e-good-0123456789abcdef", AllowedOrigins: []string{"https://openrouter.ai"},
	})
	if got.OK || got.Status != VerifyUnknown || got.Kind != jev.KindCredentialsUnavailable {
		t.Fatalf("verification = %+v, want credentials_unavailable", got)
	}
	if n := len(m.Requests()); n != 0 {
		t.Fatalf("mock-upstream got %d requests; the key must not leave for an origin it is not bound to", n)
	}

	// The same for a vendor GET /v1/models check.
	var hits atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer vendor.Close()
	v = &Verifier{OpenAIBaseURL: vendor.URL, Timeout: 5 * time.Second}
	got = v.Verify(context.Background(), VerifyTarget{Name: "openai", Secret: "sk-test-openai-0123456789", AllowedOrigins: []string{"https://api.openai.com"}})
	if got.OK || got.Kind != jev.KindCredentialsUnavailable || hits.Load() != 0 {
		t.Fatalf("verification = %+v with %d vendor hits, want a refusal before any request", got, hits.Load())
	}
}

func TestVerifyProbeSkippedByTheBudget(t *testing.T) {
	_, hs := startMockUpstream(t)
	origin := originOf(t, hs.URL)
	v := openRouterVerifier(hs.URL, &recordingBudget{refuse: true}, nil)
	v.TypeSafeBaseURL = hs.URL
	// OpenRouter proved the key with the free key check: ok, with the probe marked as skipped.
	got := v.Verify(context.Background(), VerifyTarget{Name: "openrouter", Secret: "sk-or-v1-e2e-good-0123456789", AllowedOrigins: []string{origin}})
	if !got.OK || got.Hint != HintProbeSkipped || got.Kind != jev.KindBudget || got.KeyLabel == nil {
		t.Errorf("openrouter with an exhausted budget = %+v, want ok/probe_skipped", got)
	}
	// TypeSafe has no free check: unknown, with the budget as the reason.
	got = v.Verify(context.Background(), VerifyTarget{Name: "typesafe", Secret: "sk-test-typesafe-0123456789", AllowedOrigins: []string{origin}})
	if got.OK || got.Status != VerifyUnknown || got.Hint != jev.KindBudget {
		t.Errorf("typesafe with an exhausted budget = %+v, want unknown/budget", got)
	}
}

func TestVerifyVendorModelsCheck(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	status := http.StatusOK
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		st := status
		mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		if st != http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": "x", "message": "vendor says no"}})
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer vendor.Close()
	origin := originOf(t, vendor.URL)
	v := &Verifier{AnthropicBaseURL: vendor.URL, OpenAIBaseURL: vendor.URL, Timeout: 5 * time.Second}

	got := v.Verify(context.Background(), VerifyTarget{Name: "anthropic", Secret: "sk-ant-test-0123456789", AllowedOrigins: []string{origin}})
	if !got.OK || got.Status != VerifyOK || got.CostUSD != nil {
		t.Fatalf("anthropic = %+v, want a free ok", got)
	}
	got = v.Verify(context.Background(), VerifyTarget{Name: "openai", Secret: "sk-test-openai-0123456789", AllowedOrigins: []string{origin}})
	if !got.OK {
		t.Fatalf("openai = %+v, want ok", got)
	}
	mu.Lock()
	if h := seen[0]; h.Get("X-Api-Key") != "sk-ant-test-0123456789" || h.Get("Anthropic-Version") == "" || h.Get("Authorization") != "" {
		t.Errorf("anthropic headers wrong (x-api-key and anthropic-version, no Authorization)")
	}
	if h := seen[1]; h.Get("Authorization") != "Bearer sk-test-openai-0123456789" || h.Get("X-Api-Key") != "" {
		t.Errorf("openai headers wrong (Authorization: Bearer only)")
	}
	mu.Unlock()

	for _, tc := range []struct {
		code         int
		status, hint string
	}{
		{http.StatusUnauthorized, VerifyUnauthorized, ""},
		{http.StatusForbidden, VerifyUnauthorized, ""},
		{http.StatusPaymentRequired, VerifyNoCredits, HintNoCredits},
		{http.StatusServiceUnavailable, VerifyUnreachable, jev.KindRetryable},
		{http.StatusNotFound, VerifyUnknown, jev.KindNotFound},
	} {
		mu.Lock()
		status = tc.code
		mu.Unlock()
		got := v.Verify(context.Background(), VerifyTarget{Name: "anthropic", Secret: "sk-ant-test-0123456789", AllowedOrigins: []string{origin}})
		if got.OK || got.Status != tc.status || got.Hint != tc.hint || got.HTTPStatus != tc.code || !strings.Contains(got.Message, "vendor says no") {
			t.Errorf("HTTP %d → %+v, want %s/%q", tc.code, got, tc.status, tc.hint)
		}
	}
}

func TestVerifyVendorCheckDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer vendor.Close()
	v := &Verifier{AnthropicBaseURL: vendor.URL, Timeout: 5 * time.Second}
	got := v.Verify(context.Background(), VerifyTarget{Name: "anthropic", Secret: "sk-ant-test-0123456789", AllowedOrigins: []string{originOf(t, vendor.URL)}})
	if got.OK || elsewhere.Load() != 0 {
		t.Fatalf("verification = %+v, redirect target hit %d times; the key must stay at the vendor", got, elsewhere.Load())
	}
}

func TestVerifyUnreachable(t *testing.T) {
	// A port nothing listens on.
	l := httptest.NewServer(http.NotFoundHandler())
	base := l.URL
	l.Close()
	v := &Verifier{HTTP: fastHTTP(), OpenRouterBaseURL: base, OpenAIBaseURL: base, Timeout: 5 * time.Second}
	got := v.Verify(context.Background(), VerifyTarget{Name: "openai", Secret: "sk-test-openai-0123456789", AllowedOrigins: []string{originOf(t, base)}})
	if got.OK || got.Status != VerifyUnreachable {
		t.Errorf("openai on a closed port = %+v, want unreachable", got)
	}
	got = v.Verify(context.Background(), VerifyTarget{Name: "openrouter", Secret: "sk-or-v1-e2e-good-0123456789", AllowedOrigins: []string{originOf(t, base)}})
	if got.OK || got.Status != VerifyUnreachable {
		t.Errorf("openrouter on a closed port = %+v, want unreachable", got)
	}
}

func TestVerifyUpstreamKeysAreNotChecked(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	v := &Verifier{OpenRouterBaseURL: srv.URL, OpenAIBaseURL: srv.URL, AnthropicBaseURL: srv.URL, TypeSafeBaseURL: srv.URL}
	for _, name := range []string{"upstream-default", "upstream-anthropic", "upstream-fallback"} {
		got := v.Verify(context.Background(), VerifyTarget{Name: name, Secret: "sk-test-upstream-0123456789", BaseURL: srv.URL, AllowedOrigins: []string{originOf(t, srv.URL)}})
		if got.OK || got.Status != VerifyUnknown || got.Hint != HintNotChecked || got.Checked {
			t.Errorf("%s = %+v, want unknown/not_checked", name, got)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("an upstream key check made %d requests, want none", hits.Load())
	}
}

func TestVerifyErrorTextIsRedactedAndTruncated(t *testing.T) {
	// The pattern set alone must leave the unmatched secret visible, or this test proves nothing.
	if got := config.NewRedactor().Redact(verifyUnmatchedSecret); got != verifyUnmatchedSecret {
		t.Fatalf("the redaction patterns mask the unmatched secret (%q); pick another", got)
	}
	other := "sk-test-other-secret-0123456789"
	// The secret straddles the 300-character cut, where a cut-then-mask order would leave its
	// prefix behind.
	var message atomic.Pointer[string]
	long := strings.Repeat("é", 285) + " key " + verifyUnmatchedSecret + " also " + other + " " + strings.Repeat("z", 400)
	message.Store(&long)
	echo := func(w http.ResponseWriter, envelope string) {
		long := *message.Load()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		var body map[string]any
		if envelope == "typesafe" {
			body = map[string]any{"detail": map[string]any{"error_type": "invalid_request", "message": long}}
		} else {
			body = map[string]any{"error": map[string]any{"message": long, "type": "invalid_request_error"}}
		}
		_ = json.NewEncoder(w).Encode(body)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			echo(w, "openai")
			return
		}
		echo(w, "typesafe")
	}))
	defer srv.Close()
	origin := originOf(t, srv.URL)
	var processRedacted atomic.Int32
	v := &Verifier{
		HTTP: fastHTTP(), OpenAIBaseURL: srv.URL, TypeSafeBaseURL: srv.URL, Timeout: 5 * time.Second,
		Redact: func(s string) string { processRedacted.Add(1); return config.NewRedactor(other).Redact(s) },
	}
	for _, name := range []string{"openai", "typesafe"} {
		got := v.Verify(context.Background(), VerifyTarget{Name: name, Secret: verifyUnmatchedSecret, AllowedOrigins: []string{origin}})
		if got.OK || got.Message == "" {
			t.Fatalf("%s: verification = %+v, want a failure with a message", name, got)
		}
		if n := utf8.RuneCountInString(got.Message); n > MaxVerifyMessage {
			t.Errorf("%s: message has %d characters, want at most %d", name, n, MaxVerifyMessage)
		}
		for i := 6; i <= len(verifyUnmatchedSecret); i++ {
			if strings.Contains(got.Message, verifyUnmatchedSecret[:i]) {
				t.Fatalf("%s: the message carries a %d-character prefix of the candidate secret", name, i)
			}
		}
		if strings.Contains(got.Message, "sk-test-other") {
			t.Errorf("%s: the message carries another secret", name)
		}
	}
	if processRedacted.Load() == 0 {
		t.Error("the process redactor was never applied")
	}

	// A short message keeps its text, with the secret masked in place.
	short := "invalid key " + verifyUnmatchedSecret + " for this account"
	message.Store(&short)
	got := v.Verify(context.Background(), VerifyTarget{Name: "openai", Secret: verifyUnmatchedSecret, AllowedOrigins: []string{origin}})
	if want := "HTTP 400: invalid key " + config.Masked + " for this account"; got.Message != want {
		t.Errorf("message = %q, want %q", got.Message, want)
	}
}

func TestMaskCutSecret(t *testing.T) {
	secret := verifyUnmatchedSecret
	if got := maskCutSecret("bad key "+secret[:9]+"…", secret); got != "bad key "+config.Masked+"…" {
		t.Errorf("cut prefix: %q", got)
	}
	if got := maskCutSecret("bad key "+secret[:9], secret); got != "bad key "+config.Masked {
		t.Errorf("prefix at the end: %q", got)
	}
	// Three characters are too few to tell from ordinary text.
	if got := maskCutSecret("ends with cmp…", secret); got != "ends with cmp…" {
		t.Errorf("short tail masked: %q", got)
	}
}

func TestTruncateChars(t *testing.T) {
	if got := truncateChars("abc", 3); got != "abc" {
		t.Errorf("no cut: %q", got)
	}
	got := truncateChars(strings.Repeat("é", 10), 5)
	if utf8.RuneCountInString(got) != 5 || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Errorf("cut = %q, want 4 characters and an ellipsis", got)
	}
}

func TestKeyInfoRemainingLimit(t *testing.T) {
	limit, remaining := 5.0, 1.5
	_, l, r := keyInfo(&jev.KeyInfo{Label: "x", UsageUSD: 2, LimitUSD: &limit})
	if *l != 5 || *r != 3 {
		t.Errorf("limit − usage: %v %v", *l, *r)
	}
	_, _, r = keyInfo(&jev.KeyInfo{UsageUSD: 2, LimitUSD: &limit, LimitRemainingUSD: &remaining})
	if *r != 1.5 {
		t.Errorf("the vendor's limit_remaining wins: %v", *r)
	}
	label, l, r := keyInfo(&jev.KeyInfo{UsageUSD: 2})
	if label != nil || l != nil || r != nil {
		t.Errorf("no label and no limit: %v %v %v", label, l, r)
	}
}
