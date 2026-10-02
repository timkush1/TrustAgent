package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
)

// knowledgeCall is one Retrieve call a fakeRetriever saw.
type knowledgeCall struct {
	query, response string
	opts            KnowledgeOptions
	hasOpts         bool
	deadline        time.Duration
}

// fakeRetriever answers every retrieval with docs or err; with hang it waits for the retrieval's
// context to end instead. ended receives each retrieval's context error once it ends.
type fakeRetriever struct {
	docs  []evidence.Item
	err   error
	hang  bool
	ended chan error

	mu    sync.Mutex
	calls []knowledgeCall
}

func (r *fakeRetriever) Retrieve(ctx context.Context, query, response string) ([]evidence.Item, error) {
	call := knowledgeCall{query: query, response: response}
	call.opts, call.hasOpts = KnowledgeOptionsFrom(ctx)
	if d, ok := ctx.Deadline(); ok {
		call.deadline = time.Until(d)
	}
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.hang {
		<-ctx.Done()
		if r.ended != nil {
			r.ended <- ctx.Err()
		}
		return nil, ctx.Err()
	}
	return slices.Clone(r.docs), r.err
}

func (r *fakeRetriever) seen() []knowledgeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// knowledgePolicy is one route of mode with the evidence sources and knowledge settings given.
func knowledgePolicy(mode, sources, knowledge string) string {
	return blockPolicy(mode, "evidence: {sources: ["+sources+"], knowledge: {"+knowledge+"}}")
}

// knowledgeRequest is a chat request whose system prompt carries no document, so the request has no
// evidence of its own; with doc it carries one.
func knowledgeRequest(t *testing.T, doc bool, answer string) string {
	t.Helper()
	system := "You are the Acme support assistant."
	if doc {
		system += "\n\n<document id=\"refund-policy.md\">Annual plans can be refunded within 30 days of purchase.</document>"
	}
	body := map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": "How long is the refund window on annual plans?"},
		},
		"test_response": answer,
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var kbDocs = []evidence.Item{
	{ID: "handbook-refunds:2", Label: "Refund handbook", Text: "Annual plans can be refunded within 30 days of purchase."},
	{ID: "not a valid id!", Text: "Monthly plans are non-refundable."},
	{ID: "handbook-refunds:3", Text: "  Annual plans can be refunded within   30 days of purchase. "}, // a duplicate
	{ID: "handbook-refunds:4", Text: "Refunds go back to the original payment method."},
}

