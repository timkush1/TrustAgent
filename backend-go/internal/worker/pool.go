// Package worker runs deep audits (SPEC §3.1 internal/worker): two lanes, a token bucket that
// keeps the high lane to TRUTHTABLE_AUDIT_HIGH_PER_MIN, one Evaluate call per audit (M5: no
// polling), persistence before broadcast, and a drain at shutdown (M6).
//
// Every audit's lifecycle is broadcast over WS v2 (SPEC §2.5): audit_queued when Submit puts it in
// a lane, audit_started when a worker takes it (the trace's audit status is then persisted as
// running), and audit_completed, audit_failed or audit_dropped at the end. Submit reports a full
// queue or a stopped pool as an error (the trace records the audit as dropped), and Drain lets
// queued audits finish within the drain timeout.
//
// Every audit is held against the daily LLM cap that SetBudget installs (D48): Submit
// reserves the expected cost of one audit and refuses the audit with ErrLLMBudget when the cap
// cannot take it, in every lane; the worker settles the hold with what the engine's cost records
// count against the cap (Budget.CapCostUSD, D91), and the records are stored with the audit and
// linked to it by audit_id (D45). The cost recorder (SetCostRecorder) then receives the records
// of every answered audit. The pool keeps no capacity aside for uniform samples and does not
// retry a failed audit.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
)

// Lanes.
const (
	LaneHigh = "high"
	LaneLow  = "low"
)

// Submit errors (the reasons of audit_dropped).
var (
	ErrQueueFull = errors.New("worker: audit queue is full")
	ErrStopped   = errors.New("worker: the audit pool is stopped")
	ErrNoEngine  = errors.New("worker: no audit engine is configured")
	// ErrLLMBudget refuses an audit the day's LLM cap cannot take (D48): hard in every lane.
	ErrLLMBudget = errors.New("worker: today's LLM spend cap is reached")
)

// Drop reasons (audit_dropped.reason) and audit_jobs_total outcomes of audits that did not run.
const (
	DropQueueFull = "queue_full"
	DropStopped   = "stopped"
	DropNoEngine  = "no_engine"
	DropLLMBudget = "llm_budget"
)

// componentDeepAudit is the component an audit's hold is booked under.
const componentDeepAudit = "deep_audit"

// ErrNoJudge is JudgeReady's answer when the engine has no working deep-audit judge: it would
// answer every audit not assessed.
var ErrNoJudge = errors.New("worker: the audit engine has no working judge")

// DropReason maps a Submit error to the audit_dropped reason.
func DropReason(err error) string {
	switch {
	case errors.Is(err, ErrStopped):
		return DropStopped
	case errors.Is(err, ErrNoEngine):
		return DropNoEngine
	case errors.Is(err, ErrLLMBudget):
		return DropLLMBudget
	default:
		return DropQueueFull
	}
}

// Evaluator is the engine RPC the pool calls (grpc.AuditClient).
type Evaluator interface {
	Evaluate(ctx context.Context, req *pb.AuditRequest) (*pb.AuditResult, error)
}

// Store persists finished audits (store.PostgresStore).
type Store interface {
	SaveAuditResult(ctx context.Context, record *store.AuditRecord, traceAuditStatus string) error
	SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error
}

// Publisher broadcasts stream events (websocket.Hub).
type Publisher interface {
	Publish(ev websocket.Event)
}

// Job is one deep audit. Request carries the engine request (trace_id, audit_id, query,
// response, context, gate, options, route, source); the other fields describe the stored audit.
type Job struct {
	Request *pb.AuditRequest
	// Lane is the requested lane; Submit may move a high job to the low lane.
	Lane          string
	Selection     string
	InclusionProb float64
	// ClientRequestID is the B4-valid client X-Request-ID of the trace, if any.
	ClientRequestID *string
	// KeyLabel is the label of the ingest key the audited answer came through ("" for none): the
	// key_label of the audit's cost rows.
	KeyLabel string
	// Gate is the gate decision the audit ran behind (nil for direct audits).
	Gate *store.AuditGate
	// EvidenceSummary and ContextUsed are the JSON of the request's EvidenceSummary and of the
	// EvidenceItem[] sent as context.
	EvidenceSummary []byte
	ContextUsed     []byte

	enqueued time.Time
	// queued is closed once Submit has broadcast audit_queued; the worker waits for it before it
	// broadcasts audit_started, so the stream never shows a start before the queueing.
	queued chan struct{}
	// hold is the audit's share of the LLM cap (nil without a budget); estimate is its amount.
	hold     func(actualUSD float64)
	estimate float64
}

