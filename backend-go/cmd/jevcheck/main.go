// Command jevcheck validates the decision-model (Jev) configuration: it resolves the backend the
// gateway would use and says why, checks the key (OpenRouter GET /api/v1/key, free), and sends
// the one-question connectivity probe (contracts/cascade/probe.v1.json, ≈ $0.00001). It prints
// the snapshot, latency and cost, or the error kind with a plain-language fix (SPEC §3.1, §6).
//
//	jevcheck [-backend id] [-model id] [-base-url url] [-path /v1/systemone] [-timeout 10s] [-json]
//	jevcheck --gateway http://localhost:8080 --admin-key-file admin.key [-timeout 10s] [-json]
//	jevcheck --capture DIR [--max-cost-usd 0.01] [--dry-run]     (capture.go, D211)
//
// It reads the same environment as the proxy (OPENROUTER_API_KEY, TYPESAFE_API_KEY,
// TRUTHTABLE_JEV_*, TRUTHTABLE_OPENROUTER_BASE_URL, TRUTHTABLE_DEMO_MODE); flags override it. That
// probe is jevcheck's own call: the gateway's cost ledger does not book it. A key connected in
// the dashboard lives in the gateway's credential store, which the environment cannot see:
// --gateway asks a running gateway to test its own decision model instead (POST
// /api/decision-model/test with the admin key read from --admin-key-file, as Settings → Test
// does), and the gateway books that probe.
// Exit status: 0 ready, 1 the check failed (including a backend that is off), 2 usage error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/jev"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// Exit statuses.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

type options struct {
	backend, model, baseURL, path string
	gateway, adminKeyFile         string
	timeout                       time.Duration
	json, noKeyCheck              bool
	capture                       captureOptions
}

// captureTimeout is the default -timeout of a --capture run (seven calls, one of 30,000 tokens).
const captureTimeout = 3 * time.Minute

// How the check reached the decision model (report.Via).
const (
	viaEnv     = "env"
	viaGateway = "gateway"
)

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	o := options{}
	fs := flag.NewFlagSet("jevcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.backend, "backend", "", "backend id (default TRUTHTABLE_JEV_BACKEND, else auto)")
	fs.StringVar(&o.model, "model", "", "decision model (default TRUTHTABLE_JEV_MODEL, else the backend default)")
	fs.StringVar(&o.baseURL, "base-url", "", "decision endpoint base URL (default TRUTHTABLE_JEV_BASE_URL)")
	fs.StringVar(&o.path, "path", "", "decision endpoint path (default TRUTHTABLE_JEV_PATH)")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Second, "overall time limit of the check")
	fs.BoolVar(&o.json, "json", false, "print one JSON object instead of text")
	fs.BoolVar(&o.noKeyCheck, "no-key-check", false, "skip the free OpenRouter key check")
	fs.StringVar(&o.gateway, "gateway", "", "test the decision model of the gateway at this base URL (its credentials, a dashboard key included)")
	fs.StringVar(&o.adminKeyFile, "admin-key-file", "", "with -gateway: file holding an admin key of that gateway")
	fs.StringVar(&o.capture.dir, "capture", "", "capture sanitized vendor fixtures into this directory (D211; OPENROUTER_API_KEY, about $0.002)")
	fs.Float64Var(&o.capture.maxCostUSD, "max-cost-usd", defaultCaptureCapUSD, "with --capture: the most the capture may spend")
	fs.BoolVar(&o.capture.dryRun, "dry-run", false, "with --capture: print the plan and its estimate, send nothing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 || o.timeout <= 0 {
		fmt.Fprintln(stderr, "jevcheck: unexpected arguments or a non-positive -timeout")
		return exitUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if o.capture.dir != "" {
		if o.gateway != "" || o.adminKeyFile != "" || o.backend != "" || o.path != "" || o.baseURL != "" || o.json || o.noKeyCheck {
			fmt.Fprintln(stderr, "jevcheck: --capture takes only --max-cost-usd, --dry-run and -timeout (it always calls OpenRouter: TRUTHTABLE_OPENROUTER_BASE_URL)")
			return exitUsage
		}
		if !(o.capture.maxCostUSD > 0) || o.capture.maxCostUSD > 1 {
			fmt.Fprintln(stderr, "jevcheck: --max-cost-usd must be above 0 and at most 1")
			return exitUsage
		}
		o.capture.timeout = o.timeout
		if !set["timeout"] {
			o.capture.timeout = captureTimeout
		}
		if o.model != "" {
			getenv = withEnv(getenv, "TRUTHTABLE_JEV_MODEL", o.model)
		}
		return runCapture(ctx, o.capture, getenv, time.Now(), nil, stdout, stderr)
	}
	if set["max-cost-usd"] || set["dry-run"] {
		fmt.Fprintln(stderr, "jevcheck: --max-cost-usd and --dry-run go with --capture")
		return exitUsage
	}
	if o.gateway != "" || o.adminKeyFile != "" {
		return runGateway(ctx, o, stdout, stderr)
	}

	cfg, warnings := configFromEnv(getenv)
	for _, f := range []struct {
		value string
		field *string
	}{{o.backend, &cfg.Backend}, {o.model, &cfg.Model}, {o.baseURL, &cfg.BaseURL}, {o.path, &cfg.Path}} {
		if f.value != "" {
			*f.field = f.value
		}
	}
	client, err := jev.New(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "jevcheck:", err)
		return exitUsage
	}
	if c, ok := client.(io.Closer); ok {
		defer c.Close()
	}
	info := client.Info()
	rep := report{Via: viaEnv, Backend: info.Backend, Reason: info.Reason, Endpoint: info.Endpoint, Model: info.Model,
		Credential: info.CredentialSource, Warnings: append(warnings, info.Warnings...)}
	if info.Backend != jev.BackendMock && !envHasKey(getenv) {
		rep.Notes = append(rep.Notes, dashboardKeyHint)
	}

	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	switch {
	case info.Backend == jev.BackendOff:
		rep.Error = &reportError{Kind: jev.KindOff, Message: info.Reason, Hint: jev.Hint(jev.KindOff)}
		if info.Misconfigured {
			rep.Error.Hint = "Fix the setting named above, then run jevcheck again."
		}
	default:
		rep.check(ctx, client, o.noKeyCheck)
		if rep.LatencyMs != nil || rep.Attempts > 0 {
			rep.CostLedger = ledgerNotBooked
		}
	}
	return rep.finish(o.json, stdout)
}

