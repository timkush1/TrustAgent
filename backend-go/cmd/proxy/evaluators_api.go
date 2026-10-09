package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// Evaluator endpoints (SPEC §2.4 rows 27-33, plus a version diff for Studio's "Save version"):
// the registry's evaluators with the routes that attach them, their versions and their
// calibration progress; validation; and the dashboard writes: a new evaluator, a new version of a
// dashboard evaluator, and archiving (D57).

// labelCounter counts the labels of a calibration target (*store.PostgresStore); nil without a
// database.
type labelCounter interface {
	CalibrationLabelCounts(ctx context.Context, f store.LabelCountFilter) (store.LabelCounts, error)
}

// evaluatorsAPI serves /api/evaluators.
type evaluatorsAPI struct {
	registry *evaluators.Registry
	// policy returns the active policy (for attached_routes).
	policy func() *policy.Policy
	labels labelCounter
	// runtime gives the snapshot and calibrations (calibration_progress).
	runtime func() policy.Runtime
	// writes are the evaluator writes (#31-33); nil serves them as 503 persistence_disabled.
	writes *evaluatorWrites
	// includeMock counts uniform audits of simulated traffic in calibration_progress.proxy_labels
	// (the demo), as GET /api/gate/reliability does.
	includeMock bool
	// cpt is the characters-per-token ratio of question_tokens (TRUTHTABLE_JEV_CHARS_PER_TOKEN).
	cpt float64
	// started stands in for stored times when there is no database.
	started time.Time
	now     func() time.Time
	log     *slog.Logger
}

// newEvaluatorsAPI builds the evaluator endpoints. labels and runtime may be nil.
func newEvaluatorsAPI(reg *evaluators.Registry, active func() *policy.Policy, labels labelCounter, runtime func() policy.Runtime, log *slog.Logger) *evaluatorsAPI {
	if runtime == nil {
		runtime = func() policy.Runtime { return policy.Runtime{Calibrations: policy.NoCalibration{}} }
	}
	if log == nil {
		log = slog.Default()
	}
	return &evaluatorsAPI{registry: reg, policy: active, labels: labels, runtime: runtime, cpt: decision.DefaultCPT,
		started: time.Now().UTC(), now: time.Now, log: log}
}

// register mounts the evaluator routes on the /api group.
func (a *evaluatorsAPI) register(api *gin.RouterGroup, g apiGuards) {
	api.GET("/evaluators", g.read, g.rateLimit, a.list)
	api.GET("/evaluators/:id", g.read, g.rateLimit, a.get)
	api.GET("/evaluators/:id/versions/:v", g.read, g.rateLimit, a.version)
	api.GET("/evaluators/:id/diff", g.read, g.rateLimit, a.diff)
	// Read-only mode refuses every non-GET /api/* request (SPEC §2.4), the read-scope validate
	// included. CONFIG_LOCK does not block evaluator writes (D57).
	api.POST("/evaluators/validate", g.writable, g.read, g.rateLimit, g.bodyLimit, a.validate)
	api.POST("/evaluators", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.create)
	api.POST("/evaluators/:id/versions", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.createVersion)
	api.POST("/evaluators/:id/archive", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.archive)
}

// attachedRoute is EvaluatorMeta.attached_routes[].
type attachedRoute struct {
	Route string `json:"route"`
	Stage string `json:"stage"`
}

// evaluatorMeta is api.ts EvaluatorMeta.
type evaluatorMeta struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Category       string          `json:"category"`
	Origin         string          `json:"origin"`
	LatestVersion  int             `json:"latest_version"`
	Archived       bool            `json:"archived"`
	AttachedRoutes []attachedRoute `json:"attached_routes"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// evaluatorVersionMeta is api.ts Omit<EvaluatorVersion, 'spec'>.
type evaluatorVersionMeta struct {
	EvaluatorID    string    `json:"evaluator_id"`
	Version        int       `json:"version"`
	SpecSHA256     string    `json:"spec_sha256"`
	QuestionSHA256 string    `json:"question_sha256"`
	CalibrationKey string    `json:"calibration_key"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	Changelog      string    `json:"changelog"`
}

