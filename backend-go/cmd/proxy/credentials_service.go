package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/middleware"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
)

// CredentialWrite is one credential write (D75): PUT /api/credentials/:name (#55), the
// verify-and-bind of POST /api/credentials/:name/verify (#57) and the key from Connect OpenRouter
// (#59) all go through writeCredential.
type CredentialWrite struct {
	// Name is credentials.name; Provider must match it (D29).
	Name     string
	Provider string
	// Secret is the key; empty for a verify of the stored key (#57).
	Secret string
	// BaseURL is accepted for compatible and upstream-* only (D29, D78).
	BaseURL string
	// Verify checks the key with its provider first (default true); Bind lists the targets to
	// bind when verification passes (decision_model, judge).
	Verify bool
	Bind   []string
	// Actor is the admin_audit_log actor; Action its action (credential.put, credential.verify,
	// credential.connect_openrouter, ...).
	Actor  string
	Action string
}

// CredentialWriteResult is api.ts CredentialWriteResult.
type CredentialWriteResult struct {
	Credential   any                           `json:"credential"`
	Stored       bool                          `json:"stored"`
	Bindings     CredentialWriteBindings       `json:"bindings"`
	Verification *CredentialVerificationResult `json:"verification"`
}

// CredentialWriteBindings is CredentialWriteResult.bindings.
type CredentialWriteBindings struct {
	Applied []string             `json:"applied"`
	Skipped []CredentialBindSkip `json:"skipped"`
}

// CredentialBindSkip is one skipped binding target with its reason (env_managed, backend_pinned,
// verification_failed).
type CredentialBindSkip struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// CredentialVerificationResult is api.ts CredentialVerification.
type CredentialVerificationResult struct {
	OK                bool     `json:"ok"`
	Status            string   `json:"status"`
	Hint              *string  `json:"hint"`
	LatencyMs         *int64   `json:"latency_ms"`
	CostUSD           *float64 `json:"cost_usd"`
	CostSource        *string  `json:"cost_source"`
	KeyLabel          *string  `json:"key_label,omitempty"`
	LimitUSD          *float64 `json:"limit_usd,omitempty"`
	LimitRemainingUSD *float64 `json:"limit_remaining_usd,omitempty"`

	// keyChecked: OpenRouter's GET /api/v1/key answered, so the key fields are sent even when
	// null (limit_usd null = the key has no spending limit). Other providers omit them.
	keyChecked bool
}

// MarshalJSON writes key_label, limit_usd and limit_remaining_usd (null included) when OpenRouter
// reported them, and leaves them out otherwise: a null limit means "no spending limit" only when
// someone asked.
func (r CredentialVerificationResult) MarshalJSON() ([]byte, error) {
	type plain CredentialVerificationResult
	if !r.keyChecked {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		KeyLabel          *string  `json:"key_label"`
		LimitUSD          *float64 `json:"limit_usd"`
		LimitRemainingUSD *float64 `json:"limit_remaining_usd"`
	}{plain(r), r.KeyLabel, r.LimitUSD, r.LimitRemainingUSD})
}

// credentialView is api.ts CredentialView: what the dashboard sees of a credential. The secret
// never appears; masked is config.MaskSecret ("sk-…3f2a", D79) and fingerprint the D30 HMAC.
type credentialView struct {
	Name             string     `json:"name"`
	Provider         string     `json:"provider"`
	Source           string     `json:"source"`
	Masked           string     `json:"masked"`
	Fingerprint      string     `json:"fingerprint"`
	AllowedHosts     []string   `json:"allowed_hosts"`
	CreatedAt        *time.Time `json:"created_at"`
	RotatedAt        *time.Time `json:"rotated_at"`
	LastVerifiedAt   *time.Time `json:"last_verified_at"`
	LastVerifyStatus *string    `json:"last_verify_status"`
	LastUsedAt       *time.Time `json:"last_used_at"`
	UsedBy           []string   `json:"used_by"`
	Undecryptable    bool       `json:"undecryptable"`
}

// credentialVerifyEvery is the per-name interval of POST /api/credentials/:name/verify (D40):
// each check may cost a probe.
const credentialVerifyEvery = 10 * time.Second

// credentialService holds what the credential and Connect OpenRouter endpoints share (CN-7,
// CN-8): the verifier, the pending sign-ins, the per-name verify limit and the last checks of
// env credentials.
type credentialService struct {
	verifier *credentials.Verifier
	connects *credentials.ConnectStore
	// exchange carries the Connect OpenRouter code exchange (nil: a client with a 15 s timeout).
	exchange *http.Client
	// verifyLimit is #57's limit: one check per credential name every credentialVerifyEvery,
	// whoever asks.
	verifyLimit gin.HandlerFunc
	hub         *websocket.Hub
	now         func() time.Time

	mu sync.Mutex
	// envChecks are the last checks of Go-side env credentials, by name (D80): kept in memory
	// only, since an env key has no row.
	envChecks map[string]envCredentialCheck
}