// finish prints the report and returns the exit status.
func (r *report) finish(asJSON bool, stdout io.Writer) int {
	r.OK = r.Error == nil
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		r.print(stdout)
	}
	if !r.OK {
		return exitFailed
	}
	return exitOK
}

// runGateway is a --gateway check.
func runGateway(ctx context.Context, o options, stdout, stderr io.Writer) int {
	if o.backend != "" || o.model != "" || o.baseURL != "" || o.path != "" || o.noKeyCheck {
		fmt.Fprintln(stderr, "jevcheck:", errGatewayFlags)
		return exitUsage
	}
	if o.gateway == "" || o.adminKeyFile == "" {
		fmt.Fprintln(stderr, "jevcheck: --gateway and --admin-key-file go together (the test needs an admin key)")
		return exitUsage
	}
	base, err := gatewayURL(o.gateway)
	if err != nil {
		fmt.Fprintln(stderr, "jevcheck:", err)
		return exitUsage
	}
	key, err := readKeyFile(o.adminKeyFile)
	if err != nil {
		fmt.Fprintln(stderr, "jevcheck: --admin-key-file:", err)
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	rep := checkGateway(ctx, base, key)
	return rep.finish(o.json, stdout)
}

// withEnv is getenv with key set to value.
func withEnv(getenv func(string) string, key, value string) func(string) string {
	return func(k string) string {
		if k == key {
			return value
		}
		return getenv(k)
	}
}

// envHasKey reports whether the environment holds a decision-model credential.
func envHasKey(getenv func(string) string) bool {
	for _, k := range []string{"OPENROUTER_API_KEY", "TYPESAFE_API_KEY", "TRUTHTABLE_JEV_API_KEY", "TRUTHTABLE_JEV_API_KEY_FILE"} {
		if strings.TrimSpace(getenv(k)) != "" {
			return true
		}
	}
	return false
}

// report is the check result (also the -json output).
type report struct {
	OK bool `json:"ok"`
	// Via is env (jevcheck's own probe) or gateway (the gateway's test, --gateway); Gateway is
	// the gateway's host.
	Via        string       `json:"via"`
	Gateway    string       `json:"gateway,omitempty"`
	Backend    string       `json:"backend"`
	Reason     string       `json:"reason"`
	Endpoint   string       `json:"endpoint,omitempty"`
	Model      string       `json:"model,omitempty"`
	Credential string       `json:"credential"`
	Warnings   []string     `json:"warnings,omitempty"`
	Key        *jev.KeyInfo `json:"key,omitempty"`
	KeyError   *reportError `json:"key_error,omitempty"`
	Simulated  bool         `json:"simulated"`
	Snapshot   string       `json:"snapshot,omitempty"`
	LatencyMs  *int64       `json:"latency_ms,omitempty"`
	ConnectMs  *int64       `json:"connect_ms,omitempty"`
	Attempts   int          `json:"attempts,omitempty"`
	Tokens     *int         `json:"input_tokens,omitempty"`
	CostUSD    *float64     `json:"cost_usd,omitempty"`
	CostSource string       `json:"cost_source,omitempty"`
	// CostLedger says whether the gateway's cost ledger counts the probe: booked (the gateway called
	// the vendor) or not_booked (jevcheck called it, or the mock backend answered); empty when no
	// probe reached a decision model (a check the gateway skipped, or a usage error).
	CostLedger string       `json:"cost_ledger,omitempty"`
	Notes      []string     `json:"notes,omitempty"`
	Error      *reportError `json:"error,omitempty"`
}

type reportError struct {
	Kind    string `json:"kind"`
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func toReportError(err error) *reportError {
	var e *jev.Error
	if errors.As(err, &e) {
		hint := e.Hint
		if hint == "" {
			hint = jev.Hint(e.Kind)
		}
		return &reportError{Kind: e.Kind, Status: e.Status, Message: e.Message, Hint: hint}
	}
	return &reportError{Kind: jev.ErrorKind(err), Message: err.Error(), Hint: jev.Hint(jev.ErrorKind(err))}
}

// check runs the key check (OpenRouter) and the probe.
func (r *report) check(ctx context.Context, client jev.Client, noKeyCheck bool) {
	r.Simulated = r.Backend == jev.BackendMock
	if kc, ok := client.(jev.KeyChecker); ok && !noKeyCheck {
		ki, err := kc.CheckKey(ctx)
		switch {
		case errors.Is(err, jev.ErrKeyCheckUnsupported):
		case err != nil:
			r.KeyError = toReportError(err)
			if k := r.KeyError.Kind; k == jev.KindAuth || k == jev.KindCredits {
				// Definitive: the probe would fail the same way.
				r.Error = r.KeyError
				return
			}
		default:
			r.Key = maskKeyInfo(ki)
		}
	}
	resp, err := jev.Probe(ctx, client)
	if resp != nil {
		r.Snapshot = resp.Snapshot
		lat := resp.LatencyMs
		r.LatencyMs, r.ConnectMs, r.Attempts = &lat, resp.ConnectMs, resp.Attempts
		r.Tokens, r.CostUSD, r.CostSource = resp.InputTokens, resp.CostUSD, resp.CostSource
	}
	if err != nil {
		r.Error = toReportError(err)
		var e *jev.Error
		if errors.As(err, &e) && e.Attempts > 0 {
			r.Attempts = e.Attempts
		}
	}
}

func (r *report) print(w io.Writer) {
	if r.Via == viaGateway {
		fmt.Fprintln(w, "TrustAgent jevcheck: decision-model connectivity, tested by the gateway at "+r.Gateway)
	} else {
		fmt.Fprintln(w, "TrustAgent jevcheck: decision-model connectivity")
	}
	fmt.Fprintln(w)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-10s %s\n", k, v)
		}
	}
	ledger := func() {
		switch {
		case r.CostLedger == ledgerBooked:
			row("Ledger", "booked: the gateway made the call, so its cost ledger counts it")
		case r.CostLedger == ledgerNotBooked && r.Simulated:
			row("Ledger", "not booked (simulated: nothing was spent)")
		case r.CostLedger == ledgerNotBooked:
			row("Ledger", "not booked: jevcheck called the vendor itself, so the gateway's cost ledger does not count this probe")
		}
	}
	notes := func() {
		for _, n := range r.Notes {
			row("Note", n)
		}
	}
	row("Backend", r.Backend)
	row("Why", r.Reason)
	row("Endpoint", r.Endpoint)
	row("Model", r.Model)
	if r.Backend != jev.BackendOff {
		row("Credential", r.Credential)
	}
	for _, wn := range r.Warnings {
		row("Warning", wn)
	}
	switch {
	case r.Key != nil:
		row("Key", describeKey(r.Key))
	case r.KeyError != nil:
		row("Key", "check failed: "+r.KeyError.Kind+statusSuffix(r.KeyError.Status)+" "+r.KeyError.Message)
	}
	if r.Error == nil {
		result := "OK"
		if r.Simulated {
			result += " (simulated: the mock backend answers without calling a vendor)"
		}
		row("Probe", result)
		row("Snapshot", r.Snapshot)
		if r.LatencyMs != nil {
			lat := fmt.Sprintf("%d ms", *r.LatencyMs)
			if r.ConnectMs != nil {
				lat += fmt.Sprintf(" (connect %d ms)", *r.ConnectMs)
			}
			lat += fmt.Sprintf(" · %d attempt", r.Attempts)
			if r.Attempts != 1 {
				lat += "s"
			}
			row("Latency", lat)
		}
		if r.Tokens != nil {
			row("Tokens", strconv.Itoa(*r.Tokens)+" input")
		}
		if r.CostUSD != nil {
			row("Cost", fmt.Sprintf("$%s (%s)", formatUSD(*r.CostUSD), r.CostSource))
		}
		ledger()
		notes()
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Ready.")
		return
	}
	if r.Backend != jev.BackendOff {
		row("Probe", "FAILED: "+r.Error.Kind+statusSuffix(r.Error.Status))
		if r.Attempts > 0 {
			row("Attempts", strconv.Itoa(r.Attempts))
		}
		row("Message", r.Error.Message)
		ledger()
	}
	row("Hint", r.Error.Hint)
	notes()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Not ready.")
}

