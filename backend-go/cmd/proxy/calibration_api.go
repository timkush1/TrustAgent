package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/calibration"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// Calibration records (SPEC §2.4 rows 42-46b, §2.11; D55, D62): list and read the stored
// calibrations, import a calibration file, fit one from the label store, and activate or
// deactivate a record. Activating changes which calibration applies to a target; it does not
// change live decisions while the route's threshold space is raw ("Adopt" is a policy save).
// Every write reloads the lookup before it replies and broadcasts calibration_updated.

// maxPreviewCalibrations bounds a dry-run's calibration_ids.
const maxPreviewCalibrations = 50

// Fit label sources (CalibrationFitRequest.sources).
const (
	fitSourceLabels       = "labels"
	fitSourceUniformAudit = "uniform_audit"
)

// Import rejection reasons (CalibrationImportResult.rejected[].reason).
const (
	rejectUnknownVersion = "unknown_version"
	rejectKeyMismatch    = "calibration_key_mismatch"
	rejectMockSnapshot   = "mock_snapshot"
	rejectInvalid        = "invalid"
)

// calibrationStore is the part of the store the calibration endpoints use (*store.PostgresStore).
type calibrationStore interface {
	ListCalibrations(ctx context.Context, f store.CalibrationFilter) ([]store.CalibrationRecord, error)
	GetCalibration(ctx context.Context, id int64) (*store.CalibrationRecord, error)
	CalibrationsByID(ctx context.Context, ids []int64) ([]store.CalibrationRecord, error)
	InsertCalibrations(ctx context.Context, channel string, recs []*store.CalibrationRecord, activate bool, audit store.AdminAuditEntry) error
	SetCalibrationActive(ctx context.Context, channel string, id int64, active bool, audit store.AdminAuditEntry) (*store.CalibrationRecord, bool, error)
	ReliabilitySamples(ctx context.Context, f store.ReliabilityFilter) ([]store.ReliabilitySample, error)
}

// calibrationAPI serves /api/calibration.
type calibrationAPI struct {
	store calibrationStore // nil without a database
	// lookup is the gateway's calibration lookup, reloaded after each write.
	lookup   *calibration.Store
	registry *evaluators.Registry
	policy   func() *policy.Policy
	// model reports the decision model in effect: its backend and snapshot ("" before it reported
	// one).
	model func() (backend, snapshot string)
	// allowMock accepts calibrations of mock/* snapshots (the demo and the mock test stacks);
	// includeMock fits on simulated traffic's uniform audits (the demo).
	allowMock, includeMock bool
	channel                string
	// updated broadcasts WS calibration_updated {snapshot, imported}.
	updated func(snapshot string, targets int)
	now     func() time.Time
	log     *slog.Logger
}

// register mounts the calibration routes on the /api group.
func (a *calibrationAPI) register(api *gin.RouterGroup, g apiGuards) {
	api.GET("/calibration", g.read, g.rateLimit, a.list)
	api.GET("/calibration/:id", g.read, g.rateLimit, a.get)
	api.POST("/calibration", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.importFile)
	api.POST("/calibration/fit", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.fit)
	api.POST("/calibration/:id/deactivate", g.writable, g.admin, g.rateLimit, g.bodyLimit, func(c *gin.Context) { a.setActive(c, false) })
	api.POST("/calibration/:id/activate", g.writable, g.admin, g.rateLimit, g.bodyLimit, func(c *gin.Context) { a.setActive(c, true) })
}

func (a *calibrationAPI) available(c *gin.Context) bool {
	if a.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Calibration records are kept in the database, and this proxy has none (TRUTHTABLE_DATABASE_URL). "+
				"Without one the gateway reads the files in TRUTHTABLE_CALIBRATION_DIR.")
		return false
	}
	return true
}

// calibrationMeta is api.ts CalibrationRecordMeta.
type calibrationMeta struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	Origin     string    `json:"origin"`
	Backend    string    `json:"backend"`
	Snapshot   string    `json:"snapshot"`
	Family     string    `json:"family"`
	TargetKind string    `json:"target_kind"`
	TargetKey  string    `json:"target_key"`
	Quality    string    `json:"quality"`
	Active     bool      `json:"active"`
}

// calibrationRecordView is api.ts CalibrationRecord.
type calibrationRecordView struct {
	calibrationMeta
	Entry json.RawMessage `json:"entry"`
}

