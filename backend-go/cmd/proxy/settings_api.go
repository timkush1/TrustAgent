package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/upstream"
	"github.com/truthtable/backend-go/internal/websocket"
)

// The settings endpoints (SPEC §2.4 rows 52 and 53; SEC-5): GET /api/settings shows every
// configuration key with where its value comes from and the runtime settings in effect; PATCH
// /api/settings changes the runtime settings (D31-D33, D37, D41, D76, D77). A write is stored with
// the version bump and its audit row in one transaction, then the runtime configuration reloads;
// the decision-model swap, the upstream rebuild, the engine push and the ledger caps follow from
// its subscribers, never from this handler.

// Field-error codes of a refused settings write (api.ts FieldError.code). The web shows the
// message and may render its own words by code (D86).
const (
	fieldUnknownSetting      = "unknown_setting"
	fieldInvalidType         = "invalid_type"
	fieldInvalidValue        = "invalid_value"
	fieldBindingMismatch     = "binding_mismatch"
	fieldUnknownCredential   = "unknown_credential"       // #nosec G101 -- a field-error code, not a credential
	fieldCredentialKind      = "credential_kind"          // #nosec G101 -- a field-error code, not a credential
	fieldCredentialMissing   = "credential_missing"       // #nosec G101 -- a field-error code, not a credential
	fieldCredentialUndecrypt = "credential_undecryptable" // #nosec G101 -- a field-error code, not a credential
	fieldBackendPinned       = "backend_pinned"
	fieldReenterSecret       = "reenter_secret"
	fieldOriginNotAllowed    = "origin_not_allowed"
	ingestBindingPath        = "bindings.upstream_default"
)

// The checks other streams own, called through these so tests can pin how their findings surface:
// upstream.RuntimeRisks (GCONN: the D23 condition refuses, D15 warns) and costs.ValidateOverrides
// (GCOST: D53 price checks).
var (
	upstreamRisks     = upstream.RuntimeRisks
	validateOverrides = costs.ValidateOverrides
)

// settingsStore is what PATCH /api/settings writes and reads (*store.PostgresStore).
type settingsStore interface {
	PatchSettings(ctx context.Context, channel string, ifVersion int64, rows map[string]json.RawMessage, actor string, diff map[string]any) (int64, error)
	IngestKeyExists(ctx context.Context) (bool, error)
}

// settingKeyView is api.ts SettingKeyView.
type settingKeyView struct {
	Key             string   `json:"key"`
	Env             *string  `json:"env"`
	Value           string   `json:"value"`
	Default         string   `json:"default"`
	Source          string   `json:"source"`
	Secret          bool     `json:"secret"`
	Advanced        bool     `json:"advanced"`
	RestartRequired bool     `json:"restart_required"`
	Editable        bool     `json:"editable"`
	Services        []string `json:"services"`
}

// settingsGroup is one SettingsView.groups entry: a registry group's title and its keys.
type settingsGroup struct {
	Name string           `json:"name"`
	Keys []settingKeyView `json:"keys"`
}

// warningDetails is api.ts WarningDetails: credential names or enforcing routes.
type warningDetails struct {
	Names  []string `json:"names,omitempty"`
	Routes []string `json:"routes,omitempty"`
}

// settingsWarning is one SettingsView.warnings entry: a configuration diagnostic ({key, message,
// hint}) or a runtime warning that also carries its code and details.
type settingsWarning struct {
	Key     string          `json:"key"`
	Message string          `json:"message"`
	Hint    string          `json:"hint"`
	Code    string          `json:"code,omitempty"`
	Details *warningDetails `json:"details,omitempty"`
}

// settingsView is api.ts SettingsView.
type settingsView struct {
	Version    int64                      `json:"version"`
	ETag       string                     `json:"etag"`
	Groups     []settingsGroup            `json:"groups"`
	Runtime    runtimecfg.RuntimeSettings `json:"runtime"`
	EnvManaged []string                   `json:"env_managed"`
	Warnings   []settingsWarning          `json:"warnings"`
	DotenvPath *string                    `json:"dotenv_path"`
}

// settingsETag is the ETag of a runtime-config version (D31).
func settingsETag(version int64) string { return `"s` + strconv.FormatInt(version, 10) + `"` }

// mountSettings mounts GET and PATCH /api/settings.
func (s *server) mountSettings(api *gin.RouterGroup, g apiGuards) {
	api.GET("/settings", g.read, g.rateLimit, s.handleGetSettings)
	api.PATCH("/settings", g.writable, g.admin, g.rateLimit, g.bodyLimit, s.handlePatchSettings)
}

// handleGetSettings serves GET /api/settings (read): secrets masked by caller (D79).
func (s *server) handleGetSettings(c *gin.Context) {
	snap, rows := s.rt.manager.Stored()
	view := s.settingsView(snap, rows, isAdmin(c), nil)
	c.Header("ETag", view.ETag)
	noStore(c)
	c.JSON(http.StatusOK, view)
}

