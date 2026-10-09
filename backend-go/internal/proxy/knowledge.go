package proxy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
)

// Knowledge on the hot path (SPEC §7.1, D65). A route whose evidence.sources name
// knowledge asks the knowledge add-on (Options.Knowledge) for documents about the user's request:
// the retrieval starts together with the upstream call, with the query only, and is joined
// before the gate within the route's evidence.knowledge.deadline_ms. The documents join the
// request's evidence (evidence.Result.AddKnowledge) and the trace's evidence summary reports
// what happened (knowledge used, no_hits, timeout, error or disabled). A retrieval that misses
// its deadline or fails leaves the request without knowledge: it never fails the request and
// never holds the answer past the deadline. Evidence that came from the knowledge base alone sets
// Exchange.KnowledgeOnly, which caps a critical grounding level at warn.

// knowledgeWhenAlways is the evidence.knowledge.when value that retrieves even when the request
// carries evidence of its own (the default, fallback, retrieves only without it).
const knowledgeWhenAlways = "always"

// defaultKnowledgeDeadline bounds a retrieval whose route sets no deadline_ms (the policy loader
// fills in 150 ms, as TRUTHTABLE_KNOWLEDGE_DEADLINE does).
const defaultKnowledgeDeadline = 150 * time.Millisecond

// ErrKnowledgeDisabled is what a Retriever returns when the knowledge add-on is off (the engine
// answers knowledge_disabled): the summary reports knowledge disabled rather than an error.
var ErrKnowledgeDisabled = errors.New("proxy: the knowledge add-on is off")

// KnowledgeOptions are the route's evidence.knowledge limits a retrieval runs with: at most TopK
// documents (0: the add-on's default) scoring at least MinScore (0: its default).
type KnowledgeOptions struct {
	TopK     int
	MinScore float64
}

type knowledgeOptionsKey struct{}

// WithKnowledgeOptions returns ctx carrying the route's knowledge limits for a Retriever: the
// handler puts them on the context of every retrieval it starts.
func WithKnowledgeOptions(ctx context.Context, o KnowledgeOptions) context.Context {
	return context.WithValue(ctx, knowledgeOptionsKey{}, o)
}

// KnowledgeOptionsFrom returns the knowledge limits on ctx; ok is false when there are none.
func KnowledgeOptionsFrom(ctx context.Context) (o KnowledgeOptions, ok bool) {
	o, ok = ctx.Value(knowledgeOptionsKey{}).(KnowledgeOptions)
	return o, ok
}

// knowledgeFetch is one retrieval in flight. docs and err are set before done is closed.
type knowledgeFetch struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	docs   []evidence.Item
	err    error
	topK   int
}

// startKnowledge starts the knowledge retrieval of f when its route reads the knowledge source
// and there is a retriever (the add-on is on); call it once the route is final, just before the
// upstream call. It records why no retrieval starts in the evidence's knowledge state: a route
// with when: fallback whose request carries evidence of its own, or a request with no user text
// to search for, did not request knowledge. Without a retriever the state stays unset, which the
// summary reports as disabled.
func (h *Handler) startKnowledge(f *flow) {
	x := f.x
	if h.knowl == nil || f.knowledge != nil || x.Evidence == nil || f.route == nil ||
		!slices.Contains(f.route.Evidence.Sources, evidence.SourceKnowledge) {
		return
	}
	ks := f.route.Evidence.Knowledge
	query := strings.TrimSpace(x.Evidence.UserRequest)
	if query == "" || ks.When != knowledgeWhenAlways && x.Evidence.HasRequestEvidence() {
		x.Evidence.Knowledge = evidence.KnowledgeNotRequested
		return
	}
	deadline := time.Duration(ks.DeadlineMs) * time.Millisecond
	if deadline <= 0 {
		deadline = defaultKnowledgeDeadline
	}
	// The record context: a client that goes away still gets its answer checked, with knowledge.
	ctx, cancel := context.WithTimeout(WithKnowledgeOptions(f.record, KnowledgeOptions{TopK: ks.TopK, MinScore: ks.MinScore}), deadline)
	k := &knowledgeFetch{ctx: ctx, cancel: cancel, done: make(chan struct{}), topK: ks.TopK}
	f.knowledge = k
	retriever := h.knowl
	go func() {
		defer close(k.done)
		k.docs, k.err = retriever.Retrieve(ctx, query, "")
	}()
}

// joinKnowledge waits for f's retrieval, at most until its deadline, and joins the documents to
// the evidence; it runs once, before the gate (evaluate). An answer that will not be checked (a
// skip reason, no text) does not wait: its retrieval is cancelled and knowledge counts as not
// requested.
func (h *Handler) joinKnowledge(f *flow) {
	k := f.knowledge
	if k == nil {
		return
	}
	f.knowledge = nil
	defer k.cancel()
	x := f.x
	if x.SkipReason != "" || x.Response == "" {
		x.Evidence.Knowledge = evidence.KnowledgeNotRequested
		return
	}
	select {
	case <-k.done:
	case <-k.ctx.Done():
		select {
		case <-k.done: // it answered right at the deadline
		default:
			h.log.Debug("knowledge retrieval missed its deadline", "trace_id", x.Trace.TraceID, "route", f.route.Name)
			x.Evidence.Knowledge = evidence.KnowledgeTimeout
			return
		}
	}
	switch err := k.err; {
	case err == nil:
		docs := k.docs
		if k.topK > 0 && len(docs) > k.topK {
			docs = docs[:k.topK]
		}
		x.Evidence.AddKnowledge(docs, f.route.Evidence.Sources)
	case errors.Is(err, ErrKnowledgeDisabled):
		x.Evidence.Knowledge = evidence.KnowledgeDisabled
	case errors.Is(err, context.DeadlineExceeded):
		x.Evidence.Knowledge = evidence.KnowledgeTimeout
	default:
		h.log.Warn("knowledge retrieval failed; checking without knowledge", "trace_id", x.Trace.TraceID, "route", f.route.Name, "err", err)
		x.Evidence.Knowledge = evidence.KnowledgeError
	}
	x.KnowledgeOnly = x.Evidence.KnowledgeOnly()
}

// evaluate gates attempt 0 of f on ctx (gate.Evaluate), once its knowledge retrieval, if one
// runs, is joined.
func (h *Handler) evaluate(ctx context.Context, f *flow) *gate.Result {
	h.joinKnowledge(f)
	return h.gate.Evaluate(ctx, f.x)
}