func metaOf(r *store.CalibrationRecord) calibrationMeta {
	return calibrationMeta{
		ID: r.ID, CreatedAt: r.CreatedAt, Origin: r.Origin, Backend: r.Backend, Snapshot: r.Snapshot, Family: r.Family,
		TargetKind: r.TargetKind, TargetKey: r.TargetKey, Quality: r.Quality, Active: r.Active,
	}
}

// GET /api/calibration?snapshot=&active= (#42): the records, newest first.
func (a *calibrationAPI) list(c *gin.Context) {
	if !a.available(c) {
		return
	}
	f := store.CalibrationFilter{Snapshot: c.Query("snapshot")}
	if f.Snapshot != "" && !validSnapshot(f.Snapshot) {
		badParam(c, "snapshot", "must be a decision-model snapshot name")
		return
	}
	active, ok := queryBool(c, "active")
	if !ok {
		return
	}
	f.Active = active
	recs, err := a.store.ListCalibrations(c.Request.Context(), f)
	if err != nil {
		a.internal(c, "list calibrations", err)
		return
	}
	items := make([]calibrationMeta, len(recs))
	for i := range recs {
		items[i] = metaOf(&recs[i])
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (a *calibrationAPI) id(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "The calibration id must be a positive integer.",
			fieldError{Path: "id", Message: "positive integer"})
		return 0, false
	}
	return id, true
}

// GET /api/calibration/:id (#43): one record with its calibration.v1 entry.
func (a *calibrationAPI) get(c *gin.Context) {
	if !a.available(c) {
		return
	}
	id, ok := a.id(c)
	if !ok {
		return
	}
	r, err := a.store.GetCalibration(c.Request.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		abortAPI(c, http.StatusNotFound, codeNotFound, "No calibration has this id.")
		return
	case err != nil:
		a.internal(c, "get calibration", err)
		return
	}
	c.JSON(http.StatusOK, calibrationRecordView{calibrationMeta: metaOf(r), Entry: r.Entry})
}

// reloaded re-reads the lookup after a write, so the reply's state is the gateway's, and
// broadcasts calibration_updated for each snapshot the write touched. A failed reload is logged:
// the write is stored and the poll catches up.
func (a *calibrationAPI) reloaded(ctx context.Context, snapshots ...string) {
	if a.lookup != nil {
		if _, err := a.lookup.ReloadContext(ctx); err != nil {
			a.log.Warn("calibration reload after a write failed; the poll retries", "err", err)
		}
	}
	if a.updated == nil {
		return
	}
	seen := map[string]bool{}
	for _, snap := range snapshots {
		if seen[snap] {
			continue
		}
		seen[snap] = true
		targets := 0
		if a.lookup != nil {
			_, targets = a.lookup.State(snap)
		}
		a.updated(snap, targets)
	}
}

// POST /api/calibration/:id/activate (#46b) and /deactivate (#46). Activating replaces the active
// record of the target; deactivating an imported or fitted record brings back the target's newest
// file record.
func (a *calibrationAPI) setActive(c *gin.Context, active bool) {
	if !a.available(c) {
		return
	}
	id, ok := a.id(c)
	if !ok {
		return
	}
	action := "calibration.deactivate"
	if active {
		action = "calibration.activate"
	}
	ctx := c.Request.Context()
	r, changed, err := a.store.SetCalibrationActive(ctx, a.channel, id, active,
		auditEntry(c, action, strconv.FormatInt(id, 10), map[string]any{"active": active}))
	switch {
	case errors.Is(err, store.ErrNotFound):
		abortAPI(c, http.StatusNotFound, codeNotFound, "No calibration has this id.")
		return
	case err != nil:
		a.internal(c, action, err)
		return
	}
	if changed {
		a.log.Info("calibration "+map[bool]string{true: "activated", false: "deactivated"}[active], "id", id, "target", r.TargetKey,
			"snapshot", r.Snapshot, "by", actor(c))
		a.reloaded(ctx, r.Snapshot)
	}
	c.JSON(http.StatusOK, metaOf(r))
}

// importRejection is CalibrationImportResult.rejected[].
type importRejection struct {
	TargetKey *string `json:"target_key"`
	Reason    string  `json:"reason"`
	Message   string  `json:"message"`
}

