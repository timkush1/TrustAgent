package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// Policy REST endpoints (SPEC §2.4 rows 18-26, plus the presets): the active policy with its
// effective routes, versions, export, validation with block readiness, dashboard saves with
// If-Match, rollback, adopt-file and the free dry-run ("Preview impact").

// REST error codes of the policy endpoints (SPEC §2.4).
const (
	codeConflict             = "conflict"
	codeFileManaged          = "file_managed"
	codeLocked               = "locked"
	codeStaleVersion         = "stale_version"
	codePreconditionRequired = "precondition_required"
)

// maxDryRunLimit is the largest dry-run window in rows (row 26: limit <= 100000).
const maxDryRunLimit = store.MaxReplayRows

// apiGuards are the middleware of the policy, evaluator and P3 routes.
type apiGuards struct {
	// read and admin authenticate callers with those scopes; writable refuses writes in
	// read-only mode; rateLimit is the general per-caller limit; dryRunLimit is the policy
	// dry-run limit (6/min), used instead of rateLimit; bodyLimit caps request bodies.
	read, admin, writable, rateLimit, dryRunLimit, bodyLimit gin.HandlerFunc
	// limitBy builds an endpoint's own per-caller limit of n requests per window, used instead
	// of rateLimit (the Playground's 10/min).
	limitBy func(n int, window time.Duration) gin.HandlerFunc
}

// policiesAPI serves /api/policies.
type policiesAPI struct {
	engine *policy.Engine
	// replay reads recorded decisions for the dry-run; nil without a database.
	replay  policy.ReplaySource
	presets json.RawMessage
	log     *slog.Logger
}

// newPoliciesAPI builds the policy endpoints. replay may be nil (no database: no dry-run).
func newPoliciesAPI(engine *policy.Engine, replay policy.ReplaySource, log *slog.Logger) (*policiesAPI, error) {
	presets, err := policy.Presets()
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &policiesAPI{engine: engine, replay: replay, presets: presets, log: log}, nil
}

// register mounts the policy routes on the /api group.
func (a *policiesAPI) register(api *gin.RouterGroup, g apiGuards) {
	api.GET("/policies", g.read, g.rateLimit, a.get)
	api.GET("/policies/versions", g.read, g.rateLimit, a.versions)
	api.GET("/policies/versions/:id", g.read, g.rateLimit, a.version)
	api.GET("/policies/export", g.read, g.rateLimit, a.export)
	api.GET("/policies/presets", g.read, g.rateLimit, a.presetList)
	// Read-only mode refuses every non-GET /api/* request (SPEC §2.4), the read-scope
	// validate and dry-run included.
	api.POST("/policies/validate", g.writable, g.read, g.rateLimit, g.bodyLimit, a.validate)
	api.PUT("/policies", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.save)
	api.POST("/policies/rollback", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.rollback)
	api.POST("/policies/adopt-file", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.adoptFile)
	api.POST("/policies/dry-run", g.writable, g.read, g.dryRunLimit, g.bodyLimit, a.dryRun)
}

// isAdmin reports whether the caller may edit (PolicyState.editable).
func isAdmin(c *gin.Context) bool { return callerIdentity(c).Has(authz.ScopeAdmin) }

// actor is the author recorded for a write.
func actor(c *gin.Context) string {
	if id := callerIdentity(c); id != nil && id.Label != "" {
		return id.Label
	}
	return publicLabel
}

// respondState writes a PolicyState with its ETag.
func (a *policiesAPI) respondState(c *gin.Context, st *policy.State) {
	c.Header("ETag", st.ETag())
	c.JSON(http.StatusOK, a.engine.View(st, isAdmin(c)))
}

// GET /api/policies
func (a *policiesAPI) get(c *gin.Context) { a.respondState(c, a.engine.State()) }

