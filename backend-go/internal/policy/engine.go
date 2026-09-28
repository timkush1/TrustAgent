package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// The engine owns the active policy (SPEC §7.2 "Policy versions"):
//
//   - Whole-document versions live in policy_versions; the newest one is active.
//   - The policy file is synced into versions: a changed file (new sha) becomes a file version,
//     unless a dashboard version is newer than the last file version, in which case the file is
//     reported as diverged until an admin adopts it. TRUTHTABLE_CONFIG_LOCK makes the file
//     authoritative: it is always synced and dashboard writes are refused.
//   - Every write runs under the policy write lock, so replicas starting together insert a file
//     version once, and an If-Match save compares against the head it is inserted after.
//   - Hot reload: NOTIFY on every write plus a poll (TRUTHTABLE_POLICY_RELOAD_INTERVAL) of the
//     file and of the newest version id.
//   - Without a database the file is the only source (file-managed): the file is polled, and each
//     valid change becomes the next in-memory version.

// File statuses (PolicyState.file.status, SystemStatus.policy.file_status).
const (
	FileOK       = "ok"
	FileMissing  = "missing"
	FileInvalid  = "invalid"
	FileDiverged = "diverged"
)

// Editability reasons (PolicyState.editable_reason, SPEC §2.4).
const (
	ReasonFileManaged   = "file-managed (no database)"
	ReasonLocked        = "locked (TRUTHTABLE_CONFIG_LOCK)"
	ReasonAdminRequired = "admin sign-in required"
)

// Error sources (WS policy_error).
const (
	ErrorSourceFile     = "file"
	ErrorSourceDatabase = "database"
)

// VersionStore is the database side of the engine; *store.PostgresStore implements it.
type VersionStore interface {
	WithPolicyLock(ctx context.Context, channel string, fn func(store.PolicyTx) error) error
	LatestPolicyVersion(ctx context.Context) (*store.PolicyVersionRecord, error)
	PolicyHeadID(ctx context.Context) (int64, error)
	GetPolicyVersion(ctx context.Context, id int64) (*store.PolicyVersionRecord, error)
	ListPolicyVersions(ctx context.Context, limit int, cursor string) ([]store.PolicyVersionRecord, string, error)
}

// Listener delivers NOTIFY payloads; *store.PostgresStore implements it. A VersionStore that
// also implements Listener gets immediate reloads.
type Listener interface {
	Listen(ctx context.Context, channel string, ready func(), fn func(payload string)) error
}

// Update is a newly active policy version (WS policy_updated).
type Update struct {
	Version int64  `json:"version"`
	Source  string `json:"source"`
	Author  string `json:"author"`
	Message string `json:"message"`
}

// PolicyError is a policy that failed to load (WS policy_error).
type PolicyError struct {
	Source string       `json:"source"`
	Errors []FieldError `json:"errors"`
}

// EngineConfig configures an Engine.
type EngineConfig struct {
	// File is TRUTHTABLE_POLICY_FILE.
	File string
	Env  Env
	// Store holds the versions; nil makes the file the only source (file-managed).
	Store VersionStore
	// Lock is TRUTHTABLE_CONFIG_LOCK.
	Lock bool
	// ReloadInterval is TRUTHTABLE_POLICY_RELOAD_INTERVAL; 0 disables polling.
	ReloadInterval time.Duration
	// Channel is the NOTIFY channel (store.ConfigChannel when empty).
	Channel string
	// Runtime reports the decision-model state for effective routes; nil means a working
	// decision model with no calibrations.
	Runtime func() Runtime
	// OnUpdate and OnError are called after a version becomes active or fails to load (outside
	// the engine's lock). Either may be nil.
	OnUpdate func(Update)
	OnError  func(PolicyError)
	Logger   *slog.Logger
	Now      func() time.Time
}

// VersionMeta is api.ts PolicyVersionMeta.
type VersionMeta struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Source    string    `json:"source"`
	Author    string    `json:"author"`
	Message   string    `json:"message"`
	SHA256    string    `json:"sha256"`
}