// settingsView builds the SettingsView of snap for a caller; rows are the stored settings rows
// snap was built from (their paths show source "dashboard"), extra are warnings of the write that
// produced snap.
func (s *server) settingsView(snap *runtimecfg.Snapshot, rows map[string]json.RawMessage, admin bool, extra []settingsWarning) settingsView {
	v := settingsView{
		Version: snap.Version, ETag: settingsETag(snap.Version),
		Groups:     s.settingGroups(snap, runtimecfg.StoredPaths(rows), admin),
		Runtime:    redactRuntimeURLs(snap.Settings),
		EnvManaged: append([]string{}, snap.EnvManaged...),
		Warnings:   append(s.settingsWarnings(snap), extra...),
	}
	if admin && s.cfg.EnvFile != "" {
		p := s.cfg.EnvFile
		v.DotenvPath = &p
	}
	return v
}

// settingGroups lists the proxy's and the engine's registry keys by registry group, in registry
// order. A key's value is masked for its caller (D79): a secret shows config.MaskSecret to an
// admin and "set" or "" to anyone else, and a URL never shows userinfo or its query.
func (s *server) settingGroups(snap *runtimecfg.Snapshot, stored []string, admin bool) []settingsGroup {
	reg := s.cfg.Registry()
	if reg == nil {
		return []settingsGroup{}
	}
	entries := map[string]config.Entry{}
	for _, e := range s.cfg.Entries() {
		entries[e.Key.Name] = e
	}
	judge := s.engineJudgeConfig()
	editableBase := admin && s.rt.store != nil && !s.cfg.Readonly
	out := []settingsGroup{}
	for _, g := range reg.Groups {
		grp := settingsGroup{Name: g.Title, Keys: []settingKeyView{}}
		for i := range reg.Keys {
			k := &reg.Keys[i]
			if k.Group != g.ID || !(k.HasService("proxy") || k.HasService("engine")) {
				continue
			}
			e, hasEntry := entries[k.Name]
			grp.Keys = append(grp.Keys, s.settingKey(k, e, hasEntry, entries, snap, stored, judge, admin, editableBase))
		}
		if len(grp.Keys) > 0 {
			out = append(out, grp)
		}
	}
	return out
}

// engineJudgeConfig is the judge the engine reports it configured: provider and model ("" when
// the engine has not reported one).
func (s *server) engineJudgeConfig() (j struct{ provider, model string }) {
	if s.status == nil {
		return j
	}
	if ji := s.status.engineHealth().Info.GetJudge(); ji != nil {
		j.provider, j.model = ji.GetConfiguredProvider(), ji.GetModel()
	}
	return j
}

// settingKey is the SettingKeyView of one registry key. Precedence follows D33: a non-empty env
// value (the proxy's env or .env, or the engine's env as EngineInfo.env_managed reports it, D80)
// wins over a dashboard row, which wins over the default.
func (s *server) settingKey(k *config.Key, e config.Entry, hasEntry bool, entries map[string]config.Entry, snap *runtimecfg.Snapshot,
	stored []string, judge struct{ provider, model string }, admin, editableBase bool) settingKeyView {
	v := settingKeyView{
		Key: k.Name, Default: k.DefaultValue(), Value: k.DefaultValue(), Source: string(config.SourceDefault),
		Secret: k.Secret, Advanced: k.Advanced, RestartRequired: k.Restart, Services: append([]string{}, k.Services...),
	}
	if k.Secret {
		v.Default = "" // a secret's default is never shown (they are empty)
	}
	path := runtimecfg.SettingPath(k.Name)
	fileKey, fileSet := entries[k.Name+"_FILE"]
	switch {
	case hasEntry && (e.Source == config.SourceEnv || e.Source == config.SourceDotenv) && strings.TrimSpace(e.Value) != "":
		v.Source, v.Value = string(e.Source), shownValue(k, e.Value, admin)
		if k.Secret && admin {
			v.Value = e.DisplayValue()
		}
		v.Env = stringPtr(e.Var)
	case hasEntry && k.Secret && fileSet && strings.TrimSpace(fileKey.Value) != "":
		// The secret is read from the file its _FILE key names; only that it is set is shown.
		v.Source, v.Value, v.Env = "file", "set", stringPtr(fileKey.Key.Name)
	case !hasEntry && slices.Contains(snap.EngineEnv, k.Name):
		v.Source, v.Env = string(config.SourceEnv), stringPtr(k.Name)
		v.Value = "set" // the proxy never sees the engine's values (D80), only that they are set
		switch {
		case k.Secret:
		case path == "judge.provider" && judge.provider != "":
			v.Value = judge.provider
		case path == "judge.model" && judge.model != "":
			v.Value = judge.model
		}
	case path != "" && slices.Contains(stored, path):
		v.Source, v.Value = "dashboard", runtimeValue(snap.Settings, path)
	case hasEntry:
		v.Source, v.Value = string(e.Source), shownValue(k, e.Value, admin)
		if k.Secret && admin {
			v.Value = e.DisplayValue()
		}
		if e.Var != "" {
			v.Env = stringPtr(e.Var)
		}
	}
	// A URL shows no userinfo and no query. A secret URL (the database, Redis) is already masked
	// or "set" by now; parsing the mask as a URL would only percent-encode its ellipsis.
	if k.Kind == config.KindURL && !k.Secret {
		v.Value, v.Default = redactURL(v.Value), redactURL(v.Default)
	}
	v.Editable = editableBase && path != "" && !snap.IsEnvManaged(path)
	return v
}