// settle books the audit's real LLM cost against its hold, once; without a hold it does nothing.
func (j *Job) settle(actualUSD float64) {
	if j.hold != nil {
		j.hold(actualUSD)
		j.hold = nil
	}
}

// Config configures a Pool.
type Config struct {
	// Workers is TRUTHTABLE_WORKER_COUNT; LowQueue and HighQueue are TRUTHTABLE_QUEUE_SIZE and
	// TRUTHTABLE_AUDIT_QUEUE_HIGH; HighPerMin is TRUTHTABLE_AUDIT_HIGH_PER_MIN (0 sends every job
	// to the low lane).
	Workers    int
	LowQueue   int
	HighQueue  int
	HighPerMin int
	// Engine answers Evaluate; nil makes Submit fail with ErrNoEngine.
	Engine Evaluator
	// Store persists results; nil only broadcasts them.
	Store Store
	// Publisher broadcasts audit events; nil broadcasts nothing.
	Publisher Publisher
	// TextRetention is how long the audit's prompt and answer are kept (TRUTHTABLE_BODY_RETENTION).
	TextRetention time.Duration
	Now           func() time.Time
	Logger        *slog.Logger
}

// Budget is the daily LLM cap deep audits are held against (the cost ledger's LLM budget,
// D48): hard in every lane, with no admin bypass.
type Budget interface {
	// EstimateAuditUSD is what one audit is expected to cost: the amount an audit holds.
	EstimateAuditUSD() float64
	// Reserve holds usd of today's LLM cap for a call of component and returns the function that
	// books the call's real cost (0 releases the hold); it fails once the cap cannot take usd.
	Reserve(usd float64, component string) (settle func(actualUSD float64), err error)
	// HeadroomUSD is the LLM spend still allowed today (+Inf without a cap).
	HeadroomUSD() float64
	// PriceTableVersion is the price_table_version an LLM cost of model on provider records:
	// "override" when a dashboard override prices it, else the version of the table in force.
	PriceTableVersion(provider, model string) string
	// CapCostUSD is what an audit's cost records count against the cap (D91): their real cost,
	// with records no price covers at the reference judge's price.
	CapCostUSD(records []*pb.CostRecord) float64
}

// AuditCosts are the LLM cost records of one audit the engine answered.
type AuditCosts struct {
	TraceID, AuditID, Route, Selection string
	Records                            []*pb.CostRecord
}

// CostRecorder receives the cost records of every audit the engine answered, once its result is
// stored: the cost ledger learns what an audit costs from them (D48). It must not block.
type CostRecorder func(AuditCosts)

// Pool runs deep audits.
type Pool struct {
	cfg    Config
	high   chan *Job
	low    chan *Job
	bucket *tokenBucket

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.RWMutex
	stopping bool

	// judgeCheck is the check behind JudgeReady (SetJudgeCheck); nil means none.
	judgeCheck atomic.Pointer[func() error]
	// budget is the LLM cap (SetBudget) and recorder the cost recorder (SetCostRecorder); nil
	// means none.
	budget   atomic.Pointer[Budget]
	recorder atomic.Pointer[CostRecorder]
}