// evaluatorVersion is api.ts EvaluatorVersion.
type evaluatorVersion struct {
	evaluatorVersionMeta
	Spec *decision.Spec `json:"spec"`
}

// labelNeeds is CalibrationProgress.needed.
type labelNeeds struct {
	N   int `json:"n"`
	Pos int `json:"pos"`
	Neg int `json:"neg"`
}

// calibrationProgress is api.ts CalibrationProgress.
type calibrationProgress struct {
	EvaluatorRef      string     `json:"evaluator_ref"`
	Snapshot          *string    `json:"snapshot"`
	HumanLabels       labelNeeds `json:"human_labels"`
	Needed            labelNeeds `json:"needed"`
	ProxyLabels       int        `json:"proxy_labels"`
	State             string     `json:"state"`
	CanFitMeasured    bool       `json:"can_fit_measured"`
	CanFitProvisional bool       `json:"can_fit_provisional"`
}

// evaluatorDetail is api.ts EvaluatorDetail.
type evaluatorDetail struct {
	evaluatorMeta
	Versions            []evaluatorVersionMeta `json:"versions"`
	Latest              evaluatorVersion       `json:"latest"`
	CalibrationProgress calibrationProgress    `json:"calibration_progress"`
}

func (a *evaluatorsAPI) meta(e *evaluators.Evaluator) evaluatorMeta {
	m := evaluatorMeta{
		ID: e.ID, Name: e.Name, Category: e.Category, Origin: e.Origin, LatestVersion: e.LatestVersion, Archived: e.Archived,
		AttachedRoutes: []attachedRoute{}, UpdatedAt: e.UpdatedAt,
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = a.started
	}
	if a.policy == nil {
		return m
	}
	p := a.policy()
	if p == nil {
		return m
	}
	for _, r := range p.AllRoutes() {
		for _, ref := range r.Evaluators {
			id, _, err := decision.ParseRef(ref.Ref)
			if err != nil || id != e.ID || !ref.IsEnabled() {
				continue
			}
			m.AttachedRoutes = append(m.AttachedRoutes, attachedRoute{Route: r.Name, Stage: ref.StageOrDefault()})
			break
		}
	}
	return m
}

func (a *evaluatorsAPI) versionMeta(v *evaluators.Version) evaluatorVersionMeta {
	m := evaluatorVersionMeta{
		EvaluatorID: v.Spec.ID, Version: v.Spec.Version, SpecSHA256: v.Hashes.SpecSHA256, QuestionSHA256: v.Hashes.QuestionSHA256,
		CalibrationKey: v.Hashes.CalibrationKey, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, Changelog: v.Changelog,
	}
	if m.CreatedBy == "" {
		m.CreatedBy = "system"
		if v.Source == evaluators.SourceFile {
			m.CreatedBy = "file:" + v.File
		}
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = a.started
	}
	return m
}

