package jev

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClassifyStatus(t *testing.T) {
	ra := func(v string) http.Header { h := http.Header{}; h.Set("Retry-After", v); return h }
	cases := []struct {
		name      string
		status    int
		header    http.Header
		body      string
		kind      string
		effect    breakerEffect
		retryable bool
		after     time.Duration
		message   string
	}{
		{"401 openrouter", 401, nil, `{"error":{"code":401,"message":"No auth credentials found"}}`, KindAuth, effectAuth, false, 0, "No auth credentials found"},
		{"403 typesafe detail object", 403, nil, `{"detail":{"error_type":"forbidden","message":"Key disabled"}}`, KindAuth, effectAuth, false, 0, "Key disabled"},
		{"403 moderation judges the input, not the key", 403, nil,
			`{"error":{"code":403,"message":"Your input was flagged","metadata":{"reasons":["harassment"],"flagged_input":"…","provider_name":"X","model_slug":"m"}}}`,
			KindBadRequest, effectNeutral, false, 0, "moderation flagged the input: Your input was flagged"},
		{"402 credits", 402, nil, `{"error":{"code":402,"message":"Insufficient credits"}}`, KindCredits, effectAuth, false, 0, "Insufficient credits"},
		{"402 in-flight without Retry-After stays credits", 402, nil,
			`{"error":{"code":402,"message":"budget","metadata":{"limit_source":"openrouter_in_flight_budget"}}}`, KindCredits, effectAuth, false, 0, "budget"},
		{"402 in-flight is transient", 402, ra("2"),
			`{"error":{"code":402,"message":"budget","metadata":{"limit_source":"openrouter_in_flight_budget"}}}`, KindCredits, effectFault, true, 2 * time.Second, "budget"},
		{"402 other limit source with Retry-After", 402, ra("2"),
			`{"error":{"code":402,"message":"limit","metadata":{"limit_source":"key_limit"}}}`, KindCredits, effectAuth, false, 0, "limit"},
		{"404 data policy", 404, nil, `{"error":{"code":404,"message":"No endpoints found matching your data policy"}}`, KindDataPolicy, effectNeutral, false, 0, "No endpoints found"},
		{"404 other", 404, nil, `{"error":{"code":404,"message":"Model not found"}}`, KindNotFound, effectNeutral, false, 0, "Model not found"},
		{"404 html page", 404, nil, `<!DOCTYPE html><html>not here</html>`, KindNotFound, effectNeutral, false, 0, "<!DOCTYPE html>"},
		{"413", 413, nil, `{"error":{"code":413,"message":"too big"}}`, KindTooLarge, effectNeutral, false, 0, "too big"},
		{"400 about context", 400, nil, `{"error":{"code":400,"message":"exceeds the maximum context length"}}`, KindTooLarge, effectNeutral, false, 0, "context"},
		{"400 about tokens", 400, nil, `{"message":"Too many tokens","error_type":"invalid_request"}`, KindTooLarge, effectNeutral, false, 0, "Too many tokens"},
		{"400 other", 400, nil, `{"error":{"code":400,"message":"questions.q.type is invalid"}}`, KindBadRequest, effectNeutral, false, 0, "questions.q.type"},
		{"422 typesafe list", 422, nil, `{"detail":[{"loc":["body","questions","q1"],"msg":"field required","type":"value_error"}]}`,
			KindBadRequest, effectNeutral, false, 0, "body.questions.q1: field required"},
		{"422 vercel", 422, nil, `{"message":"Unsupported model","error_type":"invalid_request_error"}`, KindBadRequest, effectNeutral, false, 0, "Unsupported model"},
		{"408", 408, nil, ``, KindRetryable, effectFault, true, 0, "Request Timeout"},
		{"429 with Retry-After", 429, ra("1"), `{"error":{"code":429,"message":"Rate limited"}}`, KindRetryable, effectFault, true, time.Second, "Rate limited"},
		{"429 Retry-After capped", 429, ra("120"), `{}`, KindRetryable, effectFault, true, 10 * time.Second, "Too Many Requests"},
		{"500 openai style", 500, nil, `{"error":{"message":"boom","type":"server_error"}}`, KindRetryable, effectFault, true, 0, "boom"},
		{"502 string error", 502, nil, `{"error":"upstream failed"}`, KindRetryable, effectFault, true, 0, "upstream failed"},
		{"503 detail string", 503, nil, `{"detail":"maintenance"}`, KindRetryable, effectFault, true, 0, "maintenance"},
		{"524", 524, nil, `error code: 524`, KindRetryable, effectFault, true, 0, "error code: 524"},
		{"529 overloaded", 529, nil, `{"error":{"code":529,"message":"Overloaded"}}`, KindRetryable, effectFault, true, 0, "Overloaded"},
		{"odd status", 600, nil, ``, KindBadResponse, effectFault, false, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.header
			if h == nil {
				h = http.Header{}
			}
			e, effect := classifyStatus(c.status, h, []byte(c.body), 10*time.Second)
			if e.Kind != c.kind || effect != c.effect || e.Retryable != c.retryable || e.RetryAfter != c.after || e.Status != c.status {
				t.Errorf("got kind %s effect %d retryable %v after %s status %d, want %s %d %v %s %d", e.Kind, effect, e.Retryable, e.RetryAfter, e.Status, c.kind, c.effect, c.retryable, c.after, c.status)
			}
			if !strings.Contains(e.Message, c.message) {
				t.Errorf("message %q, want it to contain %q", e.Message, c.message)
			}
			if e.Hint == "" && c.kind != KindBadResponse {
				t.Error("every classified error carries a hint")
			}
		})
	}
	e, _ := classifyStatus(404, http.Header{}, []byte(`{"error":{"message":"No endpoints found"}}`), time.Second)
	if e.Hint != "check openrouter.ai/settings/privacy or unset TRUTHTABLE_JEV_ZDR" {
		t.Errorf("data_policy hint %q", e.Hint)
	}
}

