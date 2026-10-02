package credentials

// verify.go checks a credential with its provider before it is stored or bound (P4-PLAN D40):
// PUT /api/credentials/:name (#55), POST /api/credentials/:name/verify (#57) and the key from
// Connect OpenRouter (#59) all call Verifier.Verify. It belongs to GCONN; the rest of the package
// (the cipher, the origins, the store state) to GSEC.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
)

// Verification statuses (api.ts CredentialVerifyStatus, D40).
const (
	VerifyOK           = "ok"
	VerifyUnauthorized = "unauthorized"
	VerifyNoCredits    = "no_credits"
	VerifyUnreachable  = "unreachable"
	VerifyUnknown      = "unknown"
)

// Verification hints (CredentialVerification.hint): codes the web renders words for. A failed
// verification whose status alone does not explain it carries the decision-model error kind
// (jev.Kind*) instead, e.g. not_found for a model the gateway does not serve.
const (
	// HintNoCredits: the key works, but the account or the key's limit has no credits left (402).
	HintNoCredits = "no_credits"
	// HintNoLimit: an OpenRouter key that works and has no spending limit (copy step_connect.no_limit).
	HintNoLimit = "no_limit"
	// HintNotChecked: nothing was sent (upstream-* keys, or a configuration that cannot be probed).
	HintNotChecked = "not_checked"
	// HintProbeSkipped: OpenRouter accepted the key, but the probe was not sent because the
	// gateway's own decision budget, limiter or breaker refused it, so credits are unproven.
	HintProbeSkipped = "probe_skipped"
)

// MaxVerifyMessage is the longest error text a verification carries, in characters (D40).
const MaxVerifyMessage = 300

// Vendor API origins of the engine-side credentials (D40: GET /v1/models).
const (
	anthropicAPIBase = "https://api.anthropic.com"
	openAIAPIBase    = "https://api.openai.com"
	anthropicVersion = "2023-06-01"
)

// errVerifyOriginRefused is the candidate KeyFunc's refusal of an origin outside the credential's
// allowed origins (D78).
var errVerifyOriginRefused = errors.New("the credential is not allowed to be sent to this origin")

// VerifyTarget is the credential to check: the candidate secret with the origins it may be sent
// to. AllowedOrigins comes from the credential's spec (AllowedOrigins / VendorOrigins); every
// call the verifier makes asserts its origin against it.
type VerifyTarget struct {
	// Name is credentials.name (openrouter, typesafe, anthropic, openai, compatible,
	// upstream-*); it picks the check.
	Name   string
	Secret string
	// AllowedOrigins are the canonical origins the secret may reach.
	AllowedOrigins []string
	// BaseURL and Path are the compatible gateway's base URL and decision path ("" = default).
	BaseURL, Path string
	// Model is the decision model the probe asks ("" = the backend default; compatible needs
	// one).
	Model string
}

// Verification is the outcome of a check (api.ts CredentialVerification, plus what the decision
// model test and the logs need). It never carries the secret.
type Verification struct {
	OK     bool
	Status string
	// Hint is one of the Hint* codes, a decision-model error kind, or "".
	Hint string
	// Checked is false when no call was made (upstream-* keys, a configuration that cannot be
	// probed).
	Checked bool
	// Kind and HTTPStatus describe a failure: the decision-model error kind (jev.Kind*) and the
	// vendor's HTTP status (0 when there was none).
	Kind       string
	HTTPStatus int
	// Message is the failure text, redacted and at most MaxVerifyMessage characters.
	Message string
	// Latency is the wall time of the whole check.
	Latency time.Duration
	// Backend, EndpointHost, Model and Snapshot describe the probe: the decision backend it
	// used, the endpoint's host, the model asked and the snapshot that answered.
	Backend, EndpointHost, Model, Snapshot string
	InputTokens                            *int
	// CostUSD and CostSource are the probe's cost (nil and "" without a probe answer).
	CostUSD    *float64
	CostSource string
	// KeyLabel, LimitUSD and LimitRemainingUSD come from OpenRouter's GET /api/v1/key.
	KeyLabel                    *string
	LimitUSD, LimitRemainingUSD *float64
}