// GET /api/evaluators?include_archived=true
func (a *evaluatorsAPI) list(c *gin.Context) {
	include := c.Query("include_archived") == "true" || c.Query("include_archived") == "1"
	items := []evaluatorMeta{}
	for _, e := range a.registry.Evaluators(include) {
		items = append(items, a.meta(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (a *evaluatorsAPI) evaluator(c *gin.Context) (*evaluators.Evaluator, bool) {
	e, ok := a.registry.Evaluator(c.Param("id"))
	if !ok {
		abortAPI(c, http.StatusNotFound, codeNotFound, "No such evaluator.")
		return nil, false
	}
	return e, true
}

// GET /api/evaluators/:id
func (a *evaluatorsAPI) get(c *gin.Context) {
	e, ok := a.evaluator(c)
	if !ok {
		return
	}
	latest := e.Latest()
	d := evaluatorDetail{
		evaluatorMeta: a.meta(e), Versions: make([]evaluatorVersionMeta, 0, len(e.Versions)),
		Latest: evaluatorVersion{evaluatorVersionMeta: a.versionMeta(latest), Spec: latest.Spec},
	}
	for _, v := range e.Versions {
		d.Versions = append(d.Versions, a.versionMeta(v))
	}
	d.CalibrationProgress = a.progress(c.Request.Context(), latest)
	c.JSON(http.StatusOK, d)
}

// fitWindow is the label window of calibration progress and of POST /api/calibration/fit: the
// labels of the last 30 days (the longest window the dashboard names), so the progress meter
// counts exactly the labels a fit would use.
const (
	fitWindow     = "30d"
	fitWindowSpan = 30 * 24 * time.Hour
)

// progress is the calibration progress of an evaluator version on the current snapshot: its human
// labels of the last 30 days against the measured-quality minimums (the labels POST
// /api/calibration/fit would use), and the uniform-audit proxy labels.
func (a *evaluatorsAPI) progress(ctx context.Context, v *evaluators.Version) calibrationProgress {
	rt := a.runtime()
	ref := v.Ref()
	p := calibrationProgress{
		EvaluatorRef: ref, State: policy.CalibrationNone,
		Needed: labelNeeds{N: decision.MeasuredMinLabels, Pos: decision.MeasuredMinPerClass, Neg: decision.MeasuredMinPerClass},
	}
	if rt.Snapshot != "" {
		snap := rt.Snapshot
		p.Snapshot = &snap
	}
	if rt.Calibrations != nil {
		if st := rt.Calibrations.Lookup(ref, nil, rt.Snapshot).State; st != "" {
			p.State = st
		}
	}
	if a.labels != nil {
		to := a.now().UTC()
		counts, err := a.labels.CalibrationLabelCounts(ctx, store.LabelCountFilter{
			Target: ref, Snapshot: rt.Snapshot, From: to.Add(-fitWindowSpan), To: to, IncludeMock: a.includeMock,
		})
		if err != nil {
			a.log.Warn("label counts unavailable", "evaluator", ref, "err", err)
		} else {
			p.HumanLabels = labelNeeds{N: counts.N, Pos: counts.Pos, Neg: counts.Neg}
			p.ProxyLabels = counts.Proxy
		}
	}
	h := p.HumanLabels
	p.CanFitMeasured = h.N >= decision.MeasuredMinLabels && h.Pos >= decision.MeasuredMinPerClass && h.Neg >= decision.MeasuredMinPerClass
	p.CanFitProvisional = h.N >= decision.MinFitLabels && h.Pos >= decision.MinFitPerClass && h.Neg >= decision.MinFitPerClass
	return p
}

func (a *evaluatorsAPI) versionOf(c *gin.Context, e *evaluators.Evaluator, raw, name string) (*evaluators.Version, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		badParam(c, name, "must be a positive integer version")
		return nil, false
	}
	v, ok := e.Version(n)
	if !ok {
		abortAPI(c, http.StatusNotFound, codeNotFound, "No such evaluator version.")
		return nil, false
	}
	return v, true
}

// GET /api/evaluators/:id/versions/:v
func (a *evaluatorsAPI) version(c *gin.Context) {
	e, ok := a.evaluator(c)
	if !ok {
		return
	}
	v, ok := a.versionOf(c, e, c.Param("v"), "v")
	if !ok {
		return
	}
	c.JSON(http.StatusOK, evaluatorVersion{evaluatorVersionMeta: a.versionMeta(v), Spec: v.Spec})
}

// evaluatorDiff is the diff of two versions' specs (pretty JSON, keys sorted).
type evaluatorDiff struct {
	EvaluatorID string `json:"evaluator_id"`
	From        int    `json:"from"`
	To          int    `json:"to"`
	Diff        string `json:"diff"`
}

// GET /api/evaluators/:id/diff?from=1&to=2 (to defaults to the latest version, from to the one
// before it)
func (a *evaluatorsAPI) diff(c *gin.Context) {
	e, ok := a.evaluator(c)
	if !ok {
		return
	}
	to := e.Latest()
	if raw := c.Query("to"); raw != "" {
		if to, ok = a.versionOf(c, e, raw, "to"); !ok {
			return
		}
	}
	from := to
	if raw := c.Query("from"); raw != "" {
		if from, ok = a.versionOf(c, e, raw, "from"); !ok {
			return
		}
	} else if i := slices.Index(e.Versions, to); i > 0 {
		from = e.Versions[i-1]
	}
	fromText, err := prettySpec(from.Spec)
	if err == nil {
		var toText string
		if toText, err = prettySpec(to.Spec); err == nil {
			c.JSON(http.StatusOK, evaluatorDiff{EvaluatorID: e.ID, From: from.Spec.Version, To: to.Spec.Version,
				Diff: policy.UnifiedDiff(from.Ref(), to.Ref(), fromText, toText)})
			return
		}
	}
	a.log.Error("evaluator diff failed", "evaluator", e.ID, "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request failed; see the proxy log.")
}

func prettySpec(s *decision.Spec) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// ---------------------------------------------------------------------------------------------
// Validation and writes (SPEC §2.4 rows 30-33, D54, D57)

// maxChangelogRunes bounds a version's changelog.
const maxChangelogRunes = 1000

// evaluatorWriter stores evaluator writes (*store.PostgresStore).
type evaluatorWriter interface {
	CreateEvaluator(ctx context.Context, channel string, e store.EvaluatorRecord, v *store.EvaluatorVersionRecord, audit store.AdminAuditEntry) error
	CreateEvaluatorVersion(ctx context.Context, channel string, base int, name, category string, v *store.EvaluatorVersionRecord, audit store.AdminAuditEntry) error
	SetEvaluatorArchived(ctx context.Context, channel, id string, archived bool, audit store.AdminAuditEntry) (*store.EvaluatorRecord, error)
}

// evaluatorWrites are the dependencies of the evaluator writes.
type evaluatorWrites struct {
	store evaluatorWriter
	// channel is the NOTIFY channel of the registry (store.ConfigChannel when empty).
	channel string
	// reload re-reads the registry synchronously, so the reply's version already resolves (a PUT
	// /api/policies right after it can attach it), and announces the change on the WebSocket.
	reload func(ctx context.Context) error
}

// evaluatorValidateResult is api.ts EvaluatorValidateResult.
type evaluatorValidateResult struct {
	Errors         []decision.FieldError `json:"errors"`
	Lints          []decision.Lint       `json:"lints"`
	QuestionSHA256 *string               `json:"question_sha256"`
	CalibrationKey *string               `json:"calibration_key"`
	QuestionTokens *int                  `json:"question_tokens"`
}

// specBody is {spec} plus the write fields.
type specBody struct {
	Spec        json.RawMessage `json:"spec"`
	Changelog   string          `json:"changelog"`
	BaseVersion *int            `json:"base_version"`
}

// knownID reports whether an evaluator id is registered (archived ones included), for the
// unknown_evaluator_reference rule.
func (a *evaluatorsAPI) knownID(id string) bool {
	_, ok := a.registry.Evaluator(id)
	return ok
}

// validateSpec runs the schema, the hard rules and the lints over a spec document.
func (a *evaluatorsAPI) validateSpec(doc json.RawMessage) decision.Validation {
	return decision.ValidateSpec(doc, decision.LintOptions{Registry: a.knownID})
}

// POST /api/evaluators/validate (#30): schema errors and lints; the hashes and the question's
// token estimate once the spec is valid.
func (a *evaluatorsAPI) validate(c *gin.Context) {
	var body struct {
		Spec json.RawMessage `json:"spec"`
	}
	if !bindJSON(c, &body, false) {
		return
	}
	if len(body.Spec) == 0 || string(body.Spec) == "null" {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "spec is required: the evaluator spec to check.",
			fieldError{Path: "spec", Message: "required"})
		return
	}
	v := a.validateSpec(body.Spec)
	res := evaluatorValidateResult{Errors: v.Errors, Lints: v.Lints}
	if v.Hashes != nil && v.Spec != nil {
		q, k := v.Hashes.QuestionSHA256, v.Hashes.CalibrationKey
		res.QuestionSHA256, res.CalibrationKey = &q, &k
		if n, err := decision.Est(v.Spec.QuestionBody(), a.questionCPT()); err == nil {
			res.QuestionTokens = &n
		}
	}
	c.JSON(http.StatusOK, res)
}

func (a *evaluatorsAPI) questionCPT() float64 {
	if a.cpt > 0 {
		return a.cpt
	}
	return decision.DefaultCPT
}

// writable answers 503 persistence_disabled when there is no database to write to.
func (a *evaluatorsAPI) writable(c *gin.Context) bool {
	if a.writes == nil || a.writes.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Saving evaluators needs a database (TRUTHTABLE_DATABASE_URL).")
		return false
	}
	return true
}

// checkedSpec validates a spec for saving: it must pass the schema and every hard rule, and a
// dashboard spec is never a built-in. On failure it answers 422 validation_failed with one field
// error per finding and returns nil.
func (a *evaluatorsAPI) checkedSpec(c *gin.Context, body specBody) *decision.Validation {
	if len(body.Spec) == 0 || string(body.Spec) == "null" {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "spec is required: the evaluator spec to save.",
			fieldError{Path: "spec", Message: "required"})
		return nil
	}
	if utf8.RuneCountInString(body.Changelog) > maxChangelogRunes {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The changelog is too long.",
			fieldError{Path: "changelog", Message: "at most " + strconv.Itoa(maxChangelogRunes) + " characters"})
		return nil
	}
	v := a.validateSpec(body.Spec)
	var fields []fieldError
	for _, e := range v.Errors {
		fields = append(fields, fieldError{Path: specPath(e.Path), Message: e.Message})
	}
	for _, l := range v.HardErrors() {
		fields = append(fields, fieldError{Path: specPath(l.Path), Message: l.Message, Code: l.Code})
	}
	var flags struct {
		Builtin bool `json:"builtin"`
	}
	if json.Unmarshal(body.Spec, &flags) == nil && flags.Builtin {
		fields = append(fields, fieldError{Path: "spec.builtin", Message: "must be false: only TrustAgent's own templates are built-in"})
	}
	if v.Spec != nil && v.Spec.ID == decision.DraftID {
		fields = append(fields, fieldError{Path: "spec.id", Message: decision.DraftID + " is reserved for Studio drafts"})
	}
	if len(fields) > 0 || v.Spec == nil || v.Hashes == nil {
		if len(fields) == 0 {
			fields = append(fields, fieldError{Path: "spec", Message: "is not a valid evaluator spec"})
		}
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The evaluator spec does not validate.", fields...)
		return nil
	}
	return &v
}