func TestKnowledgeJoinsTheEvidenceBeforeTheGate(t *testing.T) {
	kb := &fakeRetriever{docs: kbDocs}
	e := newEnv(t, envOpts{testMode: true, knowledge: kb,
		policyYAML: knowledgePolicy(decision.ModeFlag, "request_system, knowledge", "when: fallback, top_k: 3, deadline_ms: 500, min_score: 0.4")})
	w := e.do(t, "/v1/chat/completions", knowledgeRequest(t, false, "Annual plans can be refunded within 30 days."), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	r := e.sink.result(t)

	calls := kb.seen()
	if len(calls) != 1 {
		t.Fatalf("%d retrievals, want 1", len(calls))
	}
	c := calls[0]
	if c.query != "How long is the refund window on annual plans?" || c.response != "" {
		t.Errorf("retrieved with query %q and response %q, want the user request only", c.query, c.response)
	}
	if !c.hasOpts || c.opts != (KnowledgeOptions{TopK: 3, MinScore: 0.4}) {
		t.Errorf("retrieval options %+v (set %v), want the route's top_k and min_score", c.opts, c.hasOpts)
	}
	if c.deadline <= 0 || c.deadline > 500*time.Millisecond {
		t.Errorf("retrieval deadline %v, want the route's 500ms at most", c.deadline)
	}

	// top_k 3 keeps the first three documents; the third repeats the first and is dropped.
	var got []evidence.Item
	for _, it := range r.Evidence {
		if it.Source == evidence.SourceKnowledge {
			got = append(got, it)
		}
	}
	want := []evidence.Item{
		{ID: "handbook-refunds:2", Source: evidence.SourceKnowledge, Label: "Refund handbook", Text: kbDocs[0].Text},
		{ID: "kb1", Source: evidence.SourceKnowledge, Label: evidence.LabelKnowledge, Text: kbDocs[1].Text},
	}
	if !slices.Equal(got, want) {
		t.Errorf("knowledge evidence %+v, want %+v", got, want)
	}
	sum := r.Decision.EvidenceSummary
	if sum == nil || sum.Knowledge != evidence.KnowledgeUsed || sum.CheckedAgainst() != "knowledge:2" {
		t.Fatalf("evidence summary %+v, want knowledge used with 2 items", sum)
	}
	if h := w.Header().Get(HeaderCheckedAgainst); h != "knowledge:2" {
		t.Errorf("%s %q, want knowledge:2", HeaderCheckedAgainst, h)
	}
	// The documents reached the decision model: they were packed into its request.
	if r.Decision.Compile == nil || !slices.Equal(r.Decision.Compile.PackedEvidenceIDs, []string{"handbook-refunds:2", "kb1"}) {
		t.Errorf("compile %+v, want both knowledge documents packed", r.Decision.Compile)
	}
}

func TestKnowledgeWhen(t *testing.T) {
	cases := []struct {
		name, when string
		doc        bool
		retrieved  bool
		state      string
		checked    string
	}{
		{name: "fallback without request evidence", when: "fallback", retrieved: true, state: evidence.KnowledgeUsed, checked: "knowledge:3"},
		{name: "fallback with request evidence", when: "fallback", doc: true, state: evidence.KnowledgeNotRequested, checked: "request_system:1"},
		// The request's own copy of a document wins over the knowledge base's.
		{name: "always with request evidence", when: "always", doc: true, retrieved: true, state: evidence.KnowledgeUsed, checked: "request_system:1,knowledge:2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kb := &fakeRetriever{docs: kbDocs}
			e := newEnv(t, envOpts{testMode: true, knowledge: kb,
				policyYAML: knowledgePolicy(decision.ModeFlag, "request_system, knowledge", "when: "+tc.when+", deadline_ms: 500")})
			w := e.do(t, "/v1/chat/completions", knowledgeRequest(t, tc.doc, "Within 30 days."), nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			r := e.sink.result(t)
			if got := len(kb.seen()) == 1; got != tc.retrieved {
				t.Errorf("retrieved %v, want %v", got, tc.retrieved)
			}
			sum := r.Decision.EvidenceSummary
			if sum.Knowledge != tc.state || sum.CheckedAgainst() != tc.checked {
				t.Errorf("knowledge %q, checked against %q; want %q, %q", sum.Knowledge, sum.CheckedAgainst(), tc.state, tc.checked)
			}
			// Request evidence comes first: the route lists request_system before knowledge.
			if len(r.Evidence) > 0 && tc.doc && r.Evidence[0].Source != evidence.SourceRequestSystem {
				t.Errorf("first evidence item %+v, want the request's document", r.Evidence[0])
			}
		})
	}
}

func TestKnowledgeDeadlineMissIsNoFailure(t *testing.T) {
	kb := &fakeRetriever{hang: true, ended: make(chan error, 1)}
	e := newEnv(t, envOpts{testMode: true, knowledge: kb,
		policyYAML: knowledgePolicy(decision.ModeFlag, "request_system, knowledge", "when: fallback, deadline_ms: 60")})
	start := time.Now()
	w := e.do(t, "/v1/chat/completions", knowledgeRequest(t, false, "Within 30 days."), nil)
	elapsed := time.Since(start)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if elapsed > 3*time.Second {
		t.Errorf("the answer took %v: a missed knowledge deadline must not hold it", elapsed)
	}
	r := e.sink.result(t)
	sum := r.Decision.EvidenceSummary
	if sum.Knowledge != evidence.KnowledgeTimeout || sum.CheckedAgainst() != "none" {
		t.Errorf("knowledge %q, checked against %q; want timeout and none", sum.Knowledge, sum.CheckedAgainst())
	}
	if r.Status == "" || strings.HasPrefix(r.Status, "error") {
		t.Errorf("status %q: a knowledge miss is no gate error", r.Status)
	}
	select {
	case err := <-kb.ended:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("retrieval ended with %v, want its deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retrieval was never stopped")
	}
}

func TestKnowledgeFailuresAreNoFailure(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		docs  []evidence.Item
		state string
	}{
		{name: "error", err: errors.New("engine unavailable"), state: evidence.KnowledgeError},
		{name: "add-on off", err: ErrKnowledgeDisabled, state: evidence.KnowledgeDisabled},
		{name: "deadline at the engine", err: context.DeadlineExceeded, state: evidence.KnowledgeTimeout},
		{name: "no hits", state: evidence.KnowledgeNoHits},
		{name: "only blank documents", docs: []evidence.Item{{ID: "a", Text: "  "}}, state: evidence.KnowledgeNoHits},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kb := &fakeRetriever{err: tc.err, docs: tc.docs}
			e := newEnv(t, envOpts{testMode: true, knowledge: kb,
				policyYAML: knowledgePolicy(decision.ModeFlag, "request_system, knowledge", "when: fallback, deadline_ms: 500")})
			w := e.do(t, "/v1/chat/completions", knowledgeRequest(t, false, "Within 30 days."), nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			sum := e.sink.result(t).Decision.EvidenceSummary
			if sum.Knowledge != tc.state || sum.CheckedAgainst() != "none" {
				t.Errorf("knowledge %q, checked against %q; want %q and none", sum.Knowledge, sum.CheckedAgainst(), tc.state)
			}
		})
	}
}

