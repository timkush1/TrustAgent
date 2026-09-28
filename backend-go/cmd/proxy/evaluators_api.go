package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// Evaluator read endpoints (SPEC §2.4 rows 27-29, plus a version diff for Studio's "Save
// version"): the registry's evaluators with the routes that attach them, their versions and
// their calibration progress. Evaluator writes and validation arrive with Studio (P4).

// Calibration thresholds (SPEC §2.11 quality measured; §7.3 quick calibration).
const (
	measuredMinN   = 200
	measuredMinPos = 30
	measuredMinNeg = 30
	provisionalN   = 8
	provisionalPer = 3
)

// labelCounter counts human labels (*store.PostgresStore); nil without a database.
type labelCounter interface {
	HumanLabelCounts(ctx context.Context, target string) (store.LabelCounts, error)
}

// evaluatorsAPI serves /api/evaluators.
type evaluatorsAPI struct {
	registry *evaluators.Registry
	// policy returns the active policy (for attached_routes).
	policy func() *policy.Policy
	labels labelCounter
	// runtime gives the snapshot and calibrations (calibration_progress).
	runtime func() policy.Runtime
	// started stands in for stored times when there is no database.
	started time.Time
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
	return &evaluatorsAPI{registry: reg, policy: active, labels: labels, runtime: runtime, started: time.Now().UTC(), log: log}
}

// register mounts the evaluator routes on the /api group.
func (a *evaluatorsAPI) register(api *gin.RouterGroup, g apiGuards) {
	api.GET("/evaluators", g.read, g.rateLimit, a.list)
	api.GET("/evaluators/:id", g.read, g.rateLimit, a.get)
	api.GET("/evaluators/:id/versions/:v", g.read, g.rateLimit, a.version)
	api.GET("/evaluators/:id/diff", g.read, g.rateLimit, a.diff)
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

// progress is the calibration progress of an evaluator version: human labels against the
// measured-quality minimums.
func (a *evaluatorsAPI) progress(ctx context.Context, v *evaluators.Version) calibrationProgress {
	rt := a.runtime()
	ref := v.Ref()
	p := calibrationProgress{EvaluatorRef: ref, Needed: labelNeeds{N: measuredMinN, Pos: measuredMinPos, Neg: measuredMinNeg}, State: policy.CalibrationNone}
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
		counts, err := a.labels.HumanLabelCounts(ctx, ref)
		if err != nil {
			a.log.Warn("label counts unavailable", "evaluator", ref, "err", err)
		} else {
			p.HumanLabels = labelNeeds{N: counts.N, Pos: counts.Pos, Neg: counts.Neg}
		}
	}
	h := p.HumanLabels
	p.CanFitMeasured = h.N >= measuredMinN && h.Pos >= measuredMinPos && h.Neg >= measuredMinNeg
	p.CanFitProvisional = h.N >= provisionalN && h.Pos >= provisionalPer && h.Neg >= provisionalPer
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
