package gate

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

// sizeJev refuses a request whose state is longer than limit bytes as too large (an HTTP 413) and
// answers the others from the hash mock. It records the state size of every request.
type sizeJev struct {
	jev.Client
	limit int
	mu    sync.Mutex
	sizes []int
}

func (s *sizeJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	b, err := json.Marshal(req.State)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sizes = append(s.sizes, len(b))
	s.mu.Unlock()
	if len(b) > s.limit {
		return nil, &jev.Error{Kind: jev.KindTooLarge, Status: 413, Attempts: 1, Message: "request entity too large"}
	}
	return s.Client.Decide(ctx, req, opt)
}

// longDocumentBody is a chat request whose system prompt carries one document of about n bytes.
func longDocumentBody(n int) []byte {
	doc := strings.Repeat("Priority tickets get a first response within 4 hours on business days. ", n/72+1)
	b, _ := json.Marshal(map[string]any{"model": "gpt-4o-mini", "messages": []map[string]string{
		{"role": "system", "content": `<document id="sla">` + doc + `</document>`},
		{"role": "user", "content": "How fast do you answer priority tickets?"},
	}})
	return b
}

func TestTooLargeResendsWithHalfTheEvidence(t *testing.T) {
	cl := &sizeJev{Client: jevHash(t), limit: 25000}
	x := exchange(t, flagPolicy(t, ""), nil, longDocumentBody(50000), "We answer priority tickets within 4 hours.", "")
	r := newGate(t, cl, seq()).Evaluate(context.Background(), x)
	if len(cl.sizes) != 2 || cl.sizes[0] <= cl.limit || cl.sizes[1] > cl.limit {
		t.Fatalf("request sizes %v, want one refused and one that fits under %d", cl.sizes, cl.limit)
	}
	d := r.Decision
	if r.Status != decision.StatusOK || d.DecisionModel.Calls != 2 || d.DecisionModel.Attempts != 2 {
		t.Errorf("status %s usage %+v", r.Status, d.DecisionModel)
	}
	// The recorded compile is the one that answered: half the evidence, still truncated.
	if d.Compile == nil || d.Compile.Requests != 1 || len(d.Compile.TruncatedIDs) != 1 || !d.EvidenceSummary.Truncated {
		t.Errorf("compile %+v evidence %+v", d.Compile, d.EvidenceSummary)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestTooLargeTwiceIsTooLarge(t *testing.T) {
	cl := &sizeJev{Client: jevHash(t), limit: 1000}
	x := exchange(t, flagPolicy(t, ""), nil, longDocumentBody(50000), "We answer priority tickets within 4 hours.", "")
	r := newGate(t, cl, seq()).Evaluate(context.Background(), x)
	if len(cl.sizes) != 2 || cl.sizes[1] >= cl.sizes[0] {
		t.Fatalf("request sizes %v, want a second, smaller request", cl.sizes)
	}
	d := r.Decision
	if r.Status != "error:too_large" || d.DecisionModel.Calls != 2 {
		t.Errorf("status %s usage %+v", r.Status, d.DecisionModel)
	}
	for _, e := range d.Evaluators {
		if e.Status != decision.EvalError || e.Reason == nil || *e.Reason != "error:too_large" {
			t.Errorf("evaluator %s: status %s reason %q", e.Ref, e.Status, deref(e.Reason))
		}
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestTooLargeWithoutEvidenceIsNotResent(t *testing.T) {
	// Halving an evidence cap changes nothing when the request carries no evidence to cut.
	cl := &sizeJev{Client: jevHash(t), limit: 10}
	x := exchange(t, flagPolicy(t, ""), nil, []byte(defaultRouteBody), "We answer priority tickets within 4 hours.", "")
	r := newGate(t, cl, seq()).Evaluate(context.Background(), x)
	if len(cl.sizes) != 1 || r.Status != "error:too_large" || r.Decision.DecisionModel.Calls != 1 {
		t.Errorf("sizes %v status %s usage %+v", cl.sizes, r.Status, r.Decision.DecisionModel)
	}
	// A request refused for another reason is not resent either.
	cl2 := &scriptedJev{Client: jevHash(t), err: &jev.Error{Kind: jev.KindBadRequest, Status: 400, Attempts: 1}}
	x = exchange(t, flagPolicy(t, ""), nil, longDocumentBody(50000), "We answer priority tickets within 4 hours.", "")
	if r := newGate(t, cl2, seq()).Evaluate(context.Background(), x); len(cl2.opts) != 1 || r.Status != "error:bad_request" {
		t.Errorf("calls %d status %s", len(cl2.opts), r.Status)
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
