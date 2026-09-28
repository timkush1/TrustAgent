package jev

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LimitSourceInFlight is OpenRouter's metadata.limit_source of a transient 402: the key's
// in-flight budget, not the account balance.
const LimitSourceInFlight = "openrouter_in_flight_budget"

// breakerEffect is what an attempt outcome does to the circuit breaker.
type breakerEffect int

const (
	// effectNeutral: not a vendor fault (our own cancellation, a hedge loser, a request the
	// vendor refused on its merits); the breaker ignores it.
	effectNeutral breakerEffect = iota
	// effectSuccess closes the breaker and resets the fault count.
	effectSuccess
	// effectFault is a vendor fault counted toward BreakerFailures.
	effectFault
	// effectTrip opens the breaker at once for BreakerCooldown (200 text/html).
	effectTrip
	// effectAuth opens the breaker at once for AuthCooldown (401, 403, 402 without credits).
	effectAuth
)

// sizeMessage matches a 400 about size (SPEC §2.9: /token|context|too long|too large/i).
var sizeMessage = regexp.MustCompile(`(?i)token|context|too long|too large`)

// dataPolicyMessage matches OpenRouter's 404 when privacy settings exclude every provider.
var dataPolicyMessage = regexp.MustCompile(`(?i)no endpoints found|data policy`)

// classifyStatus turns a non-2xx vendor response into an *Error and its breaker effect (SPEC §3.1
// Jev error taxonomy). body may be any of the OpenRouter, TypeSafe or Vercel error envelopes.
func classifyStatus(status int, header http.Header, body []byte, retryAfterCap time.Duration) (*Error, breakerEffect) {
	env := parseEnvelope(body)
	e := &Error{Status: status, Message: env.message}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	effect := effectNeutral
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind, effect = KindAuth, effectAuth
	case status == http.StatusPaymentRequired:
		e.Kind = KindCredits
		ra, hasRA := retryAfter(header, retryAfterCap)
		if env.limitSource == LimitSourceInFlight && hasRA {
			// OpenRouter's in-flight budget: transient, retried after Retry-After.
			e.Retryable, e.RetryAfter, effect = true, ra, effectFault
		} else {
			effect = effectAuth
		}
	case status == http.StatusNotFound:
		if dataPolicyMessage.Match(body) {
			e.Kind = KindDataPolicy
		} else {
			e.Kind = KindNotFound
		}
	case status == http.StatusRequestEntityTooLarge:
		e.Kind = KindTooLarge
	case status == http.StatusBadRequest && sizeMessage.MatchString(env.message):
		e.Kind = KindTooLarge
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status <= 599:
		e.Kind, e.Retryable, effect = KindRetryable, true, effectFault
		e.RetryAfter, _ = retryAfter(header, retryAfterCap)
	case status >= 400 && status <= 499:
		e.Kind = KindBadRequest
	default:
		e.Kind, effect = KindBadResponse, effectFault
	}
	e.Hint = Hint(e.Kind)
	if e.Kind == KindCredits && e.Retryable {
		e.Hint = "OpenRouter's in-flight budget for this key was exceeded; TrustAgent retries after Retry-After."
	}
	return e, effect
}

// checkContentType accepts a JSON media type (application/json or application/*+json), or no
// Content-Type at all when the body is a JSON object. Anything else on a 2xx, typically the site's
// catch-all text/html page behind a wrong base URL, is bad_content_type and trips the breaker.
func checkContentType(header http.Header, body []byte) *Error {
	ct := header.Get("Content-Type")
	if ct == "" {
		if trimmed := strings.TrimSpace(string(body[:min(len(body), 64)])); strings.HasPrefix(trimmed, "{") {
			return nil
		}
		ct = "(none)"
	} else if mt, _, err := mime.ParseMediaType(ct); err == nil {
		if mt == "application/json" || strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json") {
			return nil
		}
	}
	msg := "expected JSON, got Content-Type " + ct
	if strings.Contains(strings.ToLower(ct), "html") {
		msg = "got a web page (" + ct + ") instead of JSON"
	}
	return &Error{Kind: KindBadContentType, Status: http.StatusOK, Message: msg, Hint: Hint(KindBadContentType)}
}

// retryAfter parses Retry-After (delta seconds or an HTTP date), capped at limit.
func retryAfter(h http.Header, limit time.Duration) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		d = time.Duration(secs * float64(time.Second))
	} else if t, err := http.ParseTime(v); err == nil {
		d = max(time.Until(t), 0)
	} else {
		return 0, false
	}
	if limit > 0 && d > limit {
		d = limit
	}
	return d, true
}

// envelope is what the error envelopes have in common.
type envelope struct {
	message     string
	errorType   string
	limitSource string
}

