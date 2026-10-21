package mockupstream

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The OpenRouter vendor fixtures (contracts/fixtures/vendor/openrouter, D122) are what
// this mock answers, written down for the browser's tests (Playwright's mockOpenRouter, vitest).
// They are synthetic: made from faultTable, the handlers in openrouter.go and llm.go, and SPEC §6,
// never captured from openrouter.ai, and each says so in its source. Their access-control-*
// headers are the ones openrouter.ai sent at Step 0a (2026-10-09), which this mock does not send.
// This test keeps each file equal to what the mock answers today; after a deliberate change run
//
//	go test ./cmd/mock-upstream/mockupstream -run TestVendorFixtures -update
//
// and review the diff.
var updateVendorFixtures = flag.Bool("update", false, "rewrite contracts/fixtures/vendor/openrouter from the mock")

const vendorFixtureSource = "synthetic: mock-upstream faultTable + SPEC §6"

// vendorFixtureDir is contracts/fixtures/vendor/openrouter from this package's directory.
var vendorFixtureDir = filepath.Join("..", "..", "..", "..", "contracts", "fixtures", "vendor", "openrouter")

// observedCORS are the CORS response headers openrouter.ai sent to the Pages origin at Step 0a.
var observedCORS = map[string]string{
	"access-control-allow-origin":   "*",
	"access-control-expose-headers": "X-Generation-Id,X-Provider-Name,request-id,cf-ray",
}

// RFC 7636 Appendix B: the verifier of the auth_keys exchanges and its S256 challenge.
const (
	fixtureVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	fixtureChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	fixtureKey       = "Bearer sk-or-v1-e2e-fixture-key"
	fixtureCode      = "mockcode_000000000000000000000000"
)

type vendorRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body,omitempty"`
}