// Version is api.ts PolicyVersion.
type Version struct {
	VersionMeta
	YAML string          `json:"yaml"`
	Doc  json.RawMessage `json:"doc"`
}

// FileState is api.ts PolicyState.file.
type FileState struct {
	Path   string       `json:"path"`
	SHA256 *string      `json:"sha256"`
	Status string       `json:"status"`
	Errors []FieldError `json:"errors"`
}

// State is the engine's active policy and its version, file and lock state.
type State struct {
	Policy    *Policy
	Version   Version
	Persisted bool
	Locked    bool
	File      FileState
	// Warnings are operational warnings (a newest version that no longer validates).
	Warnings []string
}

// ETag is the version's entity tag: "v<id>".
func (s *State) ETag() string { return `"v` + strconv.FormatInt(s.Version.ID, 10) + `"` }

// fileSnapshot is the last read of the policy file.
type fileSnapshot struct {
	exists bool
	sha    string
	raw    []byte
	res    *Result
	err    error // a read error other than "does not exist"
}

func (f *fileSnapshot) valid() bool {
	return f.exists && f.err == nil && f.res != nil && f.res.Policy != nil
}

// errorsOf returns the file's errors as field errors.
func (f *fileSnapshot) errorsOf() []FieldError {
	switch {
	case f.err != nil:
		return []FieldError{{Message: f.err.Error()}}
	case f.res != nil:
		return f.res.Errors
	}
	return nil
}

// Engine owns the active policy. It is safe for concurrent use.
type Engine struct {
	cfg     EngineConfig
	log     *slog.Logger
	state   atomic.Pointer[State]
	mu      sync.Mutex // serializes loads, writes and state changes
	file    fileSnapshot
	nextSeq int64 // file-managed versions
	// badHead is a newest version that failed to load; it is not retried until it changes.
	badHead int64
	trigger chan struct{}
}

// NewEngine loads the active policy: without a database the file (or the built-in defaults when
// it is missing); with one, the newest version after syncing the file. A policy that cannot be
// loaded at all is an error.
func NewEngine(ctx context.Context, cfg EngineConfig) (*Engine, error) {
	if cfg.Env.Specs == nil {
		return nil, errors.New("policy: an evaluator resolver is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Channel == "" {
		cfg.Channel = store.ConfigChannel
	}
	e := &Engine{cfg: cfg, log: cfg.Logger, trigger: make(chan struct{}, 1)}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.file = e.readFile()
	if cfg.Store == nil {
		return e, e.bootFileManaged()
	}
	return e, e.bootPersisted(ctx)
}

// readFile reads and checks the policy file.
func (e *Engine) readFile() fileSnapshot {
	b, err := os.ReadFile(e.cfg.File) // #nosec G304 -- operator-configured policy path
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fileSnapshot{}
	case err != nil:
		return fileSnapshot{exists: true, err: err}
	}
	return fileSnapshot{exists: true, sha: FileSHA256(b), raw: b, res: Check(b, e.cfg.Env)}
}

func (e *Engine) fileState(status string) FileState {
	fsState := FileState{Path: e.cfg.File, Status: status, Errors: []FieldError{}}
	if e.file.exists && e.file.sha != "" {
		sha := e.file.sha
		fsState.SHA256 = &sha
	}
	if status == FileInvalid {
		fsState.Errors = e.file.errorsOf()
	}
	return fsState
}

// fileStatus is the status of the file on its own (ok when valid; diverged is decided by sync).
func (e *Engine) fileStatus() string {
	switch {
	case !e.file.exists:
		return FileMissing
	case !e.file.valid():
		return FileInvalid
	}
	return FileOK
}

func (e *Engine) bootFileManaged() error {
	var p *Policy
	switch {
	case !e.file.exists:
		res := Check([]byte(builtinYAML), e.cfg.Env)
		if res.Policy == nil {
			return res.Err()
		}
		p = res.Policy
		p.Source = SourceBuiltin
		e.log.Warn("policy file not found: gating on the built-in defaults", "path", e.cfg.File, "mode", p.Default.Mode)
	case !e.file.valid():
		if e.file.err != nil {
			return fmt.Errorf("policy: %s: %w", e.cfg.File, e.file.err)
		}
		return fmt.Errorf("%s: %w", e.cfg.File, e.file.res.Err())
	default:
		p = e.file.res.Policy
		p.Source, p.Path = SourceFile, e.cfg.File
	}
	e.storeFileManaged(p)
	return nil
}