func TestCheckContentType(t *testing.T) {
	cases := []struct {
		ct, body string
		ok       bool
		msg      string
	}{
		{"application/json", `{}`, true, ""},
		{"application/json; charset=utf-8", `{}`, true, ""},
		{"Application/JSON", `{}`, true, ""},
		{"application/problem+json", `{}`, true, ""},
		{"", ` {"answers":{}}`, true, ""},
		{"", `<html>`, false, "(none)"},
		{"text/html; charset=utf-8", `<!DOCTYPE html>`, false, "got a web page"},
		{"text/plain", `{}`, false, "expected JSON"},
		{"application/jsonp", `{}`, false, "expected JSON"},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.ct != "" {
			h.Set("Content-Type", c.ct)
		}
		e := checkContentType(h, []byte(c.body))
		if (e == nil) != c.ok {
			t.Errorf("%q: error %v, want ok=%v", c.ct, e, c.ok)
			continue
		}
		if e != nil && (e.Kind != KindBadContentType || !strings.Contains(e.Message, c.msg) || !strings.Contains(e.Hint, "OpenRouter URLs end in /api")) {
			t.Errorf("%q: %+v", c.ct, e)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	if _, ok := retryAfter(h, time.Second); ok {
		t.Error("absent")
	}
	for v, want := range map[string]time.Duration{"0": 0, "1.5": 1500 * time.Millisecond, "30": 10 * time.Second} {
		h.Set("Retry-After", v)
		if d, ok := retryAfter(h, 10*time.Second); !ok || d != want {
			t.Errorf("%s: %s %v, want %s", v, d, ok, want)
		}
	}
	h.Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
	if d, ok := retryAfter(h, 10*time.Second); !ok || d <= 0 || d > 3*time.Second {
		t.Errorf("date: %s %v", d, ok)
	}
	for _, bad := range []string{"-1", "soon"} {
		h.Set("Retry-After", bad)
		if _, ok := retryAfter(h, time.Second); ok {
			t.Errorf("%q must be ignored", bad)
		}
	}
}

func TestScrubAndEnvelopeRedaction(t *testing.T) {
	e, _ := classifyStatus(401, http.Header{}, []byte(`{"error":{"message":"bad key sk-or-v1-abcdef0123456789 for Bearer abc.def"}}`), time.Second)
	if strings.Contains(e.Message, "abcdef0123456789") || strings.Contains(e.Message, "abc.def") {
		t.Errorf("a key leaked into the message: %q", e.Message)
	}
	long := strings.Repeat("x", 1000)
	if got := scrub(long); len([]rune(got)) != 301 {
		t.Errorf("scrub keeps %d runes", len([]rune(got)))
	}
	if env := parseEnvelope([]byte(`{"error":{"message":"Provider returned error","metadata":{"raw":"upstream said no"}}}`)); env.message != "Provider returned error: upstream said no" {
		t.Errorf("metadata.raw: %q", env.message)
	}
}

func TestHints(t *testing.T) {
	for _, k := range []string{KindAuth, KindCredits, KindDataPolicy, KindNotFound, KindTooLarge, KindBadRequest, KindRetryable, KindTimeout,
		KindBadContentType, KindBadResponse, KindBreakerOpen, KindRateLimited, KindOverload, KindBudget, KindCredentialsUnavailable, KindOff, KindMockFixtureMissing} {
		if Hint(k) == "" {
			t.Errorf("%s has no hint", k)
		}
	}
	if Hint("nope") != "" {
		t.Error("unknown kinds have no hint")
	}
}

func TestDecodeResponseErrorEnvelope(t *testing.T) {
	if _, e := decodeResponse([]byte(`{"error":{"code":502,"message":"Provider returned error"}}`)); e == nil || e.Kind != KindBadResponse ||
		e.Message != "the response has no answers object: Provider returned error" {
		t.Errorf("a 2xx error envelope: %+v", e)
	}
	if _, e := decodeResponse([]byte(`{"id":"d","model":"m"}`)); e == nil || e.Message != "the response has no answers object" {
		t.Errorf("no answers: %+v", e)
	}
	if got := redactSecret("key ts-live-12345678 rejected", "ts-live-12345678"); got != "key [redacted] rejected" {
		t.Errorf("redactSecret %q", got)
	}
	if got := redactSecret("a k b", "k"); got != "a k b" {
		t.Errorf("a short value is no key: %q", got)
	}
}
