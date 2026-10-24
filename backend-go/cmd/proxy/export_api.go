package main

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/version"
)

var (
	// gitRevision is a recording's git_sha (common.v1 GitSha).
	gitRevision = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// releaseVersion is a recording's trustagent_version (recording.v1).
	releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
)

// exportTimeout bounds reading the traces of one recording.
const exportTimeout = 45 * time.Second

// handleExportReplay serves GET /api/export/replay (SPEC §2.4 endpoint 66, D11, admin): the
// recording manifest (trustagent.recording/v1) of the newest stored traces, built and sanitized
// exactly as cmd/export-replay builds manifest.json. The trace files stay with the CLI.
//
// Query: since and until (RFC 3339), limit (1-200, default 200), source (a TraceSource) and route.
// A manifest names the build that recorded it: a proxy built without its git revision needs
// git_sha (the revision it was built from). A manifest never claims more than its traces support,
// so traces that cannot be labelled honestly (mixed backends, a fallback or mock judge, missing
// answer sources) answer 409 conflict with the reason.
func (s *server) handleExportReplay(c *gin.Context) {
	if s.pg == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Recordings are built from stored traces, and this proxy has no database (TRUTHTABLE_DATABASE_URL).")
		return
	}
	f := recording.Filter{Limit: recording.MaxTraces, Route: c.Query("route")}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > recording.MaxTraces {
			badParam(c, "limit", "must be an integer from 1 to 200")
			return
		}
		f.Limit = n
	}
	var ok bool
	if f.Since, ok = queryTime(c, "since"); !ok {
		return
	}
	if f.Until, ok = queryTime(c, "until"); !ok {
		return
	}
	if f.Source, ok = queryEnum(c, "source", traceSources...); !ok {
		return
	}
	if f.Route != "" && !routeNamePattern.MatchString(f.Route) {
		badParam(c, "route", "must be a route name (lowercase letters, digits, - and _)")
		return
	}
	gitSHA := buildSHA()
	if !gitRevision.MatchString(gitSHA) {
		switch gitSHA = c.Query("git_sha"); {
		case gitSHA == "":
			abortAPI(c, http.StatusConflict, codeConflict,
				"This proxy was built without its git revision, which a recording names: pass git_sha=<the revision it was built from>.")
			return
		case !gitRevision.MatchString(gitSHA):
			badParam(c, "git_sha", "must be a git revision: 7 to 40 lowercase hex characters")
			return
		}
	}
	if !releaseVersion.MatchString(version.Version) {
		abortAPI(c, http.StatusConflict, codeConflict,
			"This proxy is not a release build (version "+version.Version+"), and a recording names its release version.")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), exportTimeout)
	defer cancel()
	traces, err := (&recording.DBSource{Store: s.pg, Now: time.Now}).Traces(ctx, f)
	if err != nil {
		s.exportFailed(c, "read the traces", err)
		return
	}
	judge := s.engineJudge()
	rec, err := recording.Build(traces, recording.Options{GitSHA: gitSHA, Version: version.Version, DefaultJudge: judge, Hosts: s.recordingHosts(judge)})
	if errors.Is(err, recording.ErrNoJudge) {
		abortAPI(c, http.StatusConflict, codeConflict,
			"No exported trace has a deep audit that names its judge, and the audit engine reports none: widen since/until or raise limit to include audited traces.")
		return
	}
	if errors.Is(err, recording.ErrNotRecordable) {
		abortAPI(c, http.StatusConflict, codeConflict, err.Error())
		return
	}
	if err != nil {
		s.exportFailed(c, "build the recording", err)
		return
	}
	san, err := recording.NewSanitizer()
	if err != nil {
		s.exportFailed(c, "build the sanitizer", err)
		return
	}
	b, err := san.JSON(rec)
	if err != nil {
		s.exportFailed(c, "sanitize the recording", err)
		return
	}
	// The export is audited before it leaves (D44): without its audit row it is not sent.
	if err := s.pg.AppendAdminAudit(ctx, store.AdminAuditEntry{Actor: actor(c), Action: "export.replay",
		Diff: exportAuditDiff(f, len(rec.Traces))}); err != nil {
		s.exportFailed(c, "record the export in the audit log", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}

// exportAuditDiff is the export.replay audit diff: the filter and how many traces left.
func exportAuditDiff(f recording.Filter, traces int) map[string]any {
	d := map[string]any{"traces": traces, "limit": f.Limit}
	if f.Since != nil {
		d["since"] = f.Since.UTC().Format(time.RFC3339)
	}
	if f.Until != nil {
		d["until"] = f.Until.UTC().Format(time.RFC3339)
	}
	if f.Route != "" {
		d["route"] = f.Route
	}
	if f.Source != "" {
		d["source"] = f.Source
	}
	return d
}

// engineJudge is the judge the audit engine reports: the recording's judge when no exported audit
// names one. nil without an engine or a judge.
func (s *server) engineJudge() *recording.Judge {
	r := s.status.engineHealth().Response
	if r == nil {
		return nil
	}
	provider, model := r.GetJudgeProvider(), r.GetJudgeModel()
	if provider == "" || provider == "none" || model == "" {
		return nil
	}
	return &recording.Judge{Provider: provider, Model: model, FallbackActive: r.GetJudgeFallbackActive()}
}

// recordingHosts are the hosts this gateway's traffic goes to (D203), which a recorded manifest
// must name as vendor hosts: the decision endpoint it calls, the judge's host (its provider, and
// for OpenRouter the shared TRUTHTABLE_OPENROUTER_BASE_URL) and the default upstream, which writes
// generated answers. A host that is not a vendor's (mock-upstream in the e2e stacks) keeps the
// export from the recorded label.
func (s *server) recordingHosts(judge *recording.Judge) recording.Hosts {
	var h recording.Hosts
	if s.status != nil {
		h.Decision = recording.HostOf(s.status.jevStatus().Endpoint)
	}
	if judge != nil && s.cfg != nil {
		h.Judge = recording.JudgeHost(judge.Provider, s.cfg.Jev.OpenRouterBaseURL)
	}
	if s.cfg != nil {
		h.Generated = recording.HostOf(s.cfg.UpstreamURL)
	}
	return h
}

func (s *server) exportFailed(c *gin.Context, what string, err error) {
	s.log.Error("api: export replay: could not "+what, "err", err)
	if errors.Is(err, context.DeadlineExceeded) {
		abortAPI(c, http.StatusGatewayTimeout, "timeout", "Reading the traces took too long; ask for fewer (limit) or a shorter window.")
		return
	}
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The recording could not be built; see the proxy log.")
}