// storeFileManaged makes p the next in-memory version.
func (e *Engine) storeFileManaged(p *Policy) {
	e.nextSeq++
	p.Version = e.nextSeq
	author, source := "file", SourceFile
	if p.Source == SourceBuiltin {
		author, source = "system", SourceBootstrap
	}
	st := &State{
		Policy: p,
		Version: Version{VersionMeta: VersionMeta{ID: p.Version, CreatedAt: e.cfg.Now().UTC(), Source: source, Author: author,
			Message: "Loaded from " + e.cfg.File, SHA256: p.SHA256}, YAML: p.YAML, Doc: p.Doc},
		File: e.fileState(e.fileStatus()),
	}
	e.state.Store(st)
}

func (e *Engine) bootPersisted(ctx context.Context) error {
	status, err := e.syncFile(ctx)
	if err != nil {
		return err
	}
	head, err := e.cfg.Store.LatestPolicyVersion(ctx)
	if err != nil {
		return fmt.Errorf("policy: read the newest version: %w", err)
	}
	if status == FileInvalid {
		e.log.Warn("policy file does not validate; it is not applied", "path", e.cfg.File, "errors", summarize(e.file.errorsOf()))
		e.emitError(ErrorSourceFile, e.file.errorsOf())
	}
	p, errs := e.load(head)
	st := &State{Persisted: true, Locked: e.cfg.Lock, File: e.fileState(status)}
	if p == nil {
		e.badHead = head.ID
		e.log.Error("policy: the newest version no longer validates; looking for an older one", "version", head.ID, "errors", errs)
		e.emitError(ErrorSourceDatabase, errs)
		var rec *store.PolicyVersionRecord
		if p, rec, err = e.newestValid(ctx); err != nil {
			return err
		}
		st.Warnings = append(st.Warnings, fmt.Sprintf("the newest policy version v%d no longer validates (%s); running v%d until a valid version is saved",
			head.ID, summarize(errs), rec.ID))
		head = rec
	}
	st.Policy, st.Version = p, versionOf(head)
	e.state.Store(st)
	return nil
}