// New builds a pool; Start runs its workers.
func New(cfg Config) *Pool {
	if cfg.Workers <= 0 {
		cfg.Workers = 10
	}
	if cfg.LowQueue <= 0 {
		cfg.LowQueue = 1000
	}
	if cfg.HighQueue <= 0 {
		cfg.HighQueue = 200
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{
		cfg: cfg, high: make(chan *Job, cfg.HighQueue), low: make(chan *Job, cfg.LowQueue),
		bucket: newTokenBucket(cfg.HighPerMin, cfg.Now), ctx: ctx, cancel: cancel,
	}
}

// Start runs the workers.
func (p *Pool) Start() {
	for range p.cfg.Workers {
		p.wg.Add(1)
		go p.work()
	}
}

// HasEngine reports whether audits can run at all.
func (p *Pool) HasEngine() bool { return p.cfg.Engine != nil }

// SetJudgeCheck installs the check JudgeReady runs: nil when the engine can judge an audit, an
// error such as ErrNoJudge when it cannot. The proxy reads it from the engine's cached health.
func (p *Pool) SetJudgeCheck(check func() error) {
	if check == nil {
		p.judgeCheck.Store(nil)
		return
	}
	p.judgeCheck.Store(&check)
}

// SetBudget installs the daily LLM cap audits are held against (nil removes it). It is safe to
// call before Start and while audits run.
func (p *Pool) SetBudget(b Budget) {
	if b == nil {
		p.budget.Store(nil)
		return
	}
	p.budget.Store(&b)
}

// SetCostRecorder installs the recorder of the audits' cost records (nil removes it). It is safe
// to call before Start and while audits run.
func (p *Pool) SetCostRecorder(r CostRecorder) {
	if r == nil {
		p.recorder.Store(nil)
		return
	}
	p.recorder.Store(&r)
}

// JudgeReady reports whether the engine can judge a new audit (nil when no check is installed).
// Callers that ask for an audit explicitly (POST /api/audit) refuse the request when it cannot;
// gated traffic is audited anyway and comes back not assessed.
func (p *Pool) JudgeReady() error {
	if check := p.judgeCheck.Load(); check != nil {
		return (*check)()
	}
	return nil
}

// Submit queues a job without blocking and returns the lane it went to. A high job takes a token
// from the high-lane bucket, or goes to the low lane when the bucket or the high queue is full.
//
// The job first holds one audit's expected cost of the day's LLM cap (D48), in every lane: past
// the cap Submit fails with ErrLLMBudget, and when the cap cannot take one more audit after this
// one the engine is told so (AuditOptions.llm_budget_exhausted: it skips optional retries and
// explanations). It also fails with ErrNoEngine, ErrStopped or ErrQueueFull; a job that is not
// queued is not run and holds nothing.
func (p *Pool) Submit(job *Job) (string, error) {
	if p.cfg.Engine == nil {
		p.count(job.Lane, job.Selection, "dropped")
		return "", ErrNoEngine
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.stopping {
		p.count(job.Lane, job.Selection, "dropped")
		return "", ErrStopped
	}
	assignIDs(job)
	if !p.reserve(job) {
		p.count(job.Lane, job.Selection, DropLLMBudget)
		return "", ErrLLMBudget
	}
	job.enqueued = p.cfg.Now()
	job.queued = make(chan struct{})
	// A full high queue does not spend a token: the job goes to the low lane either way.
	if job.Lane == LaneHigh && len(p.high) < cap(p.high) && p.bucket.take() {
		select {
		case p.high <- job:
			p.announceQueued(job, LaneHigh, len(p.high))
			return LaneHigh, nil
		default:
		}
	}
	select {
	case p.low <- job:
		p.announceQueued(job, LaneLow, len(p.low))
		return LaneLow, nil
	default:
		job.settle(0)
		p.count(job.Lane, job.Selection, "dropped")
		return "", ErrQueueFull
	}
}

// reserve holds the expected cost of job's audit against the LLM cap and reports whether the cap
// took it (always, without a budget). When the headroom left after the hold is below one more
// audit, the engine is asked to keep this one lean (llm_budget_exhausted).
func (p *Pool) reserve(job *Job) bool {
	bp := p.budget.Load()
	if bp == nil {
		return true
	}
	b := *bp
	est := max(b.EstimateAuditUSD(), 0)
	hold, err := b.Reserve(est, componentDeepAudit)
	if err != nil {
		return false
	}
	job.hold, job.estimate = hold, est
	if est > 0 && b.HeadroomUSD() < est {
		if job.Request.Options == nil {
			job.Request.Options = &pb.AuditOptions{}
		}
		job.Request.Options.LlmBudgetExhausted = true
	}
	return true
}

// announceQueued records a job that entered lane, with depth jobs in that lane counting it: the
// queue gauge and the audit_queued event, whose queue_depth is the number of audits waiting ahead
// of it. It then lets the worker that took the job announce the start.
func (p *Pool) announceQueued(job *Job, lane string, depth int) {
	defer close(job.queued)
	metrics.AuditQueueDepth.WithLabelValues(lane).Set(float64(depth))
	if p.cfg.Publisher == nil {
		return
	}
	ev, err := queuedEvent(job.Request.GetTraceId(), job.Request.GetAuditId(), lane, max(depth-1, 0))
	if err != nil {
		p.cfg.Logger.Error("deep audit: event does not marshal", "audit_id", job.Request.GetAuditId(), "err", err)
		return
	}
	p.cfg.Publisher.Publish(ev)
}

// assignIDs fills in the server-generated ids of a job (B4): a UUIDv7 audit_id, and trace_id =
// audit_id when the submitter has no trace. A client-supplied id never becomes one.
func assignIDs(job *Job) {
	if job.Request == nil {
		job.Request = &pb.AuditRequest{}
	}
	if job.Request.AuditId == "" {
		job.Request.AuditId = store.NewID()
	}
	if job.Request.TraceId == "" {
		job.Request.TraceId = job.Request.AuditId
	}
	if job.Request.RequestId == "" {
		job.Request.RequestId = job.Request.TraceId
	}
}

// QueueDepth returns the number of queued jobs in a lane.
func (p *Pool) QueueDepth(lane string) int {
	if lane == LaneHigh {
		return len(p.high)
	}
	return len(p.low)
}

// Drain stops accepting jobs and waits for the queued and running ones. When ctx ends first,
// running audits are cancelled and the rest are recorded as dropped (reason stopped); Drain then
// returns ctx's error once the workers have exited.
func (p *Pool) Drain(ctx context.Context) error {
	p.mu.Lock()
	if !p.stopping {
		p.stopping = true
		close(p.high)
		close(p.low)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		p.cancel()
		return nil
	case <-ctx.Done():
		p.cancel()
		<-done
		return ctx.Err()
	}
}

// work runs jobs, high lane first, until both lanes are closed and empty.
func (p *Pool) work() {
	defer p.wg.Done()
	high, low := p.high, p.low
	for high != nil || low != nil {
		// Prefer the high lane whenever it has a job.
		if high != nil {
			select {
			case job, ok := <-high:
				if !ok {
					high = nil
					continue
				}
				p.run(job, LaneHigh)
				continue
			default:
			}
		}
		select {
		case job, ok := <-high:
			if !ok {
				high = nil
				continue
			}
			p.run(job, LaneHigh)
		case job, ok := <-low:
			if !ok {
				low = nil
				continue
			}
			p.run(job, LaneLow)
		}
	}
}

// run evaluates one job, persists the outcome and broadcasts it.
func (p *Pool) run(job *Job, lane string) {
	metrics.AuditQueueDepth.WithLabelValues(lane).Set(float64(p.QueueDepth(lane)))
	req := job.Request
	log := p.cfg.Logger.With("trace_id", req.GetTraceId(), "audit_id", req.GetAuditId())
	if p.ctx.Err() != nil {
		job.settle(0)
		p.drop(job, lane, DropStopped)
		return
	}
	started := p.cfg.Now()
	p.announceStarted(job, started, log)
	res, err := p.cfg.Engine.Evaluate(p.ctx, req)
	finished := p.cfg.Now()
	if err != nil && p.ctx.Err() != nil {
		// Cancelled mid-call at shutdown: the engine may have spent the estimate.
		job.settle(job.estimate)
		p.drop(job, lane, DropStopped)
		return
	}

	var (
		rec  *store.AuditRecord
		rows []store.CostEvent
	)
	if err != nil {
		kind := rpcFailureKind(err)
		log.Warn("deep audit: the engine call failed", "kind", kind, "err", err)
		job.settle(failedCallCost(err, job.estimate))
		rec = failedRecord(job, lane, kind, "The audit engine could not complete this audit.", started, finished, p.cfg.TextRetention)
	} else {
		job.settle(p.capCostUSD(res.GetCosts()))
		rec = resultRecord(job, lane, res, started, finished, p.cfg.TextRetention)
		rows = costRows(job, res.GetCosts(), finished, p.priceVersion)
	}
	status := traceAuditStatus(rec)
	if p.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.save(ctx, rec, status, rows); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				cancel()
				log.Warn("deep audit: result already stored; not broadcasting it again")
				return
			}
			log.Error("deep audit: persisting the result failed", "err", err)
		}
		cancel()
	}
	metrics.AuditJobs.WithLabelValues(lane, job.Selection, status).Inc()
	metrics.AuditRoundtrip.Observe(finished.Sub(job.enqueued).Seconds())
	if err == nil {
		p.recordCosts(job, res)
	}
	p.publish(rec)
}