// specPath is a spec finding's path inside the request body.
func specPath(p string) string {
	if p == "" {
		return "spec"
	}
	return "spec." + p
}

// versionRecord is the evaluator_versions row of a validated spec.
func versionRecord(v *decision.Validation, by, changelog string) (*store.EvaluatorVersionRecord, error) {
	doc, err := json.Marshal(v.Spec)
	if err != nil {
		return nil, err
	}
	return &store.EvaluatorVersionRecord{
		EvaluatorID: v.Spec.ID, Version: v.Spec.Version, Spec: doc, SpecSHA256: v.Hashes.SpecSHA256,
		QuestionSHA256: v.Hashes.QuestionSHA256, CalibrationKey: v.Hashes.CalibrationKey, CreatedBy: by,
		Changelog: strings.TrimSpace(changelog),
	}, nil
}

// versionResponse is the EvaluatorVersion of a stored version.
func versionResponse(rec *store.EvaluatorVersionRecord, spec *decision.Spec) evaluatorVersion {
	return evaluatorVersion{
		evaluatorVersionMeta: evaluatorVersionMeta{
			EvaluatorID: rec.EvaluatorID, Version: rec.Version, SpecSHA256: rec.SpecSHA256, QuestionSHA256: rec.QuestionSHA256,
			CalibrationKey: rec.CalibrationKey, CreatedBy: rec.CreatedBy, CreatedAt: rec.CreatedAt, Changelog: rec.Changelog,
		},
		Spec: spec,
	}
}