// envCredentialCheck is the last check of an env credential; it applies while the env key keeps
// the fingerprint it had.
type envCredentialCheck struct {
	fingerprint string
	at          time.Time
	status      string
}

// newCredentialService builds the credential endpoints' state. The verifier's probes book their
// cost on the decision model's cost hooks as the non-enforcing component probe (D82), so they can
// never spend the enforcing reserve.
func newCredentialService(s *server) *credentialService {
	byName := func(c *gin.Context) string { return "credential:" + c.Param("name") }
	deny := func(c *gin.Context, status int, code, message string) { abortAPI(c, status, code, message) }
	return &credentialService{
		verifier: &credentials.Verifier{
			HTTP: jevHTTPConfig(s.cfg, s.log), OpenRouterBaseURL: s.cfg.Jev.OpenRouterBaseURL,
			Costs: s.costs.decision, Budget: s.costs.budget, Prices: s.costs.prices,
			Redact: s.redact, Logger: s.log,
		},
		connects:    credentials.NewConnectStore(credentials.ConnectTTL, credentials.ConnectPerActor, nil),
		verifyLimit: middleware.RateLimitBy(middleware.NewMemoryLimiter(), 1, credentialVerifyEvery, deny, byName),
		hub:         s.hub,
		now:         time.Now,
		envChecks:   map[string]envCredentialCheck{},
	}
}

// credentialError is a refused credential request: its REST status, code, message, details and
// field errors. Messages name credentials and settings, never a secret.
type credentialError struct {
	status  int
	code    string
	message string
	details map[string]any
	fields  []fieldError
}

func (e *credentialError) Error() string { return e.message }

// abortCredential answers err: a *credentialError as itself, anything else as 500 (logged).
func (s *server) abortCredential(c *gin.Context, what string, err error) {
	var ce *credentialError
	if errors.As(err, &ce) {
		abortAPIDetails(c, ce.status, ce.code, ce.message, ce.details, ce.fields...)
		return
	}
	s.log.Error(what+" failed", "err", s.redact(err.Error()))
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request failed; see the proxy log.")
}

// credentialNotFound is 404 for a name that is not a credential or has nothing stored.
func credentialNotFound(name string) *credentialError {
	return &credentialError{status: http.StatusNotFound, code: codeNotFound, message: fmt.Sprintf("No saved key named %q.", name)}
}

// credentialInvalid is 422 validation_failed with one field error.
func credentialInvalid(path, message string) *credentialError {
	return &credentialError{status: http.StatusUnprocessableEntity, code: codeValidationFailed, message: message,
		fields: []fieldError{{Path: path, Message: message}}}
}

// credentialEnvSide says which process's env sets the credential (D29, D80): the proxy's (the
// snapshot holds an env credential of that name), the engine's (EngineInfo.env_managed names its
// env var), or neither (""). An env key always wins and is read-only here.
func credentialEnvSide(snap *runtimecfg.Snapshot, spec credentials.Spec) credentials.Side {
	if c, ok := snap.Credentials[spec.Name]; ok && c.Source == credentials.SourceEnv {
		return credentials.SideProxy
	}
	if spec.Side == credentials.SideEngine && slices.Contains(snap.EngineEnv, spec.EnvVar) {
		return credentials.SideEngine
	}
	return ""
}

// credentialEnvManaged is 409 env_managed for a credential an env var sets (D29, D33): details.keys
// names the variable and details.names the credential.
func credentialEnvManaged(spec credentials.Spec, side credentials.Side) *credentialError {
	msg := fmt.Sprintf("%s is set by %s in the gateway's .env; change it there and restart.", spec.Name, spec.EnvVar)
	if side == credentials.SideEngine {
		msg = fmt.Sprintf("%s is set by %s in the engine's .env; change it there and restart (check it with make doctor).", spec.Name, spec.EnvVar)
	}
	return &credentialError{status: http.StatusConflict, code: codeEnvManaged, message: msg,
		details: map[string]any{"fields": []string{}, "keys": []string{spec.EnvVar}, "names": []string{spec.Name}}}
}