// Verifier checks credentials with their providers (D40). The zero value works against the real
// vendors; tests point the base URLs at mock-upstream.
type Verifier struct {
	// HTTP configures the throwaway decision-model clients (nil: jev.DefaultHTTPConfig). Keep-warm
	// is always off for them.
	HTTP *jev.HTTPConfig
	// OpenRouterBaseURL is TRUTHTABLE_OPENROUTER_BASE_URL ("" = https://openrouter.ai).
	OpenRouterBaseURL string
	// TypeSafeBaseURL, AnthropicBaseURL and OpenAIBaseURL replace the vendor APIs (tests); ""
	// uses the vendor.
	TypeSafeBaseURL, AnthropicBaseURL, OpenAIBaseURL string
	// Costs books the probe's cost (component probe, D82), Budget admits it against the daily
	// decision cap as a non-enforcing call, and Prices prices it; nil discards, keeps a private
	// budget and uses the embedded table.
	Costs  jev.CostSink
	Budget jev.Budget
	Prices jev.PriceFunc
	// Redact is the process redactor (D42); the candidate secret is masked in any case.
	Redact func(string) string
	// Timeout bounds one whole check (default 15 s).
	Timeout time.Duration
	// Transport carries the GET /v1/models checks (nil: jev.NewTransport()).
	Transport http.RoundTripper
	Logger    *slog.Logger
}

// Verify checks t with its provider (D40):
//   - openrouter: GET /api/v1/key (label, limit, usage), then the connectivity probe;
//   - typesafe and compatible: the probe;
//   - anthropic and openai: GET /v1/models;
//   - upstream-*: no call (status unknown).
//
// 401/403 → unauthorized, 402 → no_credits, network errors, 5xx and timeouts → unreachable; any
// other failure is unknown with the error kind as its hint. The probe is a non-enforcing call
// (component probe), so it can never spend the enforcing reserve (D82).
func (v *Verifier) Verify(ctx context.Context, t VerifyTarget) Verification {
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	var out Verification
	switch {
	case t.Name == "openrouter":
		out = v.probe(ctx, t, jev.BackendOpenRouterDecisions, true)
	case t.Name == "typesafe":
		out = v.probe(ctx, t, jev.BackendTypeSafe, false)
	case t.Name == "compatible":
		out = v.probe(ctx, t, jev.BackendCompatible, false)
	case t.Name == "anthropic":
		out = v.listModels(ctx, t, defaultIfBlank(v.AnthropicBaseURL, anthropicAPIBase), true)
	case t.Name == "openai":
		out = v.listModels(ctx, t, defaultIfBlank(v.OpenAIBaseURL, openAIAPIBase), false)
	case strings.HasPrefix(t.Name, "upstream-"):
		// An upstream key is the client's own LLM key: checking it would need a paid call to an
		// arbitrary gateway, so it is stored unchecked.
		return Verification{Status: VerifyUnknown, Hint: HintNotChecked}
	default:
		return Verification{Status: VerifyUnknown, Hint: HintNotChecked, Message: fmt.Sprintf("%q is not a credential name", t.Name)}
	}
	out.Latency = time.Since(start)
	out.Message = v.clean(out.Message, t.Secret)
	return out
}