// reloaded re-reads the registry after a write. A failed reload is logged, not returned: the write
// is stored and the poll picks it up.
func (a *evaluatorsAPI) reloaded(c *gin.Context, what string) {
	if a.writes.reload == nil {
		return
	}
	if err := a.writes.reload(c.Request.Context()); err != nil {
		a.log.Warn("evaluator reload after a write failed; the poll retries", "write", what, "err", err)
	}
}

func auditEntry(c *gin.Context, action, target string, diff any) store.AdminAuditEntry {
	return store.AdminAuditEntry{Actor: actor(c), Action: action, Target: target, Diff: diff}
}

// POST /api/evaluators (#31): a new dashboard evaluator at version 1. 409 when the id exists
// anywhere (built-in, file or dashboard, archived or not).
func (a *evaluatorsAPI) create(c *gin.Context) {
	var body specBody
	if !bindJSON(c, &body, false) || !a.writable(c) {
		return
	}
	if body.BaseVersion != nil {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "base_version belongs to POST /api/evaluators/:id/versions.",
			fieldError{Path: "base_version", Message: "not accepted here"})
		return
	}
	v := a.checkedSpec(c, body)
	if v == nil {
		return
	}
	if v.Spec.Version != 1 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "A new evaluator starts at version 1.",
			fieldError{Path: "spec.version", Message: "must be 1 for a new evaluator"})
		return
	}
	id := v.Spec.ID
	if _, exists := a.registry.Evaluator(id); exists {
		a.idExists(c, id)
		return
	}
	rec, err := versionRecord(v, actor(c), body.Changelog)
	if err != nil {
		a.internal(c, "create evaluator", err)
		return
	}
	err = a.writes.store.CreateEvaluator(c.Request.Context(), a.writes.channel,
		store.EvaluatorRecord{ID: id, Name: v.Spec.Name, Category: v.Spec.Category}, rec,
		auditEntry(c, "evaluator.create", rec.Ref(), map[string]any{"version": rec.Version, "spec_sha256": rec.SpecSHA256, "changelog": rec.Changelog}))
	switch {
	case errors.Is(err, store.ErrEvaluatorExists):
		a.idExists(c, id)
		return
	case err != nil:
		a.internal(c, "create evaluator", err)
		return
	}
	a.log.Info("evaluator created", "ref", rec.Ref(), "by", rec.CreatedBy)
	a.reloaded(c, "create")
	c.JSON(http.StatusCreated, versionResponse(rec, v.Spec))
}