// LinkedCostStore stores an audit together with its cost_events rows as given, linked to the
// audit by audit_id with their key label and price table version (store.PostgresStore, D45). A
// store without it writes the rows SaveAuditResult derives from the record.
type LinkedCostStore interface {
	SaveAuditResultWithCosts(ctx context.Context, record *store.AuditRecord, traceAuditStatus string, costs []store.CostEvent) error
}

// save persists a finished audit with its cost rows.
func (p *Pool) save(ctx context.Context, rec *store.AuditRecord, status string, rows []store.CostEvent) error {
	if ls, ok := p.cfg.Store.(LinkedCostStore); ok {
		return ls.SaveAuditResultWithCosts(ctx, rec, status, rows)
	}
	return p.cfg.Store.SaveAuditResult(ctx, rec, status)
}

// capCostUSD is what an answered audit's records count against the LLM cap (Budget.CapCostUSD,
// D91); without a budget there is no hold to settle, and the real cost stands in.
func (p *Pool) capCostUSD(records []*pb.CostRecord) float64 {
	if b := p.budget.Load(); b != nil {
		return (*b).CapCostUSD(records)
	}
	return costs.RealCostUSD(records)
}

// priceVersion is the price_table_version of an LLM cost ("" without a budget, which has no
// price book).
func (p *Pool) priceVersion(provider, model string) string {
	if b := p.budget.Load(); b != nil {
		return (*b).PriceTableVersion(provider, model)
	}
	return ""
}