// MaskedLabel is what every jevcheck output shows for OpenRouter's key label (D211): the label
// is a truncated form of the key itself, so no part of it is printed, in text or JSON.
const MaskedLabel = "sk-or-…xxxx"

// maskKeyInfo is a copy of k with its label masked.
func maskKeyInfo(k *jev.KeyInfo) *jev.KeyInfo {
	if k == nil {
		return nil
	}
	c := *k
	if c.Label != "" {
		c.Label = MaskedLabel
	}
	return &c
}

func statusSuffix(status int) string {
	if status == 0 {
		return ""
	}
	return fmt.Sprintf(" (HTTP %d)", status)
}

func describeKey(k *jev.KeyInfo) string {
	parts := []string{"valid"}
	if k.Label != "" {
		parts = append(parts, "label "+MaskedLabel)
	}
	parts = append(parts, "usage $"+formatUSD(k.UsageUSD))
	if k.LimitUSD == nil {
		parts = append(parts, "no credit limit (set one at openrouter.ai/settings/keys)")
	} else {
		lim := "limit $" + formatUSD(*k.LimitUSD)
		if k.LimitRemainingUSD != nil {
			lim += ", $" + formatUSD(*k.LimitRemainingUSD) + " left"
			if *k.LimitRemainingUSD <= 0 {
				lim += " (limit reached: raise it at openrouter.ai/settings/keys)"
			}
		}
		parts = append(parts, lim)
	}
	if k.IsFreeTier {
		parts = append(parts, "free tier (Jev needs credits)")
	}
	return strings.Join(parts, " · ")
}