// credentialStoreRefusal is why a dashboard credential cannot be written now (D31, D32): no
// database (503 persistence_disabled) or no durable master key (503 credentials_unavailable); nil
// when the store is ready.
func (s *server) credentialStoreRefusal(snap *runtimecfg.Snapshot) *credentialError {
	if s.rt == nil || s.rt.store == nil {
		return &credentialError{status: http.StatusServiceUnavailable, code: codePersistenceDisabled,
			message: "Keys can't be saved without a database (TRUTHTABLE_DATABASE_URL). Keys set in .env still work."}
	}
	if snap.StoreState != credentials.StateReady {
		return &credentialError{status: http.StatusServiceUnavailable, code: codeCredentialsUnavailable,
			message: "Keys can't be saved: there is no durable master key. Set TRUTHTABLE_SECRET_KEY (or TRUTHTABLE_SECRET_KEY_FILE) and restart."}
	}
	return nil
}

// checkBindTargets validates a write's bind list (D75): decision_model and judge only (upstreams
// bind through PATCH /api/settings, D41), each one the credential can serve, and only with a
// check, because a key is bound only once it passed one. It returns the targets without repeats.
// judgeCredentials (settings_api.go) lists the credentials the deep-audit judge can use.
func checkBindTargets(name string, bind []string, verify bool) ([]string, *credentialError) {
	out := []string{}
	for _, t := range bind {
		switch {
		case t != runtimecfg.TargetDecisionModel && t != runtimecfg.TargetJudge:
			return nil, credentialInvalid("bind", fmt.Sprintf("bind takes decision_model and judge, not %q; upstream keys bind through the settings.", t))
		case t == runtimecfg.TargetDecisionModel && len(runtimecfg.DecisionBackends(name)) == 0:
			return nil, credentialInvalid("bind", fmt.Sprintf("A %s key cannot run the fast check.", name))
		case t == runtimecfg.TargetJudge && !slices.Contains(judgeCredentials, name):
			return nil, credentialInvalid("bind", fmt.Sprintf("A %s key cannot run the deep-audit judge.", name))
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > 0 && !verify {
		return nil, credentialInvalid("bind", "bind needs verify: a key is bound only after it passes a check.")
	}
	return out, nil
}

// writeCredential stores, verifies and binds one credential (D75). With a Secret (#55, #59) it
// checks the key with its provider, then stores it sealed and applies the bindings in one
// transaction with their audit rows, unless a failed check would replace the key a consumer is
// using (stored:false, the old key stays). Without one (#57) it checks the stored or env key: a
// dashboard row records the outcome and may bind, an env key keeps it in memory (D80). Afterwards
// the runtime configuration reloads, which swaps the decision model and pushes the engine when a
// binding changed, and the dashboard hears credentials_changed (and settings_updated when a
// binding applied). Refusals are *credentialError.
func (s *server) writeCredential(ctx context.Context, w CredentialWrite) (CredentialWriteResult, error) {
	spec, ok := credentials.Lookup(w.Name)
	if !ok {
		return CredentialWriteResult{}, credentialNotFound(w.Name)
	}
	if w.Provider == "" {
		w.Provider = spec.Provider
	}
	if w.Provider != spec.Provider {
		return CredentialWriteResult{}, credentialInvalid("provider", fmt.Sprintf("%s keys take provider %s.", spec.Name, spec.Provider))
	}
	targets, ferr := checkBindTargets(spec.Name, w.Bind, w.Verify)
	if ferr != nil {
		return CredentialWriteResult{}, ferr
	}
	snap := s.rt.manager.Current()
	if side := credentialEnvSide(snap, spec); side != "" {
		// Env keys are read-only (D29); checking a proxy-side one is allowed (D80).
		if w.Secret != "" || len(targets) > 0 || side == credentials.SideEngine {
			return CredentialWriteResult{}, credentialEnvManaged(spec, side)
		}
		return s.verifyEnvCredential(ctx, snap, spec), nil
	}
	if w.Secret == "" {
		return s.verifyStoredCredential(ctx, snap, spec, w, targets)
	}
	return s.putCredential(ctx, snap, spec, w, targets)
}

// putCredential is writeCredential with a new secret (#55, #59).
func (s *server) putCredential(ctx context.Context, snap *runtimecfg.Snapshot, spec credentials.Spec, w CredentialWrite, targets []string) (CredentialWriteResult, error) {
	if e := s.credentialStoreRefusal(snap); e != nil {
		return CredentialWriteResult{}, e
	}
	cipher, err := s.rt.manager.Cipher()
	if err != nil {
		return CredentialWriteResult{}, &credentialError{status: http.StatusServiceUnavailable, code: codeCredentialsUnavailable,
			message: "Keys can't be saved: there is no durable master key. Set TRUTHTABLE_SECRET_KEY and restart."}
	}
	origins, err := credentials.AllowedOrigins(spec.Name, w.Provider, w.BaseURL, s.cfg.Jev.OpenRouterBaseURL)
	if err != nil {
		return CredentialWriteResult{}, baseURLRefusal(err)
	}
	// D42: a received secret joins the process redactor before anything can send or log it.
	if s.redactor != nil {
		s.redactor.Add(w.Secret)
	}
	cs := s.apply.creds
	res := CredentialWriteResult{Bindings: CredentialWriteBindings{Applied: []string{}, Skipped: []CredentialBindSkip{}}}
	var v *credentials.Verification
	if w.Verify {
		got := cs.verifier.Verify(ctx, s.credentialVerifyTarget(snap, spec.Name, w.Secret, origins, w.BaseURL))
		v = &got
		res.Verification = verificationResult(v)
	}

	// D75: a failed check on a key a consumer uses stores nothing; the old key keeps serving. (A
	// bound row the master key cannot open serves nobody, so the new key replaces it.)
	if credentialCheckFailed(v) && credentialInUse(snap, spec.Name) {
		old, err := s.rt.store.GetCredential(ctx, spec.Name)
		switch {
		case err == nil:
			res.Bindings.Skipped = skipAll(targets, runtimecfg.SkipVerificationFailed)
			res.Credential = s.dashboardCredentialView(snap, spec, old)
			s.log.Warn("credential not saved: its check failed and the key in use stays", "credential", spec.Name, "status", v.Status)
			return res, nil
		case !errors.Is(err, store.ErrNotFound):
			return CredentialWriteResult{}, err
		}
		// A binding that names a key nobody stored: nothing to keep, so this one is stored.
	}

	patch, changed, settingsDiff, err := s.credentialBindings(snap, spec.Name, targets, v, &res)
	if err != nil {
		return CredentialWriteResult{}, err
	}
	ct, nonce, keyID, err := cipher.Seal(spec.Name, w.Provider, origins, w.Secret)
	if err != nil {
		return CredentialWriteResult{}, err
	}
	rec := &store.CredentialRecord{Name: spec.Name, Provider: w.Provider, Ciphertext: ct, Nonce: nonce, KeyID: keyID,
		Masked: credentials.Masked(w.Secret), Fingerprint: s.rt.manager.Fingerprint(w.Secret), AllowedHosts: origins}
	if v != nil && v.Checked {
		at, status := cs.now().UTC(), v.Status
		rec.LastVerifiedAt, rec.LastVerifyStatus = &at, &status
	}
	audit := store.CredentialAudit{Action: w.Action, Diff: credentialAuditDiff(v, res.Bindings), Settings: settingsDiff}
	put, err := s.rt.store.PutCredential(ctx, s.rt.channel, rec, patch, w.Actor, audit)
	if err != nil {
		return CredentialWriteResult{}, err
	}
	res.Stored = true
	snap = s.reloadAfterCredentialWrite(ctx)
	res.Credential = s.dashboardCredentialView(snap, spec, put.Record)
	action := "put"
	if put.Rotated {
		action = "rotated"
	}
	cs.publishCredentialsChanged(spec.Name, action, put.Record.Fingerprint)
	if len(changed) > 0 {
		cs.publishSettingsUpdated(put.Version, changed)
	}
	return res, nil
}

// verifyStoredCredential is writeCredential for a dashboard key already stored (#57): it checks
// the key, records the outcome and applies the bindings when it passed.
func (s *server) verifyStoredCredential(ctx context.Context, snap *runtimecfg.Snapshot, spec credentials.Spec, w CredentialWrite, targets []string) (CredentialWriteResult, error) {
	if s.rt.store == nil {
		return CredentialWriteResult{}, credentialNotFound(spec.Name)
	}
	if _, err := s.rt.store.GetCredential(ctx, spec.Name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return CredentialWriteResult{}, credentialNotFound(spec.Name)
		}
		return CredentialWriteResult{}, err
	}
	cred, ok := snap.Credentials[spec.Name]
	if !ok {
		// The row may be newer than the snapshot (another replica wrote it): read it once more.
		snap = s.reloadAfterCredentialWrite(ctx)
		cred, ok = snap.Credentials[spec.Name]
	}
	if !ok || cred.Source != credentials.SourceDashboard {
		if snap.StoreState != credentials.StateReady {
			return CredentialWriteResult{}, s.credentialStoreRefusal(snap)
		}
		return CredentialWriteResult{}, &credentialError{status: http.StatusConflict, code: codeConflict,
			message: fmt.Sprintf("The saved %s key can't be read with the current master key. Paste it again, or restore the old TRUTHTABLE_SECRET_KEY.", spec.Name),
			details: map[string]any{"reason": "undecryptable", "names": []string{spec.Name}}}
	}
	cs := s.apply.creds
	v := cs.verifier.Verify(ctx, s.credentialVerifyTarget(snap, spec.Name, cred.Secret, cred.AllowedOrigins, ""))
	res := CredentialWriteResult{Stored: true, Verification: verificationResult(&v),
		Bindings: CredentialWriteBindings{Applied: []string{}, Skipped: []CredentialBindSkip{}}}
	if v.Checked {
		diff := map[string]any{"bind": targets}
		if err := s.rt.store.MarkCredentialVerified(ctx, spec.Name, v.Status, cs.now().UTC(), w.Actor, diff); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return CredentialWriteResult{}, credentialNotFound(spec.Name)
			}
			return CredentialWriteResult{}, err
		}
	}
	version, changed, err := s.bindStoredCredential(ctx, snap, spec.Name, targets, &v, w.Actor, &res)
	if err != nil {
		return CredentialWriteResult{}, err
	}
	snap = s.reloadAfterCredentialWrite(ctx)
	rec, err := s.rt.store.GetCredential(ctx, spec.Name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return CredentialWriteResult{}, credentialNotFound(spec.Name)
		}
		return CredentialWriteResult{}, err
	}
	res.Credential = s.dashboardCredentialView(snap, spec, rec)
	if len(changed) > 0 {
		cs.publishSettingsUpdated(version, changed)
	}
	return res, nil
}