// POST /api/calibration {file, activate} (#44): imports the entries of a calibration file that
// apply to a registered evaluator version. The server re-derives each entry's quality from its
// labels; an entry for an unknown version, with another calibration key, or of a mock snapshot
// outside the demo and test stacks is rejected with its reason.
func (a *calibrationAPI) importFile(c *gin.Context) {
	var body struct {
		File     json.RawMessage `json:"file"`
		Activate bool            `json:"activate"`
	}
	if !bindJSON(c, &body, false) || !a.available(c) {
		return
	}
	if len(body.File) == 0 || string(body.File) == "null" {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "file is required: a trustagent.calibration/v1 document.",
			fieldError{Path: "file", Message: "required"})
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body.File))
	dec.DisallowUnknownFields()
	var f decision.CalibrationFile
	if err := dec.Decode(&f); err != nil {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The file is not a calibration file: "+err.Error(),
			fieldError{Path: "file", Message: err.Error()})
		return
	}
	// The file's own fields (schema, model, created_at), without its entries.
	head := f
	head.Entries = []decision.CalibrationFileEntry{}
	if f.Entries == nil {
		head.Entries = nil
	}
	if err := head.Check(); err != nil {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The file does not validate: "+err.Error(),
			fieldError{Path: "file", Message: err.Error()})
		return
	}
	snapshot := f.DecisionModel.Snapshot
	var recs []*store.CalibrationRecord
	rejected := []importRejection{}
	reject := func(key, reason, msg string) {
		r := importRejection{Reason: reason, Message: msg}
		if key != "" {
			r.TargetKey = &key
		}
		rejected = append(rejected, r)
	}
	seen := map[string]bool{}
	for i := range f.Entries {
		e := f.Entries[i]
		key := e.Target.Key()
		ref := evaluators.VersionRef(e.Target.EvaluatorID, e.Target.EvaluatorVersion)
		if strings.HasPrefix(snapshot, "mock/") && !a.allowMock {
			reject(key, rejectMockSnapshot, "Calibrations of the mock decision model apply only to the zero-key demo and the test stacks.")
			continue
		}
		e.Quality = decision.DeriveQuality(snapshot, e.LabelSource, e.NPos, e.NNeg, false)
		single := head
		single.Entries = []decision.CalibrationFileEntry{e}
		if err := single.Check(); err != nil {
			reject(key, rejectInvalid, strings.TrimPrefix(err.Error(), "calibration file: "))
			continue
		}
		if seen[key] {
			reject(key, rejectInvalid, "The file calibrates "+key+" twice.")
			continue
		}
		seen[key] = true
		spec, ok := a.registry.Get(ref)
		if !ok {
			reject(key, rejectUnknownVersion, ref+" is not a registered evaluator version.")
			continue
		}
		h, err := spec.Hashes()
		if err != nil {
			a.internal(c, "evaluator hashes", err)
			return
		}
		if e.Target.CalibrationKey != h.CalibrationKey {
			reject(key, rejectKeyMismatch, "The entry was fitted on a different question than "+ref+" asks (its calibration key differs).")
			continue
		}
		rec, err := entryRecord(store.CalibrationOriginImport, actor(c), f.DecisionModel, &e)
		if err != nil {
			a.internal(c, "import calibration", err)
			return
		}
		recs = append(recs, rec)
	}
	imported := []calibrationMeta{}
	if len(recs) > 0 {
		ctx := c.Request.Context()
		err := a.store.InsertCalibrations(ctx, a.channel, recs, body.Activate, auditEntry(c, "calibration.import", snapshot,
			map[string]any{"snapshot": snapshot, "imported": len(recs), "rejected": len(rejected), "activate": body.Activate}))
		if err != nil {
			a.internal(c, "import calibration", err)
			return
		}
		for _, r := range recs {
			imported = append(imported, metaOf(r))
		}
		a.log.Info("calibration imported", "snapshot", snapshot, "imported", len(recs), "rejected", len(rejected),
			"active", body.Activate, "by", actor(c))
		a.reloaded(ctx, snapshot)
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "rejected": rejected})
}

// entryRecord is the calibrations row of an entry.
func entryRecord(origin, by string, model decision.CalibrationModel, e *decision.CalibrationFileEntry) (*store.CalibrationRecord, error) {
	doc, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	src, err := json.Marshal(e.LabelSource)
	if err != nil {
		return nil, err
	}
	return &store.CalibrationRecord{
		CreatedBy: by, Origin: origin, Backend: model.Backend, Snapshot: model.Snapshot, Family: model.Family,
		TargetKind: e.Target.Kind, TargetKey: e.Target.Key(), Quality: e.Quality, LabelSource: src, Entry: doc,
	}, nil
}