// failedCallCost is what an engine call that returned no result is booked at (D48): nothing when
// the engine refused it before calling a model (unavailable, or a request it would not take), the
// estimate otherwise (a deadline, an internal error, a cancellation: the judge may have been
// called).
func failedCallCost(err error, estimate float64) float64 {
	switch status.Code(err) {
	case codes.Unavailable, codes.InvalidArgument, codes.ResourceExhausted, codes.FailedPrecondition,
		codes.Unauthenticated, codes.PermissionDenied, codes.Unimplemented:
		return 0
	}
	return estimate
}

// recordCosts hands an answered audit's cost records to the cost recorder, if one is installed.
func (p *Pool) recordCosts(job *Job, res *pb.AuditResult) {
	r := p.recorder.Load()
	if r == nil {
		return
	}
	req := job.Request
	(*r)(AuditCosts{TraceID: req.GetTraceId(), AuditID: req.GetAuditId(), Route: req.GetRoute(), Selection: job.Selection,
		Records: res.GetCosts()})
}

// announceStarted records that a worker took job at started: the trace's audit status becomes
// running, then audit_started is broadcast with the time the job waited in its lane. It waits for
// Submit's audit_queued first.
func (p *Pool) announceStarted(job *Job, started time.Time, log *slog.Logger) {
	if job.queued != nil {
		<-job.queued
	}
	id, traceID := job.Request.GetAuditId(), job.Request.GetTraceId()
	if p.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.cfg.Store.SetTraceAudit(ctx, traceID, store.TraceAuditUpdate{AuditID: &id, Status: store.AuditRunning}); err != nil {
			log.Error("deep audit: recording the running audit failed", "err", err)
		}
		cancel()
	}
	if p.cfg.Publisher == nil {
		return
	}
	ev, err := startedEvent(traceID, id, max(started.Sub(job.enqueued).Milliseconds(), 0))
	if err != nil {
		log.Error("deep audit: event does not marshal", "err", err)
		return
	}
	p.cfg.Publisher.Publish(ev)
}

// drop records a job that will not run.
func (p *Pool) drop(job *Job, lane, reason string) {
	id := job.Request.GetAuditId()
	if p.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.cfg.Store.SetTraceAudit(ctx, job.Request.GetTraceId(), store.TraceAuditUpdate{AuditID: &id, Status: store.TraceAuditDropped}); err != nil {
			p.cfg.Logger.Error("deep audit: recording a dropped audit failed", "audit_id", id, "err", err)
		}
		cancel()
	}
	p.count(lane, job.Selection, reason)
	if p.cfg.Publisher != nil {
		if ev, err := DroppedEvent(job.Request.GetTraceId(), &id, reason); err == nil {
			p.cfg.Publisher.Publish(ev)
		}
	}
}

func (p *Pool) count(lane, selection, outcome string) {
	if lane == "" {
		lane = LaneLow
	}
	metrics.AuditJobs.WithLabelValues(lane, selection, outcome).Inc()
}

// publish broadcasts a finished audit: audit_completed, or audit_failed for a failed one.
func (p *Pool) publish(rec *store.AuditRecord) {
	if p.cfg.Publisher == nil {
		return
	}
	var (
		ev  websocket.Event
		err error
	)
	if rec.Status == store.AuditFailed {
		ev, err = failedEvent(rec)
	} else {
		ev, err = CompletedEvent(rec)
	}
	if err != nil {
		p.cfg.Logger.Error("deep audit: event does not marshal", "audit_id", rec.AuditID, "err", err)
		return
	}
	p.cfg.Publisher.Publish(ev)
}

// tokenBucket allows perMin tokens a minute, refilled continuously, with a burst of perMin.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newTokenBucket(perMin int, now func() time.Time) *tokenBucket {
	b := &tokenBucket{rate: float64(perMin) / 60, burst: float64(perMin), tokens: float64(perMin), now: now}
	b.last = now()
	return b
}

func (b *tokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
