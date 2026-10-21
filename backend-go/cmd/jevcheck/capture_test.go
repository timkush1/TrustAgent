package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/jev"
)

// The fake key of these tests; mock-upstream answers any key that does not say "invalid".
const captureTestKey = "sk-or-v1-capture-test-0123456789abcdef"

func runCaptureCmd(t *testing.T, env map[string]string, args ...string) (int, string) {
	t.Helper()
	return runCheck(t, env, args...)
}

// resultLine parses the TRUSTAGENT_P6_RESULT line, which must be the last line.
func resultLine(t *testing.T, out string) p6Result {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	const prefix = "TRUSTAGENT_P6_RESULT "
	if !strings.HasPrefix(last, prefix) {
		t.Fatalf("the last line is not the result line: %q", last)
	}
	var r p6Result
	if err := json.Unmarshal([]byte(strings.TrimPrefix(last, prefix)), &r); err != nil {
		t.Fatalf("result line: %v", err)
	}
	return r
}

func TestCaptureDryRunPlan(t *testing.T) {
	m, srv := mockServer(t)
	dir := filepath.Join(t.TempDir(), "captured")
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"no key", map[string]string{"TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL}, nil, "no key, plan only"},
		{"dry run", map[string]string{"TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL, "OPENROUTER_API_KEY": captureTestKey}, []string{"--dry-run"}, "OPENROUTER_API_KEY set (never shown)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runCaptureCmd(t, tc.env, append([]string{"--capture", dir}, tc.args...)...)
			if code != exitOK {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			contains(t, out, tc.want, "the key check (free)", "a bogus key (free)", "the decisions path", "the systemone path",
				"a ZDR request", "a Haiku chat with usage.include", "a 30,000-token probe", "GET /api/v1/key", "POST /api/alpha/decisions",
				"Total estimate $", "nothing was sent and nothing was spent", "make jev-capture CONFIRM=1",
				"not a vendor host: these fixtures are simulated")
			if strings.Contains(out, captureTestKey) {
				t.Error("the plan prints the key")
			}
			r := resultLine(t, out)
			if !r.DryRun || r.Step != "jev-capture" || r.CostUSD != 0 || r.CapUSD != defaultCaptureCapUSD || r.Label != "simulated" || r.Counts.N != 7 {
				t.Errorf("result line %+v", r)
			}
			if n := len(m.Requests()); n != 0 {
				t.Errorf("a plan sent %d requests", n)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a plan created the output directory (%v)", err)
			}
		})
	}
}

// The plan's estimate: the 30k probe dominates and the whole run fits the default cap.
func TestCaptureEstimate(t *testing.T) {
	c := &capturer{model: jev.DefaultOpenRouterModel}
	steps, err := c.steps()
	if err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, s := range steps {
		total += s.estimate
		if (s.stem == "key" || s.want == http.StatusUnauthorized) != (s.estimate == 0) {
			t.Errorf("%s: estimate %v (free calls only for the key check and the bogus key)", s.what, s.estimate)
		}
	}
	long := steps[len(steps)-1]
	if long.variant != "30k" || long.estimate < 0.001 || long.estimate > 0.003 {
		t.Errorf("the 30k probe's estimate is $%v, want about $0.0013 plus margin", long.estimate)
	}
	if total >= 0.005 || total >= defaultCaptureCapUSD {
		t.Errorf("total estimate $%v, want under $0.005 (D211)", total)
	}
}

func TestCaptureAgainstMockUpstream(t *testing.T) {
	m, srv := mockServer(t)
	dir := filepath.Join(t.TempDir(), "captured")
	code, out := runCaptureCmd(t, map[string]string{"TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL, "OPENROUTER_API_KEY": captureTestKey}, "--capture", dir)
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, captureTestKey) || strings.Contains(out, "sk-or-v1-c…def") {
		t.Errorf("the output prints the key or its label:\n%s", out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	sort.Strings(names)
	want := []string{"chat.200_usage_cost.json", "decisions.200.json", "decisions.200_30k.json", "decisions.200_zdr.json",
		"decisions.401.json", "key.200.json", "systemone.200.json"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("captured %v, want %v", names, want)
	}
	r := resultLine(t, out)
	if r.DryRun || r.Exit != 0 || r.Counts != (p6Counts{N: 7, Scored: 7}) || r.CostUSD <= 0 || r.CostUSD > defaultCaptureCapUSD {
		t.Errorf("result line %+v", r)
	}
	if r.Snapshot == nil || *r.Snapshot == "" {
		t.Error("the result line names no snapshot")
	}
	if r.KeyUsageUSD == nil {
		t.Error("the result line has no key usage (the free key check reads it)")
	}
	// Every paid call is a single attempt: retries are off.
	if n := decisionCalls(m); n != 5 {
		t.Errorf("%d decision calls, want 5 (bogus, decisions, systemone, zdr, 30k)", n)
	}

	secret := regexp.MustCompile(`sk-or-v1-(capture|invalid)|gen-[A-Za-z0-9]{6,}`)
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- a file this test wrote
		if err != nil {
			t.Fatal(err)
		}
		if loc := secret.Find(b); loc != nil {
			t.Errorf("%s holds %q", filepath.Base(f), loc)
		}
		var fx fixture
		if err := json.Unmarshal(b, &fx); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		// mock-upstream is not a vendor host: its fixtures say simulated, never captured.
		if !fx.Sanitized || !regexp.MustCompile(`^simulated \d{4}-\d{2}-\d{2} via jevcheck --capture against 127\.0\.0\.1 \(not a vendor host\)$`).MatchString(fx.Source) {
			t.Errorf("%s: source %q sanitized %v", filepath.Base(f), fx.Source, fx.Sanitized)
		}
		if a := fx.Request.Headers["authorization"]; a != "Bearer "+MaskedKey {
			t.Errorf("%s: authorization %q", filepath.Base(f), a)
		}
		if !contains1(fx.Placeholders, "request.headers.authorization") {
			t.Errorf("%s: placeholders %v lack the authorization header", filepath.Base(f), fx.Placeholders)
		}
	}

	// The key check: the label masked, the amounts replaced, and both listed.
	var key struct {
		Placeholders []string `json:"placeholders"`
		Body         struct {
			Data map[string]any `json:"data"`
		} `json:"body"`
	}
	readJSON(t, filepath.Join(dir, "key.200.json"), &key)
	if key.Body.Data["label"] != MaskedLabel || key.Body.Data["usage"] != 0.0 {
		t.Errorf("key body %v", key.Body.Data)
	}
	for _, p := range []string{"body.data.label", "body.data.usage"} {
		if !contains1(key.Placeholders, p) {
			t.Errorf("key placeholders %v lack %s", key.Placeholders, p)
		}
	}
	// The long probe's filler is elided in the fixture.
	b, _ := os.ReadFile(filepath.Join(dir, "decisions.200_30k.json"))
	if !regexp.MustCompile(`<elided: \d{6} chars>`).Match(b) || len(b) > 8_000 {
		t.Errorf("the 30k fixture keeps its filler (%d bytes)", len(b))
	}
	// The ZDR request carried provider.zdr.
	var zdr fixture
	readJSON(t, filepath.Join(dir, "decisions.200_zdr.json"), &zdr)
	var zdrBody struct {
		Provider struct {
			ZDR bool `json:"zdr"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(zdr.Request.Body, &zdrBody); err != nil || !zdrBody.Provider.ZDR {
		t.Errorf("the ZDR request body is %s (%v)", zdr.Request.Body, err)
	}
	if bytes.Contains(b, []byte(`\u003c`)) {
		t.Error("the fixture escapes < as \\u003c")
	}

	for _, f := range files {
		if err := decodeCaptured(f); err != nil {
			t.Errorf("%s does not decode with the production decoders: %v", filepath.Base(f), err)
		}
	}
}

// Key order and number literals survive sanitizing (body parity, rounding).
func TestSanitizeJSONKeepsOrderAndLiterals(t *testing.T) {
	raw := []byte(`{"zeta":1,"answers":{"q":{"noul":0.490,"confidence":1e-3}},"id":"gen-abc123def456","alpha":"sk-or-v1-0123456789abcdef","note":"` + strings.Repeat("x", elideAbove+1) + `"}`)
	got, ph, err := sanitizeJSON(raw, "body", false)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"zeta":1,"answers":{"q":{"noul":0.490,"confidence":1e-3}},"id":"gen-<redacted>","alpha":"` + MaskedKey + `","note":"<elided: 2001 chars>"}`
	if string(got) != want {
		t.Errorf("sanitized\n%s\nwant\n%s", got, want)
	}
	sort.Strings(ph)
	if strings.Join(ph, " ") != "body.alpha body.id body.note" {
		t.Errorf("placeholders %v", ph)
	}
	if _, _, err := sanitizeJSON([]byte(`{"a":1} {"b":2}`), "body", false); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestFixtureSource(t *testing.T) {
	if got := fixtureSource("openrouter.ai", "2026-10-11"); got != "captured 2026-10-11 via jevcheck --capture" {
		t.Errorf("vendor: %q", got)
	}
	if got := fixtureSource("mock-upstream", "2026-10-11"); !strings.HasPrefix(got, "simulated ") || strings.Contains(got, "captured") {
		t.Errorf("mock-upstream: %q", got)
	}
}

func TestSecretIn(t *testing.T) {
	for in, want := range map[string]string{
		`{"k":"Bearer ` + MaskedKey + `"}`:   "",
		`{"k":"sk-or-v1-0123456789abcdef"}`:  "an sk- key",
		`{"k":"gen-1760000000-AbCdEf"}`:      "a generation id",
		`{"k":"Bearer abcdefghijklmnopqrs"}`: "a bearer token",
		`{"k":"my-plain-key-value"}`:         "the key",
	} {
		if got := secretIn([]byte(in), "my-plain-key-value"); got != want {
			t.Errorf("secretIn(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestCaptureBudgetRefusal(t *testing.T) {
	m, srv := mockServer(t)
	dir := filepath.Join(t.TempDir(), "captured")
	code, out := runCaptureCmd(t, map[string]string{"TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL, "OPENROUTER_API_KEY": captureTestKey},
		"--capture", dir, "--max-cost-usd", "0.0000001")
	if code != exitFailed {
		t.Fatalf("exit %d, want %d:\n%s", code, exitFailed, out)
	}
	contains(t, out, "refused: the estimate $", "not sent (the budget refused an earlier step)", "The estimate exceeds the cap")
	// The free calls ran; no paid call was sent.
	if n := decisionCalls(m); n != 1 {
		t.Errorf("%d decision calls, want only the free bogus-key call", n)
	}
	r := resultLine(t, out)
	if r.Exit != exitFailed || r.Counts.Failed != 5 || r.CostUSD != 0 {
		t.Errorf("result line %+v", r)
	}
}

// The key's own limit also bounds the capture: what is left is the smaller of the cap and the
// key's limit_remaining.
func TestCaptureRespectsTheKeyLimit(t *testing.T) {
	m, srv := mockServer(t)
	dir := filepath.Join(t.TempDir(), "captured")
	// mock-upstream gives a "nocredits" key limit 1, usage 1, limit_remaining 0.
	code, out := runCaptureCmd(t, map[string]string{"TRUTHTABLE_OPENROUTER_BASE_URL": srv.URL, "OPENROUTER_API_KEY": "sk-or-v1-nocredits-0123456789"},
		"--capture", dir)
	if code != exitFailed {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "refused: the estimate $", "exceeds the $0 left")
	if n := decisionCalls(m); n != 1 {
		t.Errorf("%d decision calls, want only the bogus-key call", n)
	}
	r := resultLine(t, out)
	if r.KeyLimitRemainingUSD == nil || *r.KeyLimitRemainingUSD != 0 {
		t.Errorf("key_limit_remaining_usd %v", r.KeyLimitRemainingUSD)
	}
}

func TestCaptureUsage(t *testing.T) {
	for _, args := range [][]string{
		{"--capture", "x", "--gateway", "http://127.0.0.1:1", "--admin-key-file", "k"},
		{"--capture", "x", "-json"},
		{"--capture", "x", "--max-cost-usd", "0"},
		{"--capture", "x", "--max-cost-usd", "2"},
		{"--dry-run"},
		{"--max-cost-usd", "0.01"},
	} {
		if code, out := runCheck(t, nil, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d:\n%s", args, code, exitUsage, out)
		}
	}
}

// A capture with a non-local http base URL is refused before anything is sent.
func TestCaptureRefusesPlainHTTPVendor(t *testing.T) {
	code, out := runCheck(t, map[string]string{"TRUTHTABLE_OPENROUTER_BASE_URL": "http://openrouter.example.com"}, "--capture", t.TempDir())
	if code != exitUsage || !strings.Contains(out, "not an https URL") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// The committed captures (contracts/fixtures/vendor/captured, written at P6b with the owner's key)
// decode with the production decoders. Before the capture the directory does not exist.
func TestCommittedCapturesDecode(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "contracts", "fixtures", "vendor", "captured")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		if _, err := os.Stat(dir); err == nil {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.Name() != ".gitkeep" {
					t.Errorf("%s holds %s but no fixture", dir, e.Name())
				}
			}
		}
		t.Logf("no committed captures in %s yet (they come from make jev-capture CONFIRM=1 at P6b)", dir)
		return
	}
	stems := map[string]bool{}
	for _, f := range files {
		var fx fixture
		readJSON(t, f, &fx)
		if !strings.HasPrefix(fx.Source, "captured ") {
			t.Errorf("%s: source %q: a committed capture comes from a vendor host", filepath.Base(f), fx.Source)
		}
		stems[strings.SplitN(filepath.Base(f), ".", 2)[0]] = true
		if err := decodeCaptured(f); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
	for _, s := range []string{"key", "decisions", "systemone", "chat"} {
		if !stems[s] {
			t.Errorf("the committed captures have no %s fixture", s)
		}
	}
}

// decodeCaptured replays a fixture's response through the production decoders: the decision
// client (decisions, systemone), its key check (key) and llmapi (chat). A 401 must classify as auth.
func decodeCaptured(path string) error {
	b, err := os.ReadFile(path) // #nosec G304 -- a fixture path from the test
	if err != nil {
		return err
	}
	var fx fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		return err
	}
	if !fx.Sanitized || !(strings.HasPrefix(fx.Source, "captured ") || strings.HasPrefix(fx.Source, "simulated ")) {
		return errors.New("not a sanitized capture")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range fx.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(fx.Status)
		_, _ = w.Write(fx.Body)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stem := strings.SplitN(filepath.Base(path), ".", 2)[0]
	backend := jev.BackendOpenRouterDecisions
	if stem == "systemone" {
		backend = jev.BackendOpenRouterSystemOne
	}
	h := jev.DefaultHTTPConfig()
	h.KeepAlive, h.MaxRetries = 0, 0
	cl, err := jev.New(jev.Config{Backend: backend, OpenRouterKey: captureTestKey, OpenRouterBaseURL: srv.URL, HTTP: &h})
	if err != nil {
		return err
	}
	switch {
	case stem == "chat":
		_, err := chatCost(fx.Body)
		return err
	case stem == "key":
		ki, err := cl.(jev.KeyChecker).CheckKey(ctx)
		if err == nil && ki.Label != MaskedLabel {
			return errors.New("the key label is not masked")
		}
		return err
	case fx.Status == http.StatusUnauthorized:
		_, err := jev.Probe(ctx, cl)
		if jev.ErrorKind(err) != jev.KindAuth {
			return errors.New("a 401 does not classify as auth: " + errText(err))
		}
		return nil
	case fx.Status/100 == 2:
		resp, err := jev.Probe(ctx, cl)
		if err == nil && resp.Snapshot == "" {
			return errors.New("the decision names no snapshot")
		}
		return err
	}
	return nil
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a file this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func contains1(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var _ = mockupstream.Config{}