type vendorFixture struct {
	Source  string            `json:"source"`
	Request vendorRequest     `json:"request"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

// vendorCase is one fixture: the request that produces it, and whether it exchanges a code issued
// by /auth first (auth_keys).
type vendorCase struct {
	name     string
	req      vendorRequest
	issue    bool   // issue a code for fixtureChallenge first and send it as body.code
	verifier string // the code_verifier sent with an issued code
}

var fixtureDecisionBody = map[string]any{
	"model": "typesafe/jev-1.13",
	"state": map[string]any{
		"evidence":           []any{"Refunds are accepted within 30 days of purchase."},
		"assistant_response": "You can return it within 60 days for a full refund.",
	},
	"questions": map[string]any{
		"contradiction__v1": map[string]any{
			"type":         "noul",
			"instructions": "Does the assistant_response contradict the evidence?",
			"criteria":     map[string]any{"true": "It states something the evidence rules out.", "false": "Nothing in it conflicts with the evidence."},
		},
	},
}

func decisionCase(name, fault, auth string) vendorCase {
	h := map[string]string{"authorization": auth, "content-type": "application/json"}
	if fault != "" {
		h["x-mock-fault"] = fault
	}
	return vendorCase{name: name, req: vendorRequest{Method: http.MethodPost, Path: "/api/alpha/decisions", Headers: h, Body: fixtureDecisionBody}}
}

func vendorCases() []vendorCase {
	exchange := func(verifier string) map[string]any {
		return map[string]any{"code": fixtureCode, "code_verifier": verifier, "code_challenge_method": "S256"}
	}
	json := map[string]string{"content-type": "application/json"}
	return []vendorCase{
		decisionCase("decisions.200", "", fixtureKey),
		decisionCase("decisions.402_credits", "402_credits", fixtureKey),
		decisionCase("decisions.402_inflight", "402_inflight", fixtureKey),
		decisionCase("decisions.401", "", "Bearer sk-or-v1-e2e-invalid-key"),
		decisionCase("decisions.403_moderation", "403_moderation", fixtureKey),
		decisionCase("decisions.404_data_policy", "404_data_policy", fixtureKey),
		decisionCase("decisions.413", "413", fixtureKey),
		decisionCase("decisions.429", "429", fixtureKey),
		decisionCase("decisions.200_html", "html200", fixtureKey),
		{name: "auth_keys.200", issue: true, verifier: fixtureVerifier,
			req: vendorRequest{Method: http.MethodPost, Path: "/api/v1/auth/keys", Headers: json, Body: exchange(fixtureVerifier)}},
		{name: "auth_keys.400",
			req: vendorRequest{Method: http.MethodPost, Path: "/api/v1/auth/keys", Headers: json, Body: exchange(fixtureVerifier)}},
		{name: "auth_keys.403", issue: true, verifier: "a-verifier-that-does-not-match-the-challenge-0000",
			req: vendorRequest{Method: http.MethodPost, Path: "/api/v1/auth/keys", Headers: json, Body: exchange("a-verifier-that-does-not-match-the-challenge-0000")}},
		{name: "chat.200_usage_cost", req: vendorRequest{Method: http.MethodPost, Path: "/api/v1/chat/completions",
			Headers: map[string]string{"authorization": fixtureKey, "content-type": "application/json"},
			Body: map[string]any{
				"model":    "anthropic/claude-haiku-4.5",
				"messages": []any{map[string]any{"role": "user", "content": "Reply with the word OK."}},
				"usage":    map[string]any{"include": true},
			}}},
		{name: "key.200", req: vendorRequest{Method: http.MethodGet, Path: "/api/v1/key", Headers: map[string]string{"authorization": fixtureKey}}},
	}
}

var (
	issuedKey = regexp.MustCompile(`^sk-or-v1-mock-[0-9a-f]{32}$`)
	codeParam = regexp.MustCompile(`[?&]code=([^&]+)`)
)

// issueCode runs the mock /auth for fixtureChallenge and returns the code it redirects with.
func issueCode(t *testing.T, base string) string {
	t.Helper()
	q := url.Values{"callback_url": {"https://timkush1.github.io/TrustAgent/connect/callback/"},
		"code_challenge": {fixtureChallenge}, "code_challenge_method": {"S256"}}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(base + "/auth?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	m := codeParam.FindStringSubmatch(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || m == nil {
		t.Fatalf("/auth answered %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return m[1]
}

// answer sends c to the mock and returns the fixture it makes: the mock's status, body and
// content-type and retry-after headers, the observed CORS headers, and the request with its random
// parts (the issued code, the issued key, the created time) replaced by fixed ones.
func answer(t *testing.T, base string, c vendorCase) vendorFixture {
	t.Helper()
	var body io.Reader
	sent := c.req.Body
	if c.issue {
		b := map[string]any{}
		for k, v := range c.req.Body.(map[string]any) {
			b[k] = v
		}
		b["code"] = issueCode(t, base)
		sent = b
	}
	if sent != nil {
		raw, _ := json.Marshal(sent)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(c.req.Method, base+c.req.Path, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range c.req.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	headers := map[string]string{}
	for k, v := range observedCORS {
		headers[k] = v
	}
	for _, k := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(k); v != "" {
			headers[strings.ToLower(k)] = v
		}
	}
	var out any = string(raw)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: the mock's JSON does not parse: %v", c.name, err)
		}
	}
	if m, ok := out.(map[string]any); ok {
		if k, ok := m["key"].(string); ok {
			if !issuedKey.MatchString(k) {
				t.Errorf("%s: the issued key %q has not the mock's shape", c.name, k[:min(len(k), 14)])
			}
			m["key"] = "sk-or-v1-mock-00000000000000000000000000000000"
		}
		if _, ok := m["created"]; ok {
			m["created"] = 1760000000
		}
	}
	return vendorFixture{Source: vendorFixtureSource, Request: c.req, Status: resp.StatusCode, Headers: headers, Body: out}
}

func TestVendorFixtures(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	seen := map[string]bool{}
	for _, c := range vendorCases() {
		got := answer(t, srv.URL, c)
		path := filepath.Join(vendorFixtureDir, c.name+".json")
		seen[c.name+".json"] = true
		if *updateVendorFixtures {
			b, _ := json.MarshalIndent(got, "", "  ")
			if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b, err := os.ReadFile(path) // #nosec G304 -- a fixture path built from a constant directory and a case name
		if err != nil {
			t.Errorf("%s: %v (run with -update)", c.name, err)
			continue
		}
		var want, have any
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		hb, _ := json.Marshal(got)
		_ = json.Unmarshal(hb, &have)
		if !reflect.DeepEqual(want, have) {
			t.Errorf("%s differs from what the mock answers now (run with -update and review the diff):\nfile: %s\nmock: %s", c.name, b, hb)
		}
	}
	// Every fault fixture's status is faultTable's, and no stray file sits in the directory.
	for _, c := range vendorCases() {
		if f := c.req.Headers["x-mock-fault"]; f != "" && f != "html200" {
			if spec, ok := faultTable[f]; !ok || !strings.Contains(c.name, f) {
				t.Errorf("%s: fault %q is not a faultTable entry named in the file name", c.name, f)
			} else if b, err := os.ReadFile(filepath.Join(vendorFixtureDir, c.name+".json")); err == nil && !*updateVendorFixtures { // #nosec G304 -- see above
				var fx vendorFixture
				_ = json.Unmarshal(b, &fx)
				if fx.Status != spec.status {
					t.Errorf("%s: status %d, faultTable says %d", c.name, fx.Status, spec.status)
				}
			}
		}
	}
	entries, err := os.ReadDir(vendorFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	var stray []string
	for _, e := range entries {
		if !seen[e.Name()] {
			stray = append(stray, e.Name())
		}
	}
	sort.Strings(stray)
	if len(stray) > 0 {
		t.Errorf("files no case produces (remove them or add a case): %v", stray)
	}
}