// probe checks a decision-model credential through a throwaway client bound to the candidate:
// the key check first on OpenRouter, then jev.Probe.
func (v *Verifier) probe(ctx context.Context, t VerifyTarget, backend string, openRouter bool) Verification {
	h := jev.DefaultHTTPConfig()
	if v.HTTP != nil {
		h = *v.HTTP
	}
	h.KeepAlive = 0
	cfg := jev.Config{
		// An explicit backend with no env key resolves to the binding, so the env credentials
		// can never stand in for the candidate.
		Backend: backend, Model: t.Model, OpenRouterBaseURL: v.OpenRouterBaseURL,
		Binding: &jev.Binding{Backend: backend, Name: t.Name, BaseURL: t.BaseURL, Key: candidateKey(t.Secret, t.AllowedOrigins)},
		HTTP:    &h, Costs: v.Costs, Budget: v.Budget, Prices: v.Prices, Logger: v.logger(),
	}
	switch backend {
	case jev.BackendTypeSafe:
		cfg.BaseURL = v.TypeSafeBaseURL
	case jev.BackendCompatible:
		cfg.BaseURL, cfg.Path = t.BaseURL, t.Path
	}
	c, err := jev.New(cfg)
	if err != nil {
		return Verification{Status: VerifyUnknown, Hint: HintNotChecked, Message: "the check could not be set up: " + err.Error()}
	}
	defer func() {
		if cl, ok := c.(io.Closer); ok {
			_ = cl.Close()
		}
	}()
	info := c.Info()
	out := Verification{Backend: info.Backend, Model: info.Model, EndpointHost: hostOfURL(info.Endpoint)}
	if info.Backend == jev.BackendOff {
		out.Status, out.Hint, out.Kind, out.Message = VerifyUnknown, HintNotChecked, jev.KindOff, info.Reason
		return out
	}
	keyOK := false
	if openRouter {
		if kc, ok := c.(jev.KeyChecker); ok {
			ki, err := kc.CheckKey(ctx)
			switch {
			case err == nil:
				keyOK = true
				out.KeyLabel, out.LimitUSD, out.LimitRemainingUSD = keyInfo(ki)
			case !errors.Is(err, jev.ErrKeyCheckUnsupported):
				_, local := jev.SkipReason(err) // a refused origin: nothing was sent
				out.Checked = !local
				return failedVerification(out, err)
			}
		}
	}
	resp, err := jev.Probe(ctx, c)
	// A probe the gateway held back itself (budget, limiter, breaker, a refused origin) sent
	// nothing; the key check, when it ran, did.
	_, local := jev.SkipReason(err)
	out.Checked = keyOK || !local
	if resp != nil {
		out.Snapshot, out.InputTokens, out.CostUSD, out.CostSource = resp.Snapshot, resp.InputTokens, resp.CostUSD, resp.CostSource
	}
	if err != nil {
		if local && keyOK {
			// OpenRouter proved the key; only our own budget, limiter or breaker held the probe.
			out.OK, out.Status, out.Hint = true, VerifyOK, HintProbeSkipped
			out.Kind, out.Message = jev.ErrorKind(err), jevMessage(err)
			return out
		}
		return failedVerification(out, err)
	}
	out.OK, out.Status = true, VerifyOK
	if openRouter && keyOK && out.LimitUSD == nil {
		out.Hint = HintNoLimit
	}
	return out
}

// failedVerification fills out from a decision-model error.
func failedVerification(out Verification, err error) Verification {
	var je *jev.Error
	if errors.As(err, &je) {
		out.HTTPStatus = je.Status
	}
	out.Kind = jev.ErrorKind(err)
	out.Status = statusOfKind(out.Kind)
	out.Message = jevMessage(err)
	out.Hint = hintFor(out.Status, out.Kind)
	return out
}

// hintFor is the hint of a failed check: no_credits for a 402, none for a rejected key (the
// status says it all), else the error kind.
func hintFor(status, kind string) string {
	switch status {
	case VerifyNoCredits:
		return HintNoCredits
	case VerifyUnauthorized:
		return ""
	}
	return kind
}

// statusOfKind maps a decision-model error kind to a verification status (D40;
// jev/classify.go): 401/403 → unauthorized, 402 → no_credits, network, 5xx and timeouts →
// unreachable.
func statusOfKind(kind string) string {
	switch kind {
	case jev.KindAuth:
		return VerifyUnauthorized
	case jev.KindCredits:
		return VerifyNoCredits
	case jev.KindRetryable, jev.KindTimeout:
		return VerifyUnreachable
	}
	return VerifyUnknown
}

// listModels checks an engine-side LLM key with GET <base>/v1/models (D40): free, and it proves
// the key without a completion.
func (v *Verifier) listModels(ctx context.Context, t VerifyTarget, base string, anthropic bool) Verification {
	out := Verification{Checked: true}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/v1/models")
	if err != nil || u.Host == "" {
		out.Status, out.Kind, out.Hint, out.Checked = VerifyUnknown, jev.KindBadRequest, HintNotChecked, false
		out.Message = "the vendor URL is not valid"
		return out
	}
	out.EndpointHost = u.Hostname()
	key, err := candidateKey(t.Secret, t.AllowedOrigins)(ctx, jev.CanonicalOrigin(u))
	if err != nil {
		out.Status, out.Kind, out.Checked = VerifyUnknown, jev.KindCredentialsUnavailable, false
		out.Hint, out.Message = out.Kind, err.Error()
		return out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		out.Status, out.Kind, out.Hint, out.Message = VerifyUnknown, jev.KindBadRequest, jev.KindBadRequest, "cannot build the check"
		return out
	}
	if anthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	tr := v.Transport
	if tr == nil {
		own := jev.NewTransport()
		defer own.CloseIdleConnections()
		tr = own
	}
	hc := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		// A redirect would carry the key's header to another URL: the check stops at the vendor.
		return http.ErrUseLastResponse
	}}
	resp, err := hc.Do(req)
	if err != nil {
		out.Status = VerifyUnreachable
		out.Kind, out.Message = jev.KindRetryable, "network error: "+unwrapURLErr(err).Error()
		if ctx.Err() != nil {
			out.Kind, out.Message = jev.KindTimeout, "the vendor did not answer in time"
		}
		out.Hint = out.Kind
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	out.HTTPStatus = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		out.OK, out.Status = true, VerifyOK
		return out
	}
	out.Kind = kindOfStatus(resp.StatusCode)
	out.Status = statusOfKind(out.Kind)
	out.Message = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, vendorMessage(body, resp.StatusCode))
	out.Hint = hintFor(out.Status, out.Kind)
	return out
}