// bindStoredCredential applies a #57 check's bindings: when the check passed, the bindings rows
// are written guarded by the version the snapshot read (a concurrent settings write makes it
// read again, at most three times). It fills res.Bindings and returns the new version and the
// dotted paths that changed.
func (s *server) bindStoredCredential(ctx context.Context, snap *runtimecfg.Snapshot, name string, targets []string, v *credentials.Verification,
	actor string, res *CredentialWriteResult) (int64, []string, error) {
	if len(targets) == 0 {
		return 0, nil, nil
	}
	if !v.OK {
		res.Bindings.Skipped = skipAll(targets, runtimecfg.SkipVerificationFailed)
		return 0, nil, nil
	}
	for attempt := 0; ; attempt++ {
		after, applied, skipped := runtimecfg.ApplyBindings(snap.Persisted, name, targets, snap.EnvManaged, snap.BackendPinned(name))
		res.Bindings.Applied, res.Bindings.Skipped = applied, bindSkips(skipped)
		rows, err := runtimecfg.ChangedRows(snap.Persisted, after)
		if err != nil {
			return 0, nil, err
		}
		if len(rows) == 0 {
			return 0, nil, nil // already bound as asked
		}
		changed, diff := bindingChanges(snap.Persisted, after)
		diff["reason"], diff["credential"] = "credential.verify", name
		version, err := s.rt.store.PatchSettings(ctx, s.rt.channel, snap.Version, rows, actor, diff)
		if errors.Is(err, store.ErrStaleVersion) && attempt < 2 {
			snap = s.reloadAfterCredentialWrite(ctx)
			continue
		}
		if errors.Is(err, store.ErrStaleVersion) {
			return 0, nil, &credentialError{status: http.StatusPreconditionFailed, code: codeStaleVersion,
				message: "The settings kept changing while the key was being bound. Try again."}
		}
		if err != nil {
			return 0, nil, err
		}
		return version, changed, nil
	}
}