// policyPage is Page<PolicyVersionMeta>.
type policyPage struct {
	Items      []policy.VersionMeta `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

// GET /api/policies/versions
func (a *policiesAPI) versions(c *gin.Context) {
	limit, ok := queryLimit(c)
	if !ok {
		return
	}
	items, next, err := a.engine.Versions(c.Request.Context(), limit, c.Query("cursor"))
	if errors.Is(err, store.ErrBadCursor) {
		badParam(c, "cursor", "is not a cursor this server issued")
		return
	}
	if err != nil {
		a.internal(c, "list policy versions", err)
		return
	}
	p := policyPage{Items: items}
	if next != "" {
		p.NextCursor = &next
	}
	c.JSON(http.StatusOK, p)
}

// versionParam reads a positive version id.
func versionParam(c *gin.Context, raw, name string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		badParam(c, name, "must be a positive integer version id")
		return 0, false
	}
	return id, true
}

// GET /api/policies/versions/:id
func (a *policiesAPI) version(c *gin.Context) {
	id, ok := versionParam(c, c.Param("id"), "id")
	if !ok {
		return
	}
	v, err := a.engine.Version(c.Request.Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// GET /api/policies/export?version=
func (a *policiesAPI) export(c *gin.Context) {
	st := a.engine.State()
	v := &st.Version
	if raw := c.Query("version"); raw != "" {
		id, ok := versionParam(c, raw, "version")
		if !ok {
			return
		}
		var err error
		if v, err = a.engine.Version(c.Request.Context(), id); err != nil {
			a.fail(c, err)
			return
		}
	}
	c.Header("Content-Disposition", `attachment; filename="policies.v`+strconv.FormatInt(v.ID, 10)+`.yaml"`)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", []byte(v.YAML))
}

// GET /api/policies/presets
func (a *policiesAPI) presetList(c *gin.Context) {
	c.Data(http.StatusOK, "application/json; charset=utf-8", a.presets)
}

// sourceBody is the {yaml?|doc?} part of a request.
type sourceBody struct {
	YAML string          `json:"yaml"`
	Doc  json.RawMessage `json:"doc"`
}

func (b sourceBody) source() policy.Source { return policy.Source{YAML: b.YAML, Doc: b.Doc} }

// bindJSON decodes the request body strictly; an empty body decodes to the zero value when
// optional.
func bindJSON(c *gin.Context, dst any, optional bool) bool {
	if optional && c.Request.ContentLength == 0 {
		return true
	}
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if optional && errors.Is(err, io.EOF) {
			return true
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			abortAPI(c, http.StatusRequestEntityTooLarge, codeTooLarge, "The request body is too large.")
			return false
		}
		abortAPI(c, http.StatusBadRequest, codeInvalidJSON, "The request body is not valid JSON for this endpoint: "+err.Error())
		return false
	}
	return true
}

// POST /api/policies/validate
func (a *policiesAPI) validate(c *gin.Context) {
	var body sourceBody
	if !bindJSON(c, &body, false) {
		return
	}
	res, err := a.engine.Validate(body.source())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// PUT /api/policies
func (a *policiesAPI) save(c *gin.Context) {
	var body struct {
		sourceBody
		Message string `json:"message"`
	}
	if !bindJSON(c, &body, false) {
		return
	}
	st, err := a.engine.Save(c.Request.Context(), policy.SaveRequest{
		Source: body.source(), Message: body.Message, Author: actor(c), IfMatch: c.GetHeader("If-Match"),
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	a.log.Info("policy saved", "version", st.Version.ID, "author", st.Version.Author)
	a.respondState(c, st)
}

// POST /api/policies/rollback
func (a *policiesAPI) rollback(c *gin.Context) {
	var body struct {
		Version *int64 `json:"version"`
		Message string `json:"message"`
	}
	if !bindJSON(c, &body, false) {
		return
	}
	if body.Version == nil || *body.Version < 1 {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "version is required: the policy version id to roll back to.",
			fieldError{Path: "version", Message: "required positive integer"})
		return
	}
	st, err := a.engine.Rollback(c.Request.Context(), *body.Version, actor(c), body.Message)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.log.Info("policy rolled back", "to", *body.Version, "version", st.Version.ID, "author", actor(c))
	a.respondState(c, st)
}

// POST /api/policies/adopt-file
func (a *policiesAPI) adoptFile(c *gin.Context) {
	var body struct {
		Message string `json:"message"`
	}
	if !bindJSON(c, &body, true) {
		return
	}
	st, err := a.engine.AdoptFile(c.Request.Context(), actor(c), body.Message)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.log.Info("policy file adopted", "version", st.Version.ID, "author", actor(c))
	a.respondState(c, st)
}

// POST /api/policies/dry-run
func (a *policiesAPI) dryRun(c *gin.Context) {
	var body struct {
		sourceBody
		Version *int64 `json:"version"`
		Window  string `json:"window"`
		Limit   *int   `json:"limit"`
	}
	if !bindJSON(c, &body, false) {
		return
	}
	if _, ok := policy.ParseWindow(body.Window); !ok {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "window must be one of 15m, 1h, 24h, 7d, 30d.",
			fieldError{Path: "window", Message: "one of 15m, 1h, 24h, 7d, 30d"})
		return
	}
	limit := 0
	if body.Limit != nil {
		if *body.Limit < 1 || *body.Limit > maxDryRunLimit {
			abortAPI(c, http.StatusBadRequest, codeBadRequest, "limit must be from 1 to 100000.",
				fieldError{Path: "limit", Message: "from 1 to 100000"})
			return
		}
		limit = *body.Limit
	}
	if a.replay == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Preview impact replays recorded traffic, which needs a database (TRUTHTABLE_DATABASE_URL).")
		return
	}
	proposed, err := a.engine.Proposed(c.Request.Context(), body.source(), body.Version)
	if err != nil {
		a.fail(c, err)
		return
	}
	res, err := a.engine.Preview(c.Request.Context(), a.replay, proposed, body.Window, limit)
	if err != nil {
		a.internal(c, "policy dry-run", err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// fail maps engine errors to REST errors (SPEC §2.4 error codes).
func (a *policiesAPI) fail(c *gin.Context, err error) {
	var ve *policy.ValidationError
	switch {
	case errors.As(err, &ve):
		abortFields(c, http.StatusUnprocessableEntity, codeValidationFailed, "The policy does not validate.", ve.Errors)
	case errors.Is(err, policy.ErrBadSource):
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "Send exactly one of yaml (text) or doc (object).")
	case errors.Is(err, policy.ErrFileManaged):
		abortAPI(c, http.StatusConflict, codeFileManaged,
			"The policy is file-managed (no database): edit "+a.engine.State().File.Path+" instead.")
	case errors.Is(err, policy.ErrLocked):
		abortAPI(c, http.StatusLocked, codeLocked, "The policy is locked to its file (TRUTHTABLE_CONFIG_LOCK).")
	case errors.Is(err, policy.ErrPreconditionRequired):
		abortAPI(c, http.StatusPreconditionRequired, codePreconditionRequired,
			"Send If-Match with the ETag of the policy you edited (GET /api/policies).")
	case errors.Is(err, policy.ErrStaleVersion):
		abortAPI(c, http.StatusPreconditionFailed, codeStaleVersion,
			"The policy changed since you loaded it: reload, reapply your edit and save again.")
	case errors.Is(err, policy.ErrVersionNotFound):
		abortAPI(c, http.StatusNotFound, codeNotFound, "No such policy version.")
	case errors.Is(err, policy.ErrFileMissing):
		abortAPI(c, http.StatusConflict, codeConflict, "There is no policy file to adopt at "+a.engine.State().File.Path+".")
	default:
		a.internal(c, "policy request", err)
	}
}

func (a *policiesAPI) internal(c *gin.Context, what string, err error) {
	a.log.Error(what+" failed", "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request failed; see the proxy log.")
}

// fieldErrorBody is ApiErrorBody with field errors that carry line and column (api.ts FieldError).
type fieldErrorBody struct {
	Error struct {
		Code        string              `json:"code"`
		Message     string              `json:"message"`
		FieldErrors []policy.FieldError `json:"field_errors"`
	} `json:"error"`
}

// abortFields writes a REST error with positioned field errors and stops the handler chain.
func abortFields(c *gin.Context, status int, code, message string, fields []policy.FieldError) {
	var body fieldErrorBody
	body.Error.Code, body.Error.Message, body.Error.FieldErrors = code, message, fields
	if body.Error.FieldErrors == nil {
		body.Error.FieldErrors = []policy.FieldError{}
	}
	c.AbortWithStatusJSON(status, body)
}