// fitResponse is api.ts CalibrationFitResponse.
type fitResponse struct {
	Record      calibrationRecordView `json:"record"`
	Reliability reliabilityResponse   `json:"reliability"`
	Metrics     decision.FitMetrics   `json:"metrics"`
	Recommended decision.Recommended  `json:"recommended"`
}

// POST /api/calibration/fit (#45): fits a calibration of one evaluator version from the label
// store: the human (and public-dataset) labels of the last 30 days on the snapshot, plus the
// uniform-sample deep audits when sources asks for them. Quality is measured only from human or
// public-dataset labels (enough of them); any uniform-audit label makes it proxy. 422 below 8
// labels or 3 of each class. With activate:false the record is stored inactive for Preview impact
// (D62).
func (a *calibrationAPI) fit(c *gin.Context) {
	var body struct {
		Target   string   `json:"target"`
		Route    string   `json:"route"`
		Snapshot string   `json:"snapshot"`
		Sources  []string `json:"sources"`
		Method   string   `json:"method"`
		Activate bool     `json:"activate"`
	}
	if !bindJSON(c, &body, false) || !a.available(c) {
		return
	}
	if !labelTargetRef.MatchString(body.Target) {
		invalidLabel(c, "target", "target is an evaluator version such as groundedness@1.")
		return
	}
	spec, ok := a.registry.Get(body.Target)
	if !ok {
		invalidLabel(c, "target", body.Target+" is not a registered evaluator version.")
		return
	}
	sources := slices.Clone(body.Sources)
	slices.Sort(sources)
	uniform := false
	switch strings.Join(sources, ",") {
	case fitSourceLabels:
	case fitSourceLabels + "," + fitSourceUniformAudit:
		uniform = true
	default:
		invalidLabel(c, "sources", `sources is ["labels"] or ["labels", "uniform_audit"].`)
		return
	}
	method := body.Method
	if method == "" {
		method = decision.MethodAuto
	}
	if !slices.Contains([]string{decision.MethodAuto, decision.MethodIsotonic, decision.MethodPlatt}, method) {
		invalidLabel(c, "method", "method is auto, isotonic or platt.")
		return
	}
	backend, snapshot := a.model()
	if body.Snapshot != "" {
		if !validSnapshot(body.Snapshot) {
			invalidLabel(c, "snapshot", "snapshot must be a decision-model snapshot name.")
			return
		}
		snapshot = body.Snapshot
	}
	switch {
	case snapshot == "":
		invalidLabel(c, "snapshot", "The decision model has not reported a snapshot yet: pass the snapshot the labels were given on.")
		return
	case strings.HasPrefix(snapshot, "mock/") && !a.allowMock:
		invalidLabel(c, "snapshot", "Calibrations of the mock decision model apply only to the zero-key demo and the test stacks.")
		return
	case strings.HasPrefix(snapshot, "mock/"):
		backend = "mock"
	}
	var params map[string]string
	if body.Route != "" {
		r := a.route(body.Route)
		if r == nil {
			invalidLabel(c, "route", "The active policy has no route "+body.Route+".")
			return
		}
		for _, e := range r.Evaluators {
			if e.Ref == body.Target {
				params = e.Params
			}
		}
	}
	target, err := decision.EvaluatorTarget(spec, decision.ResolvedParams(spec, params))
	if err != nil {
		a.internal(c, "calibration target", err)
		return
	}
	ctx := c.Request.Context()
	to := a.now().UTC()
	// The route's labels only when its params shape the question (they change the target key).
	route := ""
	if len(spec.Params) > 0 {
		route = body.Route
	}
	samples, err := a.store.ReliabilitySamples(ctx, store.ReliabilityFilter{
		Evaluator: body.Target, Snapshot: snapshot, Route: route, From: to.Add(-fitWindowSpan), To: to,
		Labels: true, UniformAudits: uniform, IncludeMock: a.includeMock,
	})
	if err != nil {
		a.internal(c, "read labels", err)
		return
	}
	var src decision.LabelSourceCounts
	scores, preds := make([]decision.LabeledScore, len(samples)), make([]float64, len(samples))
	rel := reliabilityResponse{Evaluator: body.Target, Snapshot: &snapshot, Window: fitWindow, N: len(samples),
		NBySource: map[string]int{store.LabelSourceHuman: 0, store.LabelSourceUniformAudit: 0}}
	for i, smp := range samples {
		scores[i], preds[i] = decision.LabeledScore{Risk: smp.Risk, Fire: smp.Fire}, smp.Risk
		if err := src.Add(smp.Source); err != nil {
			a.internal(c, "label source", err)
			return
		}
		rel.NBySource[smp.Source]++
	}
	rel.Bins, rel.ECE = decision.Reliability(scores, preds)
	fit, err := decision.FitCalibration(scores, decision.FitOptions{Method: method})
	if errors.Is(err, decision.ErrNotEnoughLabels) {
		pos := 0
		for _, s := range scores {
			if s.Fire {
				pos++
			}
		}
		abortAPIDetails(c, http.StatusUnprocessableEntity, codeValidationFailed,
			fmt.Sprintf("Not enough labels to calibrate %s: %d (%d should fire, %d should not); a fit needs %d with at least %d of each.",
				body.Target, len(scores), pos, len(scores)-pos, decision.MinFitLabels, decision.MinFitPerClass),
			map[string]any{"names": []string{body.Target}, "n": len(scores), "pos": pos, "neg": len(scores) - pos,
				"needed": map[string]int{"n": decision.MinFitLabels, "per_class": decision.MinFitPerClass}},
			fieldError{Path: "target", Message: "not enough labels"})
		return
	}
	if err != nil {
		a.internal(c, "fit calibration", err)
		return
	}
	qs, err := decision.SoloQuestionSetSHA256(spec)
	if err != nil {
		a.internal(c, "question set", err)
		return
	}
	entry, err := decision.NewCalibrationEntry(target, decision.FitContext{StateFields: spec.StateFields, QuestionSetSHA256: qs},
		fit, src, nil, snapshot)
	if err != nil {
		a.internal(c, "calibration entry", err)
		return
	}
	model := decision.CalibrationModel{Backend: backend, Snapshot: snapshot, Family: decision.SnapshotFamily(snapshot)}
	rec, err := entryRecord(store.CalibrationOriginFit, actor(c), model, entry)
	if err != nil {
		a.internal(c, "fit calibration", err)
		return
	}
	err = a.store.InsertCalibrations(ctx, a.channel, []*store.CalibrationRecord{rec}, body.Activate,
		auditEntry(c, "calibration.fit", rec.TargetKey, map[string]any{"snapshot": snapshot, "quality": rec.Quality, "n": fit.N,
			"method": fit.Method, "sources": sources, "activate": body.Activate}))
	if err != nil {
		a.internal(c, "store calibration", err)
		return
	}
	a.log.Info("calibration fitted", "target", rec.TargetKey, "snapshot", snapshot, "quality", rec.Quality, "n", fit.N,
		"active", rec.Active, "by", actor(c))
	a.reloaded(ctx, snapshot)
	c.JSON(http.StatusOK, fitResponse{
		Record: calibrationRecordView{calibrationMeta: metaOf(rec), Entry: rec.Entry}, Reliability: rel,
		Metrics: fit.Metrics, Recommended: fit.Recommended,
	})
}