// verifyEnvCredential checks a proxy-side env key (#57 on an env row, D80): the outcome is kept
// in memory (an env key has no row) and nothing is written or bound. stored stays true: it is
// false only when a failed check left the key in use in place of a new one (api.ts
// CredentialWriteResult), and a check of the env key replaces nothing.
func (s *server) verifyEnvCredential(ctx context.Context, snap *runtimecfg.Snapshot, spec credentials.Spec) CredentialWriteResult {
	cs := s.apply.creds
	cred := snap.Credentials[spec.Name]
	v := cs.verifier.Verify(ctx, s.credentialVerifyTarget(snap, spec.Name, cred.Secret, cred.AllowedOrigins, ""))
	if v.Checked {
		cs.mu.Lock()
		cs.envChecks[spec.Name] = envCredentialCheck{fingerprint: cred.Fingerprint, at: cs.now().UTC(), status: v.Status}
		cs.mu.Unlock()
	}
	return CredentialWriteResult{Credential: s.envCredentialView(snap, spec), Stored: true, Verification: verificationResult(&v),
		Bindings: CredentialWriteBindings{Applied: []string{}, Skipped: []CredentialBindSkip{}}}
}

// credentialBindings computes a #55/#59 write's bindings (D75, D76): when the check passed, the
// targets are bound over the persisted settings (env-managed and backend-pinned ones skipped) and
// the result is the settings patch (changed fields only, merged by the store), the dotted paths
// that changed and the settings.update audit diff. A failed or missing check skips every target.
func (s *server) credentialBindings(snap *runtimecfg.Snapshot, name string, targets []string, v *credentials.Verification,
	res *CredentialWriteResult) (map[string]json.RawMessage, []string, map[string]any, error) {
	if len(targets) == 0 {
		return nil, nil, nil, nil
	}
	if v == nil || !v.OK {
		res.Bindings.Skipped = skipAll(targets, runtimecfg.SkipVerificationFailed)
		return nil, nil, nil, nil
	}
	after, applied, skipped := runtimecfg.ApplyBindings(snap.Persisted, name, targets, snap.EnvManaged, snap.BackendPinned(name))
	res.Bindings.Applied, res.Bindings.Skipped = applied, bindSkips(skipped)
	patch, err := runtimecfg.ChangedFields(snap.Persisted, after)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(patch) == 0 {
		return nil, nil, nil, nil
	}
	changed, diff := bindingChanges(snap.Persisted, after)
	diff["credential"] = name
	return patch, changed, diff, nil
}