// kindOfStatus maps a vendor HTTP status to a decision-model error kind (the jev taxonomy).
func kindOfStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return jev.KindAuth
	case status == http.StatusPaymentRequired:
		return jev.KindCredits
	case status == http.StatusNotFound:
		return jev.KindNotFound
	case status == http.StatusTooManyRequests || status >= 500:
		return jev.KindRetryable
	}
	return jev.KindBadRequest
}

// vendorMessage reads the error message of an OpenAI ({"error":{"message"}}) or Anthropic
// ({"type":"error","error":{"message"}}) error body, else the status text.
func vendorMessage(body []byte, status int) string {
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &env) == nil {
		var inner struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Error, &inner) == nil && inner.Message != "" {
			return inner.Message
		}
		var s string
		if json.Unmarshal(env.Error, &s) == nil && s != "" {
			return s
		}
		if env.Message != "" {
			return env.Message
		}
	}
	return http.StatusText(status)
}

// candidateKey is the KeyFunc of a candidate secret: it releases the secret only for one of its
// allowed origins (D78), so a check can never send it anywhere else.
func candidateKey(secret string, allowed []string) jev.KeyFunc {
	origins := canonicalOrigins(allowed)
	return func(_ context.Context, origin string) (string, error) {
		if !slices.Contains(origins, origin) {
			return "", fmt.Errorf("%w: %s (allowed: %s)", errVerifyOriginRefused, origin, strings.Join(origins, ", "))
		}
		return secret, nil
	}
}

// keyInfo turns OpenRouter's key data into the verification's label and limit fields. The
// remaining limit is the vendor's limit_remaining, else limit − usage (never below zero).
func keyInfo(ki *jev.KeyInfo) (label *string, limit, remaining *float64) {
	if ki == nil {
		return nil, nil, nil
	}
	if ki.Label != "" {
		l := ki.Label
		label = &l
	}
	if ki.LimitUSD != nil {
		l := *ki.LimitUSD
		limit = &l
		r := max(l-ki.UsageUSD, 0)
		if ki.LimitRemainingUSD != nil {
			r = *ki.LimitRemainingUSD
		}
		remaining = &r
	}
	return label, limit, remaining
}

// clean masks the candidate secret, every credential pattern and every secret the process
// redactor knows in msg, then cuts it to MaxVerifyMessage characters (D40). Masking comes first
// so the cut can never leave a recognisable prefix of a secret behind; the cut text is masked
// again, as D40 orders it.
func (v *Verifier) clean(msg, secret string) string {
	if msg == "" {
		return ""
	}
	redact := func(s string) string {
		s = maskCutSecret(s, secret)
		s = config.NewRedactor(secret).Redact(s)
		if v.Redact != nil {
			s = v.Redact(s)
		}
		return s
	}
	return redact(truncateChars(redact(msg), MaxVerifyMessage))
}

// maskCutSecret masks a prefix of secret (4 characters or more) that ends s, before a trailing
// "…": what is left of the key when a message was cut before the key was masked (the decision
// client bounds vendor messages to 300 characters before it masks the key it sent).
func maskCutSecret(s, secret string) string {
	body, cut := strings.CutSuffix(s, "…")
	for k := len(secret) - 1; k >= 4; k-- {
		if strings.HasSuffix(body, secret[:k]) {
			body = body[:len(body)-k] + config.Masked
			if cut {
				body += "…"
			}
			return body
		}
	}
	return s
}

// truncateChars cuts s to at most n characters (runes), ending with "…" when it cut.
func truncateChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// jevMessage is the message of a decision-model error without the "jev <kind>" prefix.
func jevMessage(err error) string {
	var je *jev.Error
	if errors.As(err, &je) && je.Message != "" {
		return je.Message
	}
	return err.Error()
}

// unwrapURLErr drops *url.Error's "Get <url>:" prefix, which repeats the URL.
func unwrapURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func hostOfURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

func defaultIfBlank(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func (v *Verifier) logger() *slog.Logger {
	if v.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return v.Logger
}