func (a *calibrationAPI) route(name string) *policy.Route {
	if a.policy == nil {
		return nil
	}
	p := a.policy()
	if p == nil {
		return nil
	}
	r, _ := p.Route(name)
	return r
}

// errUnknownCalibrations is a dry-run's calibration_ids naming records that do not exist.
type errUnknownCalibrations struct{ ids []int64 }

func (e *errUnknownCalibrations) Error() string {
	return fmt.Sprintf("no calibration has the ids %v", e.ids)
}

// preview is the calibration lookup of a dry-run with calibration_ids: the lookup with those
// records in place of the active ones of their targets.
func (a *calibrationAPI) preview(ctx context.Context, ids []int64) (policy.CalibrationLookup, error) {
	if a.store == nil || a.lookup == nil {
		return nil, errors.New("calibration records need a database")
	}
	recs, err := a.store.CalibrationsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	var missing []int64
	for _, id := range ids {
		if !slices.ContainsFunc(recs, func(r store.CalibrationRecord) bool { return r.ID == id }) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, &errUnknownCalibrations{ids: missing}
	}
	return a.lookup.With(recs)
}

func (a *calibrationAPI) internal(c *gin.Context, what string, err error) {
	a.log.Error("api: "+what+" failed", "path", c.FullPath(), "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request could not be completed; see the proxy log.")
}