// bindingChanges lists the dotted paths a binding write changes (the bindings and D76's leave-
// simulated flip) and their {from, to} audit diff.
func bindingChanges(before, after runtimecfg.RuntimeSettings) ([]string, map[string]any) {
	str := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	pairs := []struct {
		path     string
		from, to any
	}{
		{"bindings.decision_model", str(before.Bindings.DecisionModel), str(after.Bindings.DecisionModel)},
		{"bindings.judge", str(before.Bindings.Judge), str(after.Bindings.Judge)},
		{"decision_model.backend", before.DecisionModel.Backend, after.DecisionModel.Backend},
		{"decision_model.model", str(before.DecisionModel.Model), str(after.DecisionModel.Model)},
		{"judge.provider", before.Judge.Provider, after.Judge.Provider},
		{"judge.model", str(before.Judge.Model), str(after.Judge.Model)},
	}
	changed := []string{}
	diff := map[string]any{}
	for _, p := range pairs {
		if p.from != p.to {
			changed = append(changed, p.path)
			diff[p.path] = map[string]any{"from": p.from, "to": p.to}
		}
	}
	return changed, diff
}

// skipAll skips every target for one reason.
func skipAll(targets []string, reason string) []CredentialBindSkip {
	out := make([]CredentialBindSkip, 0, len(targets))
	for _, t := range targets {
		out = append(out, CredentialBindSkip{Target: t, Reason: reason})
	}
	return out
}

// bindSkips converts runtimecfg skips to the wire shape.
func bindSkips(skipped []runtimecfg.BindingSkip) []CredentialBindSkip {
	out := make([]CredentialBindSkip, 0, len(skipped))
	for _, sk := range skipped {
		out = append(out, CredentialBindSkip{Target: sk.Target, Reason: sk.Reason})
	}
	return out
}

// credentialCheckFailed reports a check that proved the key does not work (or could not prove it
// does). A check that deliberately sent nothing (upstream-* keys, hint not_checked) has not
// failed.
func credentialCheckFailed(v *credentials.Verification) bool {
	return v != nil && !v.OK && v.Hint != credentials.HintNotChecked
}

// credentialAuditDiff is the credential audit row's diff beyond what the store records: the check
// and the bindings. Never the secret.
func credentialAuditDiff(v *credentials.Verification, b CredentialWriteBindings) map[string]any {
	d := map[string]any{"verify": nil, "bindings": b.Applied}
	if v != nil {
		d["verify"] = v.Status
	}
	if len(b.Skipped) > 0 {
		d["skipped"] = b.Skipped
	}
	return d
}

// verificationResult is the wire form of a check (nil without one).
func verificationResult(v *credentials.Verification) *CredentialVerificationResult {
	if v == nil {
		return nil
	}
	r := &CredentialVerificationResult{OK: v.OK, Status: v.Status, CostUSD: v.CostUSD,
		KeyLabel: v.KeyLabel, LimitUSD: v.LimitUSD, LimitRemainingUSD: v.LimitRemainingUSD, keyChecked: v.KeyChecked}
	if v.Hint != "" {
		h := v.Hint
		r.Hint = &h
	}
	if v.Checked {
		ms := v.Latency.Milliseconds()
		r.LatencyMs = &ms
	}
	if v.CostSource != "" {
		cs := v.CostSource
		r.CostSource = &cs
	}
	return r
}