func (a *evaluatorsAPI) idExists(c *gin.Context, id string) {
	abortAPIDetails(c, http.StatusConflict, codeConflict,
		"An evaluator named "+id+" already exists. Choose another id, or save a new version of it.",
		map[string]any{"names": []string{id}})
}

// POST /api/evaluators/:id/versions (#32): the next version of a dashboard evaluator. base_version
// is the latest version the editor started from: 412 when another version was saved since. 403
// for built-ins, 409 file_managed for file evaluators.
func (a *evaluatorsAPI) createVersion(c *gin.Context) {
	var body specBody
	if !bindJSON(c, &body, false) || !a.writable(c) {
		return
	}
	id := c.Param("id")
	e, ok := a.evaluator(c)
	if !ok {
		return
	}
	switch {
	case a.registry.IsBuiltin(id) || e.Origin == evaluators.OriginBuiltin:
		a.builtinReadOnly(c, id)
		return
	case e.Origin == evaluators.OriginFile:
		a.fileManaged(c, id)
		return
	case body.BaseVersion == nil || *body.BaseVersion < 1:
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "base_version is required: the latest version your edit started from.",
			fieldError{Path: "base_version", Message: "required positive integer"})
		return
	}
	base := *body.BaseVersion
	v := a.checkedSpec(c, body)
	if v == nil {
		return
	}
	switch {
	case v.Spec.ID != id:
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The spec's id must be "+id+".",
			fieldError{Path: "spec.id", Message: "must be " + id})
		return
	case v.Spec.Version != base+1:
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed,
			"The new version must be "+strconv.Itoa(base+1)+" (base_version + 1).",
			fieldError{Path: "spec.version", Message: "must be " + strconv.Itoa(base+1)})
		return
	}
	rec, err := versionRecord(v, actor(c), body.Changelog)
	if err != nil {
		a.internal(c, "create evaluator version", err)
		return
	}
	var stale *store.StaleEvaluatorError
	err = a.writes.store.CreateEvaluatorVersion(c.Request.Context(), a.writes.channel, base, v.Spec.Name, v.Spec.Category, rec,
		auditEntry(c, "evaluator.version", rec.Ref(), map[string]any{"base_version": base, "spec_sha256": rec.SpecSHA256, "changelog": rec.Changelog}))
	switch {
	case errors.As(err, &stale):
		abortAPIDetails(c, http.StatusPreconditionFailed, codeStaleVersion,
			"Version "+strconv.Itoa(stale.Latest)+" of "+id+" was saved since you started editing: reload it, reapply your edit and save again.",
			map[string]any{"names": []string{id}, "latest_version": stale.Latest})
		return
	case errors.Is(err, store.ErrEvaluatorBuiltin):
		a.builtinReadOnly(c, id)
		return
	case errors.Is(err, store.ErrEvaluatorFileManaged):
		a.fileManaged(c, id)
		return
	case errors.Is(err, store.ErrNotFound):
		abortAPI(c, http.StatusNotFound, codeNotFound, "No such evaluator.")
		return
	case err != nil:
		a.internal(c, "create evaluator version", err)
		return
	}
	a.log.Info("evaluator version saved", "ref", rec.Ref(), "by", rec.CreatedBy)
	a.reloaded(c, "version")
	c.JSON(http.StatusCreated, versionResponse(rec, v.Spec))
}

