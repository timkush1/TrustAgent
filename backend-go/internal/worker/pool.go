// Package worker runs deep audits (SPEC §3.1 internal/worker): two lanes, a token bucket that
// keeps the high lane to TRUTHTABLE_AUDIT_HIGH_PER_MIN, one Evaluate call per audit (M5: no
// polling), persistence before broadcast, and a drain at shutdown (M6).
//
// S0 keeps the minimal lane model: Submit reports a full queue or a stopped pool as an error
// (the trace records the audit as dropped), and Drain lets queued audits finish within the drain
// timeout. The uniform-sample reserved capacity, LLM spend reservations, retries and the
// audit_queued/audit_started lifecycle events arrive in P2.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
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
)

// DropReason maps a Submit error to the audit_dropped reason.
func DropReason(err error) string {
	switch {
	case errors.Is(err, ErrStopped):
		return "stopped"
	case errors.Is(err, ErrNoEngine):
		return "no_engine"
	default:
		return "queue_full"
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
	// Gate is the gate decision the audit ran behind (nil for direct audits).
	Gate *store.AuditGate
	// EvidenceSummary and ContextUsed are the JSON of the request's EvidenceSummary and of the
	// EvidenceItem[] sent as context.
	EvidenceSummary []byte
	ContextUsed     []byte

	enqueued time.Time
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

// Submit queues a job without blocking and returns the lane it went to. A high job takes a token
// from the high-lane bucket, or goes to the low lane when the bucket or the high queue is full.
// It fails with ErrNoEngine, ErrStopped or ErrQueueFull; the job is then not run.
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
	job.enqueued = p.cfg.Now()
	// A full high queue does not spend a token: the job goes to the low lane either way.
	if job.Lane == LaneHigh && len(p.high) < cap(p.high) && p.bucket.take() {
		select {
		case p.high <- job:
			metrics.AuditQueueDepth.WithLabelValues(LaneHigh).Set(float64(len(p.high)))
			return LaneHigh, nil
		default:
		}
	}
	select {
	case p.low <- job:
		metrics.AuditQueueDepth.WithLabelValues(LaneLow).Set(float64(len(p.low)))
		return LaneLow, nil
	default:
		p.count(job.Lane, job.Selection, "dropped")
		return "", ErrQueueFull
	}
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
		p.drop(job, lane, "stopped")
		return
	}
	started := p.cfg.Now()
	res, err := p.cfg.Engine.Evaluate(p.ctx, req)
	finished := p.cfg.Now()
	if err != nil && p.ctx.Err() != nil {
		p.drop(job, lane, "stopped")
		return
	}

	var rec *store.AuditRecord
	if err != nil {
		kind := rpcFailureKind(err)
		log.Warn("deep audit: the engine call failed", "kind", kind, "err", err)
		rec = failedRecord(job, lane, kind, "The audit engine could not complete this audit.", started, finished, p.cfg.TextRetention)
	} else {
		rec = resultRecord(job, lane, res, finished, p.cfg.TextRetention)
	}
	status := traceAuditStatus(rec)
	if p.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.cfg.Store.SaveAuditResult(ctx, rec, status); err != nil {
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
	p.publish(rec)
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