// credentialVerifyTarget is what a check sends where (D40): the secret with its allowed origins,
// the decision model the settings name when it is one this credential serves, and for the
// compatible gateway its base URL (the settings' when it is within the key's origins) and path.
func (s *server) credentialVerifyTarget(snap *runtimecfg.Snapshot, name, secret string, origins []string, baseURL string) credentials.VerifyTarget {
	t := credentials.VerifyTarget{Name: name, Secret: secret, AllowedOrigins: origins}
	dm := snap.Settings.DecisionModel
	if slices.Contains(runtimecfg.DecisionBackends(name), dm.Backend) {
		t.Model = strval(dm.Model)
	}
	if name == credentials.NameCompatible {
		t.Model, t.Path = strval(dm.Model), strval(dm.Path)
		t.BaseURL = baseURL
		if b := strval(dm.BaseURL); b != "" {
			if o, err := credentials.Canonical(b); err == nil && slices.Contains(origins, o) {
				t.BaseURL = b
			}
		}
		if t.BaseURL == "" && len(origins) > 0 {
			t.BaseURL = origins[0]
		}
	}
	return t
}

// baseURLRefusal maps an AllowedOrigins error to 422 validation_failed on base_url (or provider),
// with the URL rule's code (insecure_url, url_credentials, forbidden_target, invalid_url).
func baseURLRefusal(err error) *credentialError {
	var ue *credentials.URLError
	switch {
	case errors.As(err, &ue):
		e := credentialInvalid("base_url", "base_url "+ue.Error()+".")
		e.fields[0].Code = ue.Code
		return e
	case errors.Is(err, credentials.ErrProviderMismatch):
		return credentialInvalid("provider", "The provider does not match the key name.")
	case errors.Is(err, credentials.ErrBaseURL):
		return credentialInvalid("base_url", strings.TrimPrefix(err.Error(), "credentials: ")+".")
	}
	return credentialInvalid("base_url", "base_url is not valid.")
}

// reloadAfterCredentialWrite rereads the settings store so this replica serves the write at once
// (the decision-model swap and the engine push follow the new snapshot) and returns the snapshot.
// A failed read is logged; the NOTIFY and the poll catch up.
func (s *server) reloadAfterCredentialWrite(ctx context.Context) *runtimecfg.Snapshot {
	if err := s.rt.manager.Reload(ctx); err != nil {
		s.log.Warn("runtime settings reload after a credential write failed; the poll retries", "err", s.redact(err.Error()))
	}
	return s.rt.manager.Current()
}

// credentialInUse reports a dashboard key that a consumer is bound to and that the snapshot can
// use (it opened with the current master key).
func credentialInUse(snap *runtimecfg.Snapshot, name string) bool {
	c, ok := snap.Credentials[name]
	return ok && c.Source == credentials.SourceDashboard && len(credentialUsedBy(snap.Settings, name)) > 0
}

// credentialUsedBy lists the consumers that use the credential called name in the settings in
// effect (CredentialView.used_by).
func credentialUsedBy(rs runtimecfg.RuntimeSettings, name string) []string {
	is := func(p *string) bool { return p != nil && *p == name }
	out := []string{}
	if is(rs.Bindings.DecisionModel) {
		out = append(out, "decision_model")
	}
	if is(rs.Bindings.Judge) {
		out = append(out, "judge")
	}
	if is(rs.Upstream.Default.Credential) || is(rs.Bindings.UpstreamDefault) {
		out = append(out, "upstream_default")
	}
	if is(rs.Upstream.Anthropic.Credential) {
		out = append(out, "upstream_anthropic")
	}
	if is(rs.Upstream.Fallback.Credential) {
		out = append(out, "upstream_fallback")
	}
	return out
}

// credentialViews lists every credential the gateway knows (#54), in the D29 display order: an env
// key wins over a dashboard row of the same name; engine-side env keys (D80) show without masked
// or fingerprint, since only the engine reads them.
func (s *server) credentialViews(snap *runtimecfg.Snapshot, rows []store.CredentialRecord) []credentialView {
	byName := map[string]*store.CredentialRecord{}
	for i := range rows {
		byName[rows[i].Name] = &rows[i]
	}
	out := []credentialView{}
	for _, spec := range credentials.Specs() {
		switch {
		case credentialEnvSide(snap, spec) == credentials.SideProxy:
			out = append(out, s.envCredentialView(snap, spec))
		case credentialEnvSide(snap, spec) == credentials.SideEngine:
			out = append(out, credentialView{Name: spec.Name, Provider: spec.Provider, Source: credentials.SourceEnv,
				AllowedHosts: nonNil(credentials.VendorOrigins(spec.Name, "")), UsedBy: []string{}})
		case byName[spec.Name] != nil:
			out = append(out, s.dashboardCredentialView(snap, spec, byName[spec.Name]))
		}
	}
	return out
}