func TestKnowledgeOnlyEvidenceCapsAtWarn(t *testing.T) {
	sc := demoScenario(t, "refund-contradiction-flag")
	headers := map[string]string{"X-TrustAgent-Scenario": sc.ID}
	body := withMembers(t, string(sc.Request), map[string]any{"test_response": sc.Response})
	policyDoc := evidence.Item{ID: "refund-policy.md", Label: "refund-policy.md",
		Text: "Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable."}

	// The request's own document: both evidence checks are critical, so the block route blocks.
	own := newEnv(t, envOpts{testMode: true, enforcing: true, knowledge: &fakeRetriever{docs: []evidence.Item{policyDoc}},
		policyYAML: knowledgePolicy(decision.ModeBlock, "request_system, knowledge", "when: fallback")})
	w := own.do(t, "/v1/chat/completions", body, headers)
	_ = own.sink.result(t)
	if got := w.Header().Get(HeaderAction); got != decision.ActionBlock {
		t.Fatalf("with the request's document: action %q, want block", got)
	}

	// The same document from the knowledge base alone: the grounding checks are capped at warn
	// (knowledge_only), so nothing is blocked.
	kbOnly := newEnv(t, envOpts{testMode: true, enforcing: true, knowledge: &fakeRetriever{docs: []evidence.Item{policyDoc}},
		policyYAML: knowledgePolicy(decision.ModeBlock, "knowledge", "when: fallback")})
	w = kbOnly.do(t, "/v1/chat/completions", body, headers)
	r := kbOnly.sink.result(t)
	if got := w.Header().Get(HeaderAction); got == decision.ActionBlock {
		t.Fatalf("knowledge-only evidence: action block, want the grounding levels capped at warn")
	}
	if r.Decision.EvidenceSummary.CheckedAgainst() != "knowledge:1" {
		t.Errorf("checked against %q, want knowledge:1", r.Decision.EvidenceSummary.CheckedAgainst())
	}
	capped := 0
	for _, res := range r.Decision.Evaluators {
		if res.Capped != nil && *res.Capped == decision.CappedKnowledgeOnly {
			capped++
			if res.Level == nil || *res.Level != decision.LevelWarn {
				t.Errorf("%s capped at %v, want warn", res.Ref, res.Level)
			}
		}
	}
	if capped == 0 {
		t.Errorf("no evaluator was capped knowledge_only: %+v", r.Decision.Evaluators)
	}
}

func TestKnowledgeIsNotWaitedForWhenTheAnswerIsNotChecked(t *testing.T) {
	kb := &fakeRetriever{hang: true, ended: make(chan error, 1)}
	e := newEnv(t, envOpts{knowledge: kb,
		policyYAML: knowledgePolicy(decision.ModeFlag, "request_system, knowledge", "when: fallback, deadline_ms: 5000")})
	e.up.status, e.up.body = http.StatusInternalServerError, `{"error":{"message":"upstream down"}}`
	body := knowledgeRequest(t, false, "")
	body = strings.Replace(body, `,"test_response":""`, "", 1)
	start := time.Now()
	w := e.do(t, "/v1/chat/completions", body, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want the upstream's 500 passed through", w.Code)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("an answer that is not checked waited for its knowledge")
	}
	r := e.sink.result(t)
	if r.Decision.EvidenceSummary.Knowledge != evidence.KnowledgeNotRequested || r.Decision.SkipReason == nil ||
		*r.Decision.SkipReason != gate.SkipUpstreamError {
		t.Errorf("knowledge %q, skip %v; want not_requested and upstream_error", r.Decision.EvidenceSummary.Knowledge, r.Decision.SkipReason)
	}
	select {
	case err := <-kb.ended:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("retrieval ended with %v, want it cancelled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the retrieval of an unchecked answer was not cancelled")
	}
}

func TestKnowledgeJoinsStreamedAnswers(t *testing.T) {
	kb := &fakeRetriever{docs: kbDocs[:1]}
	e := newEnv(t, envOpts{testMode: true, knowledge: kb,
		policyYAML: knowledgePolicy(decision.ModeFlag, "request_system, knowledge", "when: fallback, deadline_ms: 500")})
	body := withMembers(t, knowledgeRequest(t, false, "Within 30 days."), map[string]any{"stream": true})
	w := e.do(t, "/v1/chat/completions", body, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	sum := e.sink.result(t).Decision.EvidenceSummary
	if sum.Knowledge != evidence.KnowledgeUsed || sum.CheckedAgainst() != "knowledge:1" {
		t.Errorf("knowledge %q, checked against %q; want used and knowledge:1", sum.Knowledge, sum.CheckedAgainst())
	}
}