func (a *evaluatorsAPI) builtinReadOnly(c *gin.Context, id string) {
	abortAPIDetails(c, http.StatusForbidden, codeForbidden,
		id+" is built in and read-only. Duplicate it to make your own version.", map[string]any{"names": []string{id}})
}

func (a *evaluatorsAPI) fileManaged(c *gin.Context, id string) {
	abortAPIDetails(c, http.StatusConflict, codeFileManaged,
		id+" comes from a file in the evaluators directory: change the file instead.", map[string]any{"names": []string{id}})
}

// POST /api/evaluators/:id/archive (#33): archive or restore an evaluator. 409 while a route of
// the active policy attaches it (in any stage): archiving would leave the route pointing at a
// hidden check.
func (a *evaluatorsAPI) archive(c *gin.Context) {
	var body struct {
		Archived *bool `json:"archived"`
	}
	if !bindJSON(c, &body, false) || !a.writable(c) {
		return
	}
	if body.Archived == nil {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "archived is required: true to archive, false to restore.",
			fieldError{Path: "archived", Message: "required boolean"})
		return
	}
	e, ok := a.evaluator(c)
	if !ok {
		return
	}
	if *body.Archived {
		if routes := a.meta(e).AttachedRoutes; len(routes) > 0 {
			names := make([]string, len(routes))
			for i, r := range routes {
				names[i] = r.Route
			}
			abortAPIDetails(c, http.StatusConflict, codeConflict,
				e.ID+" is attached to "+strings.Join(names, ", ")+": remove it from the policy first.",
				map[string]any{"names": []string{e.ID}, "routes": names})
			return
		}
	}
	rec, err := a.writes.store.SetEvaluatorArchived(c.Request.Context(), a.writes.channel, e.ID, *body.Archived,
		auditEntry(c, "evaluator.archive", e.ID, map[string]any{"archived": *body.Archived}))
	switch {
	case errors.Is(err, store.ErrNotFound):
		abortAPI(c, http.StatusNotFound, codeNotFound, "No such evaluator.")
		return
	case err != nil:
		a.internal(c, "archive evaluator", err)
		return
	}
	a.log.Info("evaluator archive flag set", "id", e.ID, "archived", rec.Archived, "by", actor(c))
	a.reloaded(c, "archive")
	if cur, ok := a.registry.Evaluator(e.ID); ok {
		e = cur
	}
	m := a.meta(e)
	m.Archived, m.UpdatedAt = rec.Archived, rec.UpdatedAt
	c.JSON(http.StatusOK, m)
}

func (a *evaluatorsAPI) internal(c *gin.Context, what string, err error) {
	a.log.Error(what+" failed", "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request failed; see the proxy log.")
}