// envCredentialView is the view of a proxy-side env key, with its last check when one ran
// against the key the env holds now.
func (s *server) envCredentialView(snap *runtimecfg.Snapshot, spec credentials.Spec) credentialView {
	c := snap.Credentials[spec.Name]
	v := credentialView{Name: spec.Name, Provider: spec.Provider, Source: credentials.SourceEnv, Masked: c.Masked,
		Fingerprint: c.Fingerprint, AllowedHosts: nonNil(c.AllowedOrigins), UsedBy: credentialUsedBy(snap.Settings, spec.Name)}
	cs := s.apply.creds
	cs.mu.Lock()
	chk, ok := cs.envChecks[spec.Name]
	cs.mu.Unlock()
	if ok && chk.fingerprint == c.Fingerprint {
		at, status := chk.at, chk.status
		v.LastVerifiedAt, v.LastVerifyStatus = &at, &status
	}
	return v
}

// dashboardCredentialView is the view of a stored row. Its allowed origins are the ones in effect
// (a vendor's are recomputed at load, D78); a row the master key cannot open shows as
// undecryptable, without masked, fingerprint or origins.
func (s *server) dashboardCredentialView(snap *runtimecfg.Snapshot, spec credentials.Spec, rec *store.CredentialRecord) credentialView {
	v := credentialView{Name: rec.Name, Provider: rec.Provider, Source: credentials.SourceDashboard, Masked: rec.Masked,
		Fingerprint: rec.Fingerprint, CreatedAt: &rec.CreatedAt, RotatedAt: rec.RotatedAt, LastVerifiedAt: rec.LastVerifiedAt,
		LastVerifyStatus: rec.LastVerifyStatus, LastUsedAt: rec.LastUsedAt, UsedBy: credentialUsedBy(snap.Settings, rec.Name)}
	switch c, ok := snap.Credentials[rec.Name]; {
	case ok && c.Source == credentials.SourceDashboard:
		v.AllowedHosts = c.AllowedOrigins
	case slices.Contains(snap.Undecryptable, rec.Name):
		v.Undecryptable, v.Masked, v.Fingerprint, v.AllowedHosts = true, "", "", []string{}
	case !spec.CustomOrigin:
		v.AllowedHosts = credentials.VendorOrigins(rec.Name, s.cfg.Jev.OpenRouterBaseURL)
	default:
		v.AllowedHosts = rec.AllowedHosts
	}
	v.AllowedHosts = nonNil(v.AllowedHosts)
	return v
}

// nonNil is list, or an empty list for nil (JSON [] rather than null).
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// publishCredentialsChanged sends WS credentials_changed (SPEC §2.5, D42): the name, what
// happened and the fingerprint only, which clients without the content scope do not get (D79).
func (cs *credentialService) publishCredentialsChanged(name, action, fingerprint string) {
	if cs.hub == nil {
		return
	}
	var fp any
	if fingerprint != "" {
		fp = fingerprint
	}
	ev, err := websocket.NewEvent("credentials_changed", "",
		map[string]any{"name": name, "action": action, "fingerprint": fp},
		map[string]any{"name": name, "action": action, "fingerprint": nil})
	if err != nil {
		return
	}
	cs.hub.Publish(ev)
}

// publishSettingsUpdated sends WS settings_updated{version, keys}: keys are the dotted settings
// paths a credential write changed.
func (cs *credentialService) publishSettingsUpdated(version int64, paths []string) {
	if cs.hub == nil {
		return
	}
	ev, err := websocket.NewEvent("settings_updated", "", map[string]any{"version": version, "keys": paths}, nil)
	if err != nil {
		return
	}
	cs.hub.Publish(ev)
}

// connectActor is who a Connect OpenRouter sign-in belongs to (D38): the caller's stable
// principal (an env key or api_keys row, which a session is bound to), else its label.
func connectActor(c *gin.Context) string {
	if id := callerIdentity(c); id != nil {
		if id.Subject != "" {
			return id.Subject
		}
		if id.Label != "" {
			return "label:" + id.Label
		}
	}
	return publicLabel
}