// formatUSD prints amounts of a dollar or more with cents, and smaller ones with three
// significant digits in plain notation ($0.0123, $0.0000139).
func formatUSD(v float64) string {
	switch {
	case v == 0:
		return "0"
	case v >= 0.1:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	decimals := min(int(-math.Floor(math.Log10(v)))+2, 12)
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

// configFromEnv reads the decision-model keys of contracts/config/keys.v1.json. Invalid values
// keep their defaults and are reported.
func configFromEnv(getenv func(string) string) (jev.Config, []string) {
	var warnings []string
	h := jev.DefaultHTTPConfig()
	h.KeepAlive = 0 // a one-shot check keeps nothing warm
	h.SpendCapUSDDay = 0
	boolVar := func(key string, dst *bool) {
		if v := getenv(key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s=%q is not a boolean; using %v", key, v, *dst))
				return
			}
			*dst = b
		}
	}
	durVar := func(key string, dst *time.Duration) {
		if v := getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				warnings = append(warnings, fmt.Sprintf("%s=%q is not a positive duration; using %s", key, v, *dst))
				return
			}
			*dst = d
		}
	}
	floatVar := func(key string, dst *float64) {
		if v := getenv(key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 {
				warnings = append(warnings, fmt.Sprintf("%s=%q is not a non-negative number; ignored", key, v))
				return
			}
			*dst = f
		}
	}
	intVar := func(key string, dst *int) {
		if v := getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				warnings = append(warnings, fmt.Sprintf("%s=%q is not a non-negative integer; using %d", key, v, *dst))
				return
			}
			*dst = n
		}
	}
	durVar("TRUTHTABLE_JEV_ATTEMPT_TIMEOUT", &h.AttemptTimeout)
	intVar("TRUTHTABLE_JEV_MAX_RETRIES", &h.MaxRetries)
	durVar("TRUTHTABLE_JEV_RETRY_AFTER_CAP", &h.RetryAfterCap)
	floatVar("TRUTHTABLE_JEV_PRICE_PER_MTOK", &h.PricePerMTok)
	floatVar("TRUTHTABLE_JEV_CHARS_PER_TOKEN", &h.CharsPerToken)
	boolVar("TRUTHTABLE_JEV_ZDR", &h.ZDR)
	boolVar("TRUTHTABLE_JEV_SEND_SESSION_ID", &h.SendSessionID)
	boolVar("TRUTHTABLE_JEV_LOG_PAYLOADS", &h.LogPayloads)
	if v := getenv("TRUTHTABLE_JEV_APP_URL"); v != "" {
		h.AppURL = v
	}
	if v := getenv("TRUTHTABLE_JEV_APP_TITLE"); v != "" {
		h.AppTitle = v
	}
	h.PublicURL = getenv("TRUTHTABLE_PUBLIC_URL")

	cfg := jev.Config{
		Backend: getenv("TRUTHTABLE_JEV_BACKEND"), Model: getenv("TRUTHTABLE_JEV_MODEL"),
		OpenRouterKey: getenv("OPENROUTER_API_KEY"), TypeSafeKey: getenv("TYPESAFE_API_KEY"),
		JevAPIKey: getenv("TRUTHTABLE_JEV_API_KEY"), BaseURL: getenv("TRUTHTABLE_JEV_BASE_URL"),
		Path: getenv("TRUTHTABLE_JEV_PATH"), OpenRouterBaseURL: getenv("TRUTHTABLE_OPENROUTER_BASE_URL"),
		Mock: jev.MockConfig{Mode: getenv("TRUTHTABLE_JEV_MOCK_MODE"), FixturesPath: getenv("TRUTHTABLE_JEV_MOCK_FIXTURES")},
		HTTP: &h,
	}
	if cfg.JevAPIKey == "" {
		if path := getenv("TRUTHTABLE_JEV_API_KEY_FILE"); path != "" {
			if b, err := os.ReadFile(path); err != nil { // #nosec G304 G703 -- the operator names the key file (TRUTHTABLE_JEV_API_KEY_FILE)
				warnings = append(warnings, "TRUTHTABLE_JEV_API_KEY_FILE cannot be read: "+err.Error())
			} else {
				cfg.JevAPIKey = strings.TrimSpace(string(b))
			}
		}
	}
	boolVar("TRUTHTABLE_DEMO_MODE", &cfg.DemoMode)
	boolVar("TRUTHTABLE_JEV_MOCK_STRICT", &cfg.Mock.Strict)
	if v := getenv("TRUTHTABLE_JEV_MOCK_SEED"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Mock.Seed = n
		} else {
			warnings = append(warnings, fmt.Sprintf("TRUTHTABLE_JEV_MOCK_SEED=%q is not an integer; using 0", v))
		}
	} else {
		cfg.Mock.Seed = 42
	}
	return cfg, warnings
}