// shownValue is a key's value as a caller may see it: a secret is "set" or "" to anyone but an
// admin (D79; the caller masks it for admins), anything else as it is.
func shownValue(k *config.Key, value string, admin bool) string {
	if !k.Secret {
		return value
	}
	if admin {
		return config.MaskSecret(value)
	}
	if value == "" {
		return ""
	}
	return "set"
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// runtimeValue is the runtime setting at path in env-var syntax: "" for null, a number with at
// least two decimals (USD), a string as it is.
func runtimeValue(rs runtimecfg.RuntimeSettings, path string) string {
	raw := runtimecfg.Flatten(rs)[path]
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return usdString(f)
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// usdString formats a dollar amount with two decimals, or more when two would round it.
func usdString(f float64) string {
	if two := strconv.FormatFloat(f, 'f', 2, 64); strconv.FormatFloat(f, 'f', -1, 64) == strings.TrimRight(strings.TrimRight(two, "0"), ".") {
		return two
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// redactRuntimeURLs is rs with every URL setting passed through redactURL (D79: no userinfo, no
// query) for every caller.
func redactRuntimeURLs(rs runtimecfg.RuntimeSettings) runtimecfg.RuntimeSettings {
	red := func(p *string) *string {
		if p == nil {
			return nil
		}
		r := redactURL(*p)
		return &r
	}
	rs.DecisionModel.BaseURL = red(rs.DecisionModel.BaseURL)
	rs.Upstream.Default.URL = red(rs.Upstream.Default.URL)
	rs.Upstream.Anthropic.URL = red(rs.Upstream.Anthropic.URL)
	rs.Upstream.Fallback.URL = red(rs.Upstream.Fallback.URL)
	if rs.PricingOverrides == nil {
		rs.PricingOverrides = []decision.PriceEntry{}
	}
	return rs
}

// settingsWarnings are the configuration diagnostics, the snapshot's warnings (D32), the
// fail-closed warning (D77) and the upstream risks that do not refuse a write (D15).
func (s *server) settingsWarnings(snap *runtimecfg.Snapshot) []settingsWarning {
	out := []settingsWarning{}
	for _, d := range s.cfg.Diagnostics {
		out = append(out, settingsWarning{Key: d.Key, Message: apiRedact(d.Message), Hint: apiRedact(d.Hint)})
	}
	for _, w := range s.runtimeWarnings(snap) {
		out = append(out, runtimeSettingsWarning(w))
	}
	_, risks := upstreamRisks(snap.Settings, snap.Credentials)
	for _, r := range risks {
		hint := r.Hint
		if hint == "" {
			hint = "Review the upstream settings."
		}
		out = append(out, settingsWarning{Key: upstreamRiskKey(r.Path), Code: r.Code, Message: apiRedact(r.Message),
			Hint: apiRedact(hint), Details: detailsOf(r.Details)})
	}
	return out
}

// upstreamRiskKey is the registry key an upstream warning belongs to, so the dashboard shows it
// next to that setting: the env var that seeds the setting at path, or, for an upstream's
// credential (upstream.<name>.credential), the env var of that upstream's key (D29).
func upstreamRiskKey(path string) string {
	if l, ok := runtimecfg.LookupLeaf(path); ok && l.Key != "" {
		return l.Key
	}
	if rest, ok := strings.CutPrefix(path, "upstream."); ok {
		name, _, _ := strings.Cut(rest, ".")
		if spec, ok := credentials.Lookup("upstream-" + name); ok {
			return spec.EnvVar
		}
	}
	return "TRUTHTABLE_UPSTREAM_URL"
}

// runtimeWarnings are the snapshot's warnings and, when it applies, fail_closed_unprotected.
func (s *server) runtimeWarnings(snap *runtimecfg.Snapshot) []runtimecfg.Warning {
	out := slices.Clone(snap.Warnings)
	if w := s.failClosedWarning(); w != nil {
		out = append(out, *w)
	}
	return out
}

// runtimeSettingsWarning renders a runtime warning as a settings warning: the key an operator
// changes and the hint on how.
func runtimeSettingsWarning(w runtimecfg.Warning) settingsWarning {
	sw := settingsWarning{Code: w.Code, Message: apiRedact(w.Message), Details: detailsOf(w.Details)}
	switch w.Code {
	case runtimecfg.WarnMasterKeyChanged:
		sw.Key, sw.Hint = "TRUTHTABLE_SECRET_KEY", "Restore the previous TRUTHTABLE_SECRET_KEY, or paste the keys again."
	case runtimecfg.WarnStoreUnavailable:
		sw.Key, sw.Hint = "TRUTHTABLE_SECRET_KEY", "Set TRUTHTABLE_SECRET_KEY (or TRUTHTABLE_SECRET_KEY_FILE) and restart."
	case runtimecfg.WarnSettingsUnreadable:
		sw.Key, sw.Hint = "TRUTHTABLE_DATABASE_URL", "Check that the database is reachable; the last settings read stay in effect."
	case runtimecfg.WarnFailClosedUnprotected:
		sw.Key, sw.Hint = "TRUTHTABLE_JEV_BACKEND", "Connect a decision model, or let those routes fail open."
	default:
		sw.Key = "settings"
	}
	return sw
}

// detailsOf keeps the WarningDetails fields of details (names and routes).
func detailsOf(details map[string]any) *warningDetails {
	strs := func(v any) []string {
		switch t := v.(type) {
		case []string:
			return slices.Clone(t)
		case []any:
			var out []string
			for _, x := range t {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
		return nil
	}
	d := &warningDetails{Names: strs(details["names"]), Routes: strs(details["routes"])}
	if len(d.Names) == 0 && len(d.Routes) == 0 {
		return nil
	}
	return d
}

// settingsPatch is a decoded PATCH body: the leaf paths it sets and their JSON values.
type settingsPatch map[string]json.RawMessage

// handlePatchSettings serves PATCH /api/settings (admin): If-Match "s<version>", a partial
// RuntimeSettings, ?confirm=true for a D77 change.
func (s *server) handlePatchSettings(c *gin.Context) {
	if s.rt == nil || s.rt.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Dashboard settings need a database (TRUTHTABLE_DATABASE_URL); set them in .env instead.")
		return
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		abortAPI(c, http.StatusPreconditionRequired, codePreconditionRequired,
			"Send If-Match with the ETag of the settings you edited (GET /api/settings).")
		return
	}
	patch, fields, ok := decodeSettingsPatch(c)
	if !ok {
		return
	}
	if len(fields) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "Some settings are not valid.", fields...)
		return
	}
	ctx := c.Request.Context()
	if err := s.rt.manager.Reload(ctx); err != nil {
		s.log.Error("settings not read before a write", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The settings could not be read; see the proxy log.")
		return
	}
	snap, rows := s.rt.manager.Stored()
	if !settingsETagMatches(ifMatch, snap.Version) {
		abortAPI(c, http.StatusPreconditionFailed, codeStaleVersion,
			"The settings changed since you loaded them: reload, reapply your edit and save again.")
		return
	}
	plan, status, failure := s.planSettingsWrite(ctx, snap, patch, c.Query("confirm") == "true")
	if failure != nil {
		abortAPIDetails(c, status, failure.code, failure.message, failure.details, failure.fields...)
		return
	}
	if len(plan.changed) == 0 {
		view := s.settingsView(snap, rows, true, plan.warnings)
		c.Header("ETag", view.ETag)
		noStore(c)
		c.JSON(http.StatusOK, view)
		return
	}
	write, err := runtimecfg.RowsWithLeaves(rows, plan.persisted, plan.changed)
	if err != nil {
		s.log.Error("settings rows not built", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The settings could not be saved; see the proxy log.")
		return
	}
	version, err := s.rt.store.PatchSettings(ctx, s.rt.channel, snap.Version, write, actor(c), plan.diff(snap.Persisted))
	switch {
	case errors.Is(err, store.ErrStaleVersion):
		abortAPI(c, http.StatusPreconditionFailed, codeStaleVersion,
			"The settings changed since you loaded them: reload, reapply your edit and save again.")
		return
	case err != nil:
		s.log.Error("settings not saved", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The settings could not be saved; see the proxy log.")
		return
	}
	if err := s.rt.manager.Reload(ctx); err != nil {
		s.log.Warn("settings saved but not reloaded yet; the reload loop retries", "version", version, "err", err)
	}
	s.publishSettingsUpdated(version, plan.changed)
	s.log.Info("settings updated", "version", version, "keys", strings.Join(plan.changed, ","), "actor", actor(c))
	snap, rows = s.rt.manager.Stored()
	view := s.settingsView(snap, rows, true, plan.warnings)
	c.Header("ETag", view.ETag)
	noStore(c)
	c.JSON(http.StatusOK, view)
}

// publishSettingsUpdated sends WS settings_updated{version, keys} (SPEC §2.5): keys are the dotted
// paths that changed.
func (s *server) publishSettingsUpdated(version int64, keys []string) {
	if s.hub == nil {
		return
	}
	ev, err := websocket.NewEvent("settings_updated", "", map[string]any{"version": version, "keys": keys}, nil)
	if err != nil {
		return
	}
	s.hub.Publish(ev)
}

// settingsETagMatches reports whether an If-Match header names version: a list of "s<v>" tags
// (weak tags and tags without quotes are accepted), or "*".
func settingsETagMatches(header string, version int64) bool {
	want := "s" + strconv.FormatInt(version, 10)
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
		if tag == "*" || strings.Trim(tag, `"`) == want {
			return true
		}
	}
	return false
}

// decodeSettingsPatch reads the body as a partial RuntimeSettings: it answers 413 and 400 itself
// (ok false) and returns the leaves it sets, or field errors for unknown settings and values of
// the wrong type. An empty string sets a nullable string to null.
func decodeSettingsPatch(c *gin.Context) (settingsPatch, []fieldError, bool) {
	var raw json.RawMessage
	if !decodeAuthzBody(c, &raw, "settings patch") {
		return nil, nil, false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || top == nil {
		abortAPI(c, http.StatusBadRequest, codeInvalidJSON, "The body is not a settings patch JSON object.")
		return nil, nil, false
	}
	patch := settingsPatch{}
	var fields []fieldError
	var walk func(prefix string, obj map[string]json.RawMessage)
	walk = func(prefix string, obj map[string]json.RawMessage) {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			path, v := k, obj[k]
			if prefix != "" {
				path = prefix + "." + k
			}
			if leaf, ok := runtimecfg.LookupLeaf(path); ok {
				if err := runtimecfg.CheckLeaf(leaf, v); err != nil {
					fields = append(fields, fieldError{Path: path, Message: err.Error(), Code: fieldInvalidType})
					continue
				}
				if leaf.Kind == runtimecfg.LeafNullableString && string(bytes.TrimSpace(v)) == `""` {
					v = json.RawMessage("null")
				}
				patch[path] = v
				continue
			}
			if !runtimecfg.IsBranch(path) {
				fields = append(fields, fieldError{Path: path, Message: "is not a runtime setting", Code: fieldUnknownSetting})
				continue
			}
			var sub map[string]json.RawMessage
			if json.Unmarshal(v, &sub) != nil || sub == nil {
				fields = append(fields, fieldError{Path: path, Message: "must be an object", Code: fieldInvalidType})
				continue
			}
			walk(path, sub)
		}
	}
	walk("", top)
	return patch, fields, true
}

// settingsWritePlan is a validated settings write: the persisted settings after it, the leaf paths
// that change, and the warnings that do not refuse it.
type settingsWritePlan struct {
	persisted runtimecfg.RuntimeSettings
	changed   []string
	warnings  []settingsWarning
}

// diff is the admin_audit_log diff of the write: each changed path with its value before and after
// (URLs and secrets are scrubbed by the store, D44).
func (p settingsWritePlan) diff(before runtimecfg.RuntimeSettings) map[string]any {
	a, b := runtimecfg.Flatten(before), runtimecfg.Flatten(p.persisted)
	fields := map[string]any{}
	for _, path := range p.changed {
		fields[path] = map[string]json.RawMessage{"from": a[path], "to": b[path]}
	}
	return map[string]any{"fields": fields, "paths": p.changed}
}

// writeFailure is a refused write: its status, code, message, details and field errors.
type writeFailure struct {
	code    string
	message string
	details map[string]any
	fields  []fieldError
}

// planSettingsWrite applies patch to the snapshot's persisted settings and checks the result, in
// order: D41b, env-managed paths (409 env_managed), values (422), the credential store (503),
// the ingest key (409 ingest_key_required) and, unless confirmed, D77 (409 confirmation_required).
func (s *server) planSettingsWrite(ctx context.Context, snap *runtimecfg.Snapshot, patch settingsPatch, confirmed bool) (settingsWritePlan, int, *writeFailure) {
	var plan settingsWritePlan
	// D41b: bindings.upstream_default and upstream.default.credential are one field.
	b, hasB := patch["bindings.upstream_default"]
	u, hasU := patch["upstream.default.credential"]
	switch {
	case hasB && hasU && !bytes.Equal(compactJSON(b), compactJSON(u)):
		msg := "bindings.upstream_default and upstream.default.credential are the same setting; send one value"
		return plan, http.StatusUnprocessableEntity, &writeFailure{code: codeValidationFailed, message: "Some settings are not valid.",
			fields: []fieldError{
				{Path: "bindings.upstream_default", Message: msg, Code: fieldBindingMismatch},
				{Path: "upstream.default.credential", Message: msg, Code: fieldBindingMismatch},
			}}
	case hasB:
		patch["upstream.default.credential"] = b
	case hasU:
		patch["bindings.upstream_default"] = u
	}

	// D33: an env-managed path is read-only; sending the value in effect is not a change.
	current := runtimecfg.Flatten(snap.Settings)
	var pinned, keys []string
	for _, l := range runtimecfg.Leaves {
		v, ok := patch[l.Path]
		if !ok || !snap.IsEnvManaged(l.Path) {
			continue
		}
		if bytes.Equal(compactJSON(v), current[l.Path]) {
			delete(patch, l.Path)
			continue
		}
		pinned = append(pinned, l.Path)
		for _, k := range snap.EnvSources[l.Path] {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	if len(pinned) > 0 {
		return plan, http.StatusConflict, &writeFailure{code: codeEnvManaged,
			message: "These settings are set by environment variables; change them in .env and restart.",
			details: map[string]any{"fields": pinned, "keys": keys}}
	}

	// D76: binding a credential to the decision model or the judge leaves simulated mode. The
	// patch's own values apply after, so an explicit backend or provider in it wins.
	next := snap.Persisted
	for _, target := range []string{runtimecfg.TargetDecisionModel, runtimecfg.TargetJudge} {
		if name := patchString(patch, "bindings."+target); name != "" {
			next, _, _ = runtimecfg.ApplyBindings(next, name, []string{target}, snap.EnvManaged, false)
		}
	}
	next, err := runtimecfg.SetLeaves(next, map[string]json.RawMessage(patch))
	if err != nil {
		return plan, http.StatusUnprocessableEntity, &writeFailure{code: codeValidationFailed, message: "Some settings are not valid: " + err.Error()}
	}
	plan.persisted = next
	plan.changed = runtimecfg.ChangedLeaves(snap.Persisted, next)
	if len(plan.changed) == 0 {
		return plan, 0, nil
	}
	before, after := snap.Settings, s.rt.manager.Effective(next)

	fields, unavailable := s.checkSettings(snap, after, plan.changed)
	var overrideWarnings []settingsWarning
	if slices.Contains(plan.changed, "pricing_overrides") {
		for _, f := range validateOverrides(after.PricingOverrides) {
			if f.Warning {
				overrideWarnings = append(overrideWarnings, settingsWarning{Key: "pricing_overrides", Code: f.Code, Message: f.Message,
					Hint: "Check the price against the provider's price page."})
				continue
			}
			fields = append(fields, fieldError{Path: f.Path, Message: f.Message, Code: f.Code, Details: f.Details})
		}
	}
	if touchesUpstream(plan.changed) {
		errs, _ := upstreamRisks(after, snap.Credentials)
		for _, f := range errs {
			fields = append(fields, fieldError{Path: f.Path, Message: f.Message, Code: f.Code, Details: f.Details})
		}
	}
	if len(fields) > 0 {
		return plan, http.StatusUnprocessableEntity, &writeFailure{code: codeValidationFailed, message: "Some settings are not valid.", fields: fields}
	}
	if unavailable {
		return plan, http.StatusServiceUnavailable, &writeFailure{code: codeCredentialsUnavailable,
			message: "Saved keys can't be used here: the key store is not ready. Set TRUTHTABLE_SECRET_KEY and restart, or use a key from .env."}
	}

	// D41: an upstream key bound for every caller needs keys that callers authenticate with.
	if slices.Contains(plan.changed, "bindings.upstream_default") && after.Bindings.UpstreamDefault != nil {
		ok, err := s.ingestKeyExists(ctx)
		if err != nil {
			s.log.Error("ingest keys not counted", "err", err)
			return plan, http.StatusInternalServerError, &writeFailure{code: codeInternal, message: "The settings could not be checked; see the proxy log."}
		}
		if !ok {
			return plan, http.StatusConflict, &writeFailure{code: codeIngestKeyRequired,
				message: "Create an ingest API key first: with an upstream key bound, unauthenticated clients could spend it.",
				details: map[string]any{"field": ingestBindingPath}}
		}
	}

	// D77: a change that would block every answer on fail-closed routes needs a confirmation.
	if !confirmed {
		if msg, routes := s.settingsConfirmation(before, after); msg != "" {
			return plan, http.StatusConflict, &writeFailure{code: codeConfirmationRequired, message: msg,
				details: map[string]any{"routes": routes}}
		}
	}
	plan.warnings = overrideWarnings
	return plan, 0, nil
}

// ingestKeyExists reports whether an app key exists for an upstream key bound for every caller
// (D41).
func (s *server) ingestKeyExists(ctx context.Context) (bool, error) {
	// An app key: one that holds the ingest scope itself, from the env or the dashboard. Admin keys
	// do not count (every caller of this PATCH holds one, and an app should not use it; D95).
	if s.gateway != nil && s.gateway.auth != nil && s.gateway.auth.HasEnvIngestScope() {
		return true, nil
	}
	return s.rt.store.IngestKeyExists(ctx)
}

// touchesUpstream reports whether a changed path is an upstream setting.
func touchesUpstream(changed []string) bool {
	return slices.ContainsFunc(changed, func(p string) bool {
		return strings.HasPrefix(p, "upstream.") || p == "bindings.upstream_default"
	})
}

// patchString is the string a patch sets at path ("" when absent or null).
func patchString(patch settingsPatch, path string) string {
	var v *string
	if raw, ok := patch[path]; !ok || json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	return *v
}

// compactJSON is v without insignificant whitespace.
func compactJSON(v json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return v
	}
	return b.Bytes()
}

// Enum values of the runtime settings (api.ts RuntimeSettings).
var (
	settingsBackends = []string{"auto", jev.BackendOpenRouterDecisions, jev.BackendOpenRouterSystemOne, jev.BackendTypeSafe,
		jev.BackendCompatible, jev.BackendMock, jev.BackendOff}
	settingsJudgeProviders = []string{"auto", "anthropic", "openrouter", "openai", "ollama", "mock", "none"}
	settingsAuthHeaders    = []string{"authorization", "api-key", "x-api-key"}
	settingsFallbackFormat = []string{"openai", "anthropic"}
	// judgeCredentials are the credentials a judge can use (bindings.judge).
	judgeCredentials = []string{credentials.NameOpenRouter, credentials.NameAnthropic, credentials.NameOpenAI}
)

// upstreamSlots are the upstreams with their credential names (D41).
var upstreamSlots = []struct{ slot, credential string }{
	{"default", credentials.NameUpstreamDefault},
	{"anthropic", credentials.NameUpstreamAnthropic},
	{"fallback", credentials.NameUpstreamFallback},
}

// checkSettings checks the changed paths of the settings in effect after a write: enums, budgets,
// URLs (D78), the credentials bound and the origins they may go to (D37). unavailable reports a
// binding to a stored credential while the key store is not ready (503 credentials_unavailable).
func (s *server) checkSettings(snap *runtimecfg.Snapshot, after runtimecfg.RuntimeSettings, changed []string) (fields []fieldError, unavailable bool) {
	has := func(p string) bool { return slices.Contains(changed, p) }
	bad := func(path, code, msg string, details map[string]any) {
		fields = append(fields, fieldError{Path: path, Message: msg, Code: code, Details: details})
	}
	enum := func(path, value string, allowed []string) {
		if has(path) && !slices.Contains(allowed, value) {
			bad(path, fieldInvalidValue, "must be one of "+strings.Join(allowed, ", "), nil)
		}
	}
	enum("decision_model.backend", after.DecisionModel.Backend, settingsBackends)
	enum("judge.provider", after.Judge.Provider, settingsJudgeProviders)
	enum("upstream.default.auth_header", after.Upstream.Default.AuthHeader, settingsAuthHeaders)
	enum("upstream.anthropic.auth_header", after.Upstream.Anthropic.AuthHeader, settingsAuthHeaders)
	enum("upstream.fallback.auth_header", after.Upstream.Fallback.AuthHeader, settingsAuthHeaders)
	enum("upstream.fallback.format", after.Upstream.Fallback.Format, settingsFallbackFormat)
	for path, v := range map[string]float64{"budgets.decision_usd_day": after.Budgets.DecisionUSDDay, "budgets.llm_usd_day": after.Budgets.LLMUSDDay} {
		if has(path) && (v < 0 || math.IsNaN(v) || math.IsInf(v, 0)) {
			bad(path, fieldInvalidValue, "must be 0 (no cap) or more", nil)
		}
	}

	// The credentials bound: a known name of the right kind, usable in this snapshot.
	credential := func(path string, name *string, allowed []string, what string) {
		if !has(path) || name == nil {
			return
		}
		if _, known := credentials.Lookup(*name); !known {
			bad(path, fieldUnknownCredential, fmt.Sprintf("%q is not a credential name", *name), nil)
			return
		}
		if !slices.Contains(allowed, *name) {
			bad(path, fieldCredentialKind, what+" can use only "+strings.Join(allowed, ", "), map[string]any{"names": []string{*name}})
			return
		}
		if _, ok := snap.Credentials[*name]; ok {
			return
		}
		switch {
		case slices.Contains(snap.Undecryptable, *name) && snap.StoreState == credentials.StateReady:
			bad(path, fieldCredentialUndecrypt, "the saved "+*name+" key can't be read with the current master key; paste it again",
				map[string]any{"names": []string{*name}})
		case snap.StoreState != credentials.StateReady:
			unavailable = true
		default:
			bad(path, fieldCredentialMissing, "no "+*name+" key is saved; add it under Connections first", map[string]any{"names": []string{*name}})
		}
	}
	credential("bindings.decision_model", after.Bindings.DecisionModel,
		[]string{credentials.NameOpenRouter, credentials.NameTypeSafe, credentials.NameCompatible}, "The fast check")
	credential("bindings.judge", after.Bindings.Judge, judgeCredentials, "The judge")
	upstreams := map[string]runtimecfg.UpstreamSetting{
		"default": after.Upstream.Default, "anthropic": after.Upstream.Anthropic, "fallback": after.Upstream.Fallback.UpstreamSetting,
	}
	for _, u := range upstreamSlots {
		credential("upstream."+u.slot+".credential", upstreams[u.slot].Credential, []string{u.credential}, "The "+u.slot+" upstream")
	}
	if has("bindings.decision_model") && after.Bindings.DecisionModel != nil && snap.BackendPinned(*after.Bindings.DecisionModel) {
		bad("bindings.decision_model", fieldBackendPinned, fmt.Sprintf("TRUTHTABLE_JEV_BACKEND=%s is set in .env and does not use the %s key",
			after.DecisionModel.Backend, *after.Bindings.DecisionModel), map[string]any{"names": []string{*after.Bindings.DecisionModel}})
	}

	// URLs (D78) and the origins of the credential bound to them (D37).
	s.checkURL(&fields, snap, "decision_model.base_url", after.DecisionModel.BaseURL, after.Bindings.DecisionModel,
		has("decision_model.base_url"), has("bindings.decision_model"), true)
	for _, u := range upstreamSlots {
		up := upstreams[u.slot]
		target := up.URL
		if target == nil && up.Credential != nil {
			// A null URL is the built-in one: the bound key must be allowed to go there too.
			target = s.builtinUpstreamURL(u.slot)
		}
		s.checkURL(&fields, snap, "upstream."+u.slot+".url", target, up.Credential,
			has("upstream."+u.slot+".url"), has("upstream."+u.slot+".credential"), up.Credential != nil)
	}
	return fields, unavailable
}

// builtinUpstreamURL is the URL an upstream uses when its setting is null: the registry default
// of its env key (the OpenAI API for the default upstream, the Anthropic API for anthropic); nil
// for the fallback, which a null URL turns off.
func (s *server) builtinUpstreamURL(slot string) *string {
	def := ""
	switch slot {
	case "default":
		def = config.DefaultUpstreamURL
	case "anthropic":
		if reg := s.cfg.Registry(); reg != nil {
			if k, ok := reg.Lookup("TRUTHTABLE_ANTHROPIC_UPSTREAM_URL"); ok {
				def = k.DefaultValue()
			}
		}
	}
	if def == "" {
		return nil
	}
	return &def
}

// isHTTP reports a URL with the http scheme.
func isHTTP(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "http://")
}

// checkURL checks a URL setting at path when the URL or the credential bound to it changed: it
// must pass credentials.CheckURL (an http URL to a host that is not local only when no key goes
// with it, keyed false), and its origin must be one the bound credential may go to (D37: a
// dashboard compatible or upstream key answers reenter_secret).
func (s *server) checkURL(fields *[]fieldError, snap *runtimecfg.Snapshot, path string, raw, bound *string, urlChanged, credChanged, keyed bool) {
	if raw == nil || !(urlChanged || credChanged) {
		return
	}
	u, err := credentials.CheckURL(*raw)
	if err != nil {
		var ue *credentials.URLError
		if errors.As(err, &ue) && ue.Code == credentials.CodeInsecureURL && !keyed && isHTTP(*raw) {
			return // plain http to the client's own upstream: no key of ours goes there
		}
		code := fieldInvalidValue
		if ue != nil {
			code = ue.Code
		}
		*fields = append(*fields, fieldError{Path: path, Message: err.Error(), Code: code})
		return
	}
	if bound == nil {
		return
	}
	c, ok := snap.Credentials[*bound]
	if !ok {
		return // checkSettings reports a missing credential
	}
	origin := jev.CanonicalOrigin(u)
	if slices.Contains(c.AllowedOrigins, origin) {
		return
	}
	names := map[string]any{"names": []string{*bound}}
	if spec, _ := credentials.Lookup(*bound); spec.CustomOrigin && c.Source == credentials.SourceDashboard {
		*fields = append(*fields, fieldError{Path: path, Code: fieldReenterSecret, Details: names,
			Message: fmt.Sprintf("the %s key was saved for another address; paste it again to send it to %s", *bound, origin)})
		return
	}
	*fields = append(*fields, fieldError{Path: path, Code: fieldOriginNotAllowed, Details: names,
		Message: fmt.Sprintf("the %s key may not be sent to %s", *bound, origin)})
}