// parseEnvelope reads the vendor error envelopes:
//
//	OpenRouter: {"error":{"code":402,"message":"…","metadata":{"limit_source":"…"}}}
//	OpenAI-style: {"error":{"message":"…","type":"…"}} or {"error":"…"}
//	TypeSafe:   {"detail":[{"loc":[…],"msg":"…","type":"…"}]} (422), {"detail":{"error_type":"…","message":"…"}}
//	            or {"detail":"…"}
//	Vercel:     {"message":"…","error_type":"…"}
func parseEnvelope(body []byte) envelope {
	var raw struct {
		Error     json.RawMessage `json:"error"`
		Detail    json.RawMessage `json:"detail"`
		Message   string          `json:"message"`
		ErrorType string          `json:"error_type"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return envelope{message: scrub(firstLine(string(body)))}
	}
	env := envelope{message: raw.Message, errorType: raw.ErrorType}
	if len(raw.Error) > 0 {
		var obj struct {
			Message  string         `json:"message"`
			Type     string         `json:"type"`
			Metadata map[string]any `json:"metadata"`
		}
		var s string
		switch {
		case json.Unmarshal(raw.Error, &obj) == nil:
			env.message, env.errorType = firstNonEmpty(obj.Message, env.message), firstNonEmpty(obj.Type, env.errorType)
			if ls, ok := obj.Metadata["limit_source"].(string); ok {
				env.limitSource = ls
			}
			if raw, ok := obj.Metadata["raw"].(string); ok && env.message != "" && !strings.Contains(env.message, raw) {
				env.message += ": " + raw
			}
		case json.Unmarshal(raw.Error, &s) == nil:
			env.message = firstNonEmpty(s, env.message)
		}
	}
	if len(raw.Detail) > 0 {
		var list []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		}
		var obj struct {
			ErrorType string `json:"error_type"`
			Message   string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(raw.Detail, &list) == nil:
			parts := make([]string, 0, len(list))
			for _, d := range list {
				loc := make([]string, 0, len(d.Loc))
				for _, l := range d.Loc {
					loc = append(loc, strings.Trim(strings.TrimSpace(jsonText(l)), `"`))
				}
				p := d.Msg
				if len(loc) > 0 {
					p = strings.Join(loc, ".") + ": " + p
				}
				parts = append(parts, p)
			}
			env.message = firstNonEmpty(strings.Join(parts, "; "), env.message)
		case json.Unmarshal(raw.Detail, &obj) == nil:
			env.message, env.errorType = firstNonEmpty(obj.Message, env.message), firstNonEmpty(obj.ErrorType, env.errorType)
		case json.Unmarshal(raw.Detail, &s) == nil:
			env.message = firstNonEmpty(s, env.message)
		}
	}
	if env.message == "" && env.errorType != "" {
		env.message = env.errorType
	}
	env.message = scrub(env.message)
	return env
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// secretPattern masks credentials that a vendor might echo in a message (SPEC §7.5 redaction).
var secretPattern = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{8,}|Bearer\s+\S+|x-api-key:\s*\S+)`)

// scrub masks secrets and bounds the length of a vendor message.
func scrub(s string) string {
	s = secretPattern.ReplaceAllString(s, "[redacted]")
	const maxLen = 300
	if r := []rune(s); len(r) > maxLen {
		s = string(r[:maxLen]) + "…"
	}
	return s
}

// errAttemptTimeout marks an attempt that ran past TRUTHTABLE_JEV_ATTEMPT_TIMEOUT.
var errAttemptTimeout = errors.New("attempt timed out")

// Hint is the plain-language fix for an error kind (SPEC §6 errors and fixes).
func Hint(kind string) string {
	switch kind {
	case KindAuth:
		return "Key rejected (401/403): reconnect OpenRouter or paste a new key (OPENROUTER_API_KEY, TYPESAFE_API_KEY or TRUTHTABLE_JEV_API_KEY)."
	case KindCredits:
		return "No credits (402): your account is empty or the key's credit limit is reached. Add credits at openrouter.ai/settings/credits or raise the key's limit at openrouter.ai/settings/keys."
	case KindDataPolicy:
		return "check openrouter.ai/settings/privacy or unset TRUTHTABLE_JEV_ZDR"
	case KindNotFound:
		return "Endpoint or model not found (404): check TRUTHTABLE_JEV_MODEL, and TRUTHTABLE_JEV_BASE_URL / TRUTHTABLE_JEV_PATH if you set them."
	case KindTooLarge:
		return "The request is larger than the model accepts (32k tokens on OpenRouter): lower TRUTHTABLE_JEV_TOKEN_BUDGET or the route's evidence.max_tokens."
	case KindBadRequest:
		return "The decision model refused the request (400/422): check TRUTHTABLE_JEV_MODEL; TRUTHTABLE_JEV_LOG_PAYLOADS=true logs the request body."
	case KindRetryable:
		return "The provider is busy (429, 529 or 5xx): TrustAgent retries and pauses automatically; nothing to do."
	case KindTimeout:
		return "The decision model did not answer in time: the answer passes through unchecked when the route fails open. Raise deadline_ms or TRUTHTABLE_JEV_ATTEMPT_TIMEOUT if it keeps happening."
	case KindBadContentType:
		return "Got a web page instead of JSON: the base URL is wrong (OpenRouter URLs end in /api). Fix TRUTHTABLE_JEV_BASE_URL or leave it empty."
	case KindBadResponse:
		return "The decision model answered with a response TrustAgent could not read; TRUTHTABLE_JEV_LOG_PAYLOADS=true logs it."
	case KindBreakerOpen:
		return "Fast checks are paused after repeated errors; they resume automatically after the cooldown. See the last error for the cause."
	case KindRateLimited:
		return "The gateway's decision rate limit (TRUTHTABLE_GATE_JEV_RPM) is used up for now; answers pass unchecked until it refills."
	case KindOverload:
		return "Too many decision calls in flight (TRUTHTABLE_GATE_JEV_MAX_INFLIGHT); answers pass unchecked until some finish."
	case KindBudget:
		return "Today's fast-check budget is used up: raise TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY or the cap in Settings → Budgets."
	case KindCredentialsUnavailable:
		return "The stored decision-model credential cannot be read: re-enter it in Settings → Connections."
	case KindOff:
		return "Fast checks are off: set OPENROUTER_API_KEY (or TRUTHTABLE_JEV_BACKEND=mock for simulated checks)."
	case KindMockFixtureMissing:
		return "The mock has no scripted answer for this scenario and TRUTHTABLE_JEV_MOCK_STRICT forbids hash answers."
	}
	return ""
}