// newestValid scans recent versions, newest first, for one that still validates.
func (e *Engine) newestValid(ctx context.Context) (*Policy, *store.PolicyVersionRecord, error) {
	cursor := ""
	for range 4 {
		page, next, err := e.cfg.Store.ListPolicyVersions(ctx, 50, cursor)
		if err != nil {
			return nil, nil, err
		}
		for i := range page {
			if p, _ := e.load(&page[i]); p != nil {
				return p, &page[i], nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return nil, nil, errors.New("policy: no recent policy version validates; fix the evaluators it references or the policy file")
}

func summarize(errs []FieldError) string {
	if len(errs) == 0 {
		return "no details"
	}
	s := errs[0].String()
	if len(errs) > 1 {
		s += fmt.Sprintf(" and %d more", len(errs)-1)
	}
	return s
}

// load validates a stored version.
func (e *Engine) load(v *store.PolicyVersionRecord) (*Policy, []FieldError) {
	res := Check([]byte(v.YAML), e.cfg.Env)
	if res.Policy == nil {
		return nil, res.Errors
	}
	p := res.Policy
	p.Version, p.Source = v.ID, v.Source
	if v.FileSHA256 != nil {
		p.Path = e.cfg.File
	}
	return p, nil
}

func versionOf(v *store.PolicyVersionRecord) Version {
	return Version{
		VersionMeta: VersionMeta{ID: v.ID, CreatedAt: v.CreatedAt.UTC(), Source: v.Source, Author: v.Author, Message: v.Message, SHA256: v.SHA256},
		YAML:        v.YAML, Doc: v.Doc,
	}
}

// syncFile records the file as a version when it changed (SPEC §7.2) and returns its status.
// It runs at boot and whenever the file or the newest version changes.
func (e *Engine) syncFile(ctx context.Context) (string, error) {
	status := e.fileStatus()
	err := e.cfg.Store.WithPolicyLock(ctx, e.cfg.Channel, func(tx store.PolicyTx) error {
		head, err := tx.Head(ctx)
		if err != nil {
			return err
		}
		if !e.file.valid() {
			if head != nil {
				return nil
			}
			if e.file.exists {
				if e.file.err != nil {
					return fmt.Errorf("policy: %s: %w", e.cfg.File, e.file.err)
				}
				return fmt.Errorf("%s: %w", e.cfg.File, e.file.res.Err())
			}
			// A fresh database and no file: the built-in defaults are version 1.
			res := Check([]byte(builtinYAML), e.cfg.Env)
			if res.Policy == nil {
				return res.Err()
			}
			return tx.Insert(ctx, record(res.Policy, store.PolicySourceBootstrap, "system", "Built-in defaults (no policy file)", nil, nil))
		}
		last, err := tx.LastFileVersion(ctx)
		if err != nil {
			return err
		}
		if last != nil && last.FileSHA256 != nil && *last.FileSHA256 == e.file.sha {
			return nil
		}
		p := e.file.res.Policy
		if !e.cfg.Lock && (head == nil || head.SHA256 != p.SHA256) {
			dash, err := tx.LastDashboardVersion(ctx)
			if err != nil {
				return err
			}
			if dash != nil && (last == nil || dash.ID > last.ID) {
				status = FileDiverged
				return nil
			}
		}
		sha := e.file.sha
		var parent *int64
		if head != nil {
			parent = &head.ID
		}
		return tx.Insert(ctx, record(p, store.PolicySourceFile, "file", "Synced from "+e.cfg.File, &sha, parent))
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// record builds a policy_versions row from a validated policy.
func record(p *Policy, source, author, message string, fileSHA *string, parent *int64) *store.PolicyVersionRecord {
	return &store.PolicyVersionRecord{
		Source: source, Author: author, Message: message, Doc: p.Doc, YAML: p.YAML, SHA256: p.SHA256,
		FileSHA256: fileSHA, ParentID: parent,
	}
}

// Current returns the active policy.
func (e *Engine) Current() *Policy { return e.state.Load().Policy }

// State returns the active state. The value must not be modified.
func (e *Engine) State() *State { return e.state.Load() }

// Env returns the validation environment.
func (e *Engine) Env() Env { return e.cfg.Env }

// Runtime returns the decision-model state used for effective routes.
func (e *Engine) Runtime() Runtime {
	if e.cfg.Runtime == nil {
		return Runtime{Calibrations: NoCalibration{}}
	}
	return e.cfg.Runtime()
}

func (e *Engine) emitError(source string, errs []FieldError) {
	if e.cfg.OnError != nil {
		e.cfg.OnError(PolicyError{Source: source, Errors: errs})
	}
}

func (e *Engine) emitUpdate(st *State) {
	if e.cfg.OnUpdate != nil {
		v := st.Version
		e.cfg.OnUpdate(Update{Version: v.ID, Source: v.Source, Author: v.Author, Message: v.Message})
	}
}

// Reload checks the file and the newest version once and activates a newer version. It is what
// the poll and the notifications run.
func (e *Engine) Reload(ctx context.Context) error {
	var (
		updated *State
		fileErr []FieldError
		dbErr   []FieldError
	)
	err := func() error {
		e.mu.Lock()
		defer e.mu.Unlock()
		prev := e.file
		e.file = e.readFile()
		fileChanged := prev.exists != e.file.exists || prev.sha != e.file.sha || (prev.err == nil) != (e.file.err == nil)
		if fileChanged && e.file.exists && !e.file.valid() {
			fileErr = e.file.errorsOf()
		}
		cur := e.state.Load()
		if e.cfg.Store == nil {
			if !fileChanged {
				return nil
			}
			if e.file.valid() {
				p := e.file.res.Policy
				p.Source, p.Path = SourceFile, e.cfg.File
				e.storeFileManaged(p)
				updated = e.state.Load()
				return nil
			}
			next := *cur
			next.File = e.fileState(e.fileStatus())
			e.state.Store(&next)
			return nil
		}
		headID, err := e.cfg.Store.PolicyHeadID(ctx)
		if err != nil {
			return fmt.Errorf("policy reload: %w", err)
		}
		if !fileChanged && (headID == cur.Version.ID || headID == e.badHead) {
			return nil
		}
		status, err := e.syncFile(ctx)
		if err != nil {
			return err
		}
		next := *cur
		next.File = e.fileState(status)
		head, err := e.cfg.Store.LatestPolicyVersion(ctx)
		if err != nil {
			return fmt.Errorf("policy reload: %w", err)
		}
		if head.ID > cur.Version.ID && head.ID != e.badHead {
			p, errs := e.load(head)
			if p == nil {
				dbErr, e.badHead = errs, head.ID
			} else {
				next.Policy, next.Version, next.Warnings = p, versionOf(head), nil
				updated = &next
			}
		}
		e.state.Store(&next)
		return nil
	}()
	if fileErr != nil {
		e.log.Warn("policy file does not validate; it is not applied", "path", e.cfg.File, "errors", summarize(fileErr))
		e.emitError(ErrorSourceFile, fileErr)
	}
	if dbErr != nil {
		e.log.Error("policy: a new version does not validate; keeping the active one", "errors", summarize(dbErr))
		e.emitError(ErrorSourceDatabase, dbErr)
	}
	if updated != nil {
		e.log.Info("policy version active", "version", updated.Version.ID, "source", updated.Version.Source)
		e.emitUpdate(updated)
	}
	return err
}

// Trigger asks Run to reload soon (a notification arrived).
func (e *Engine) Trigger() {
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

// Run reloads on notifications and on the poll interval until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	if l, ok := e.cfg.Store.(Listener); ok && e.cfg.Store != nil {
		go e.listen(ctx, l)
	}
	var tick <-chan time.Time
	if e.cfg.ReloadInterval > 0 {
		t := time.NewTicker(e.cfg.ReloadInterval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-e.trigger:
		}
		if err := e.Reload(ctx); err != nil && ctx.Err() == nil {
			e.log.Warn("policy reload failed", "err", err)
		}
	}
}

// listen subscribes to config notifications, reconnecting with backoff.
func (e *Engine) listen(ctx context.Context, l Listener) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := l.Listen(ctx, e.cfg.Channel, func() {
			backoff = time.Second
			e.Trigger() // catch up on anything missed while not listening
		}, func(payload string) {
			if strings.HasPrefix(payload, "policy_versions:") {
				e.Trigger()
			}
		})
		if ctx.Err() != nil {
			return
		}
		e.log.Warn("policy notifications interrupted; the poll keeps running", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

// Source is a proposed policy document: exactly one of YAML and Doc.
type Source struct {
	YAML string
	Doc  json.RawMessage
}

// check validates a proposed document with env.
func (s Source) check(env Env) (*Result, error) {
	hasYAML, hasDoc := s.YAML != "", len(s.Doc) > 0 && string(s.Doc) != "null"
	switch {
	case hasYAML == hasDoc:
		return nil, ErrBadSource
	case hasYAML:
		return Check([]byte(s.YAML), env), nil
	}
	return CheckDoc(s.Doc, env), nil
}

// ValidateResult is api.ts ValidateResult.
type ValidateResult struct {
	OK             bool                        `json:"ok"`
	Errors         []FieldError                `json:"errors"`
	Warnings       []FieldError                `json:"warnings"`
	BlockReadiness map[string][]BlockReadiness `json:"block_readiness"`
	Diff           string                      `json:"diff"`
}

// Validate checks a proposed document against the active policy: errors, warnings, block
// readiness per route and a diff of the YAML.
func (e *Engine) Validate(src Source) (*ValidateResult, error) {
	res, err := src.check(e.cfg.Env)
	if err != nil {
		return nil, err
	}
	out := &ValidateResult{OK: res.Policy != nil, Errors: nonNilErrs(res.Errors), Warnings: nonNilErrs(res.Warnings),
		BlockReadiness: map[string][]BlockReadiness{}}
	text := src.YAML
	if res.Policy != nil {
		text = res.Policy.YAML
		out.BlockReadiness = res.Policy.BlockReadiness(e.cfg.Env.Specs, e.cfg.Env, e.Runtime())
	} else if text == "" {
		text, _ = yamlFromDoc(src.Doc)
	}
	cur := e.State()
	out.Diff = UnifiedDiff("v"+strconv.FormatInt(cur.Version.ID, 10), "proposed", cur.Version.YAML, text)
	return out, nil
}

func nonNilErrs(errs []FieldError) []FieldError {
	if errs == nil {
		return []FieldError{}
	}
	return errs
}

// SaveRequest is a dashboard edit (PUT /api/policies).
type SaveRequest struct {
	Source  Source
	Message string
	Author  string
	// IfMatch is the request's If-Match header.
	IfMatch string
}

// writable reports why the policy cannot be written, if it cannot.
func (e *Engine) writable() error {
	switch {
	case e.cfg.Store == nil:
		return ErrFileManaged
	case e.cfg.Lock:
		return ErrLocked
	}
	return nil
}

// Save stores a dashboard version when If-Match names the active version.
func (e *Engine) Save(ctx context.Context, req SaveRequest) (*State, error) {
	if err := e.writable(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.IfMatch) == "" {
		return nil, ErrPreconditionRequired
	}
	res, err := req.Source.check(e.cfg.Env)
	if err != nil {
		return nil, err
	}
	if res.Policy == nil {
		return nil, res.Err()
	}
	return e.write(ctx, func(tx store.PolicyTx, head *store.PolicyVersionRecord) (*store.PolicyVersionRecord, *Policy, error) {
		if !ifMatch(req.IfMatch, head) {
			return nil, nil, ErrStaleVersion
		}
		return record(res.Policy, store.PolicySourceDashboard, req.Author, req.Message, nil, headID(head)), res.Policy, nil
	})
}

// Rollback makes an earlier version active again as a new rollback version. Rolling back to
// the content that is already active changes nothing.
func (e *Engine) Rollback(ctx context.Context, id int64, author, message string) (*State, error) {
	if err := e.writable(); err != nil {
		return nil, err
	}
	target, err := e.cfg.Store.GetPolicyVersion(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	res := Check([]byte(target.YAML), e.cfg.Env)
	if res.Policy == nil {
		return nil, res.Err()
	}
	if message == "" {
		message = "Roll back to v" + strconv.FormatInt(id, 10)
	}
	return e.write(ctx, func(tx store.PolicyTx, head *store.PolicyVersionRecord) (*store.PolicyVersionRecord, *Policy, error) {
		if head != nil && head.SHA256 == target.SHA256 {
			return nil, nil, nil
		}
		return record(res.Policy, store.PolicySourceRollback, author, message, nil, headID(head)), res.Policy, nil
	})
}

// AdoptFile makes the policy file the active version (clears diverged).
func (e *Engine) AdoptFile(ctx context.Context, author, message string) (*State, error) {
	if e.cfg.Store == nil {
		return nil, ErrFileManaged
	}
	e.mu.Lock()
	e.file = e.readFile()
	file := e.file
	e.mu.Unlock()
	if !file.exists {
		return nil, ErrFileMissing
	}
	if !file.valid() {
		if file.err != nil {
			return nil, file.err
		}
		return nil, file.res.Err()
	}
	if message == "" {
		message = "Adopted " + e.cfg.File
	}
	sha := file.sha
	return e.write(ctx, func(tx store.PolicyTx, head *store.PolicyVersionRecord) (*store.PolicyVersionRecord, *Policy, error) {
		last, err := tx.LastFileVersion(ctx)
		if err != nil {
			return nil, nil, err
		}
		if head != nil && head.SHA256 == file.res.Policy.SHA256 && last != nil && last.FileSHA256 != nil && *last.FileSHA256 == sha {
			return nil, nil, nil
		}
		return record(file.res.Policy, store.PolicySourceAdoptFile, author, message, &sha, headID(head)), file.res.Policy, nil
	})
}

func headID(head *store.PolicyVersionRecord) *int64 {
	if head == nil {
		return nil
	}
	return &head.ID
}

// ifMatch reports whether the If-Match header matches the head version (RFC 9110 strong
// comparison; "*" matches any existing version). Unquoted tags are accepted too.
func ifMatch(header string, head *store.PolicyVersionRecord) bool {
	if head == nil {
		return false
	}
	want := `"v` + strconv.FormatInt(head.ID, 10) + `"`
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || tag == want || `"`+tag+`"` == want {
			return true
		}
	}
	return false
}

// write inserts the row build returns under the policy lock and activates it. build returning a
// nil row means there is nothing to write; the active state is returned.
func (e *Engine) write(ctx context.Context, build func(store.PolicyTx, *store.PolicyVersionRecord) (*store.PolicyVersionRecord, *Policy, error)) (*State, error) {
	var (
		rec *store.PolicyVersionRecord
		pol *Policy
	)
	err := e.cfg.Store.WithPolicyLock(ctx, e.cfg.Channel, func(tx store.PolicyTx) error {
		head, err := tx.Head(ctx)
		if err != nil {
			return err
		}
		if rec, pol, err = build(tx, head); err != nil || rec == nil {
			return err
		}
		return tx.Insert(ctx, rec)
	})
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return e.State(), nil
	}
	cp := *pol // the validated policy may be shared with the file snapshot
	pol = &cp
	pol.Version, pol.Source = rec.ID, rec.Source
	if rec.FileSHA256 != nil {
		pol.Path = e.cfg.File
	}
	e.mu.Lock()
	cur := e.state.Load()
	next := *cur
	if rec.ID > cur.Version.ID {
		next.Policy, next.Version, next.Warnings = pol, versionOf(rec), nil
	}
	if status, err := e.syncFile(ctx); err == nil {
		next.File = e.fileState(status)
	}
	e.state.Store(&next)
	e.mu.Unlock()
	e.emitUpdate(&next)
	return &next, nil
}

// Versions lists versions, newest first. Without a database there is only the active one.
func (e *Engine) Versions(ctx context.Context, limit int, cursor string) ([]VersionMeta, string, error) {
	if e.cfg.Store == nil {
		if cursor != "" {
			return nil, "", store.ErrBadCursor
		}
		return []VersionMeta{e.State().Version.VersionMeta}, "", nil
	}
	rows, next, err := e.cfg.Store.ListPolicyVersions(ctx, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]VersionMeta, len(rows))
	for i := range rows {
		out[i] = versionOf(&rows[i]).VersionMeta
	}
	return out, next, nil
}

// Version returns one version with its document.
func (e *Engine) Version(ctx context.Context, id int64) (*Version, error) {
	if e.cfg.Store == nil {
		if v := e.State().Version; v.ID == id {
			return &v, nil
		}
		return nil, ErrVersionNotFound
	}
	rec, err := e.cfg.Store.GetPolicyVersion(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	v := versionOf(rec)
	return &v, nil
}

// Proposed validates the policy a dry-run previews: a document, or a stored version.
func (e *Engine) Proposed(ctx context.Context, src Source, version *int64) (*Policy, error) {
	env := e.cfg.Env
	// Preview is free and changes nothing, so block and fallback may be previewed on a build
	// that does not enforce them yet (the result says so).
	env.Enforcing = true
	if version != nil {
		if src.YAML != "" || len(src.Doc) > 0 {
			return nil, ErrBadSource
		}
		v, err := e.Version(ctx, *version)
		if err != nil {
			return nil, err
		}
		src = Source{YAML: v.YAML}
	}
	res, err := src.check(env)
	if err != nil {
		return nil, err
	}
	if res.Policy == nil {
		return nil, res.Err()
	}
	return res.Policy, nil
}
