package gate_test

// Tier 0 benchmarks (D120): the deterministic detectors (decision.Detect) on realistic support
// documents and answers, from the 8k-token evidence and 1k-token answers the budget statement
// covers up to a 1 MiB document (report-only), plus the gate's work around the fast-check call.
// make bench-go runs them five times into backend-go/bench/<sha>.txt; make bench-go-smoke once.
//
// Budget statement: "Tier-0 p50 < 2 ms for ≤ 32k-token evidence and ≤ 1k-token answers, on the
// stated machine" (TestTier0Budget guards it with headroom).

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
)

// charsPerToken is decision.DefaultCPT: how the corpus sizes are counted.
const charsPerToken = decision.DefaultCPT

// months for the corpus dates.
var months = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

// evidenceText is about tokens tokens of support documentation: numbered sections with the
// figures, dates, URLs and quotations the detectors compare an answer against.
func evidenceText(tokens int) string {
	want := int(float64(tokens) * charsPerToken)
	var b strings.Builder
	for i := 1; b.Len() < want; i++ {
		fmt.Fprintf(&b, "Section %d. Annual plans bought on %s %d, 2026 can be refunded within %d days of purchase; "+
			"the form is at https://help.acme.example/refunds/%d. The policy states \"refunds always go back to the original payment method\" "+
			"and lists %d exceptions for plans above %d seats. Support answers priority tickets within %d hours. ",
			i, months[i%12], 1+i%28, 10+i%50, i, 2+i%5, 20+i%80, 2+i%10)
	}
	return b.String()[:want]
}

// evidenceItems splits text into request_system documents of about 2k tokens each, as a
// system prompt holding several <document> blocks extracts.
func evidenceItems(text string) []decision.EvidenceItem {
	const per = 2000 * charsPerToken
	var out []decision.EvidenceItem
	for i := 0; len(text) > 0; i++ {
		n := min(len(text), int(per))
		out = append(out, decision.EvidenceItem{ID: fmt.Sprintf("doc-%d.md", i+1), Source: "request_system", Text: text[:n]})
		text = text[n:]
	}
	return out
}

// answerText is about tokens tokens of an answer restating the documents. A dense answer packs a
// figure with its unit, a date, a URL, a quotation and a citation into every sentence: the most
// each detector has to check.
func answerText(tokens int, dense bool) string {
	want := int(float64(tokens) * charsPerToken)
	var b strings.Builder
	for i := 1; b.Len() < want; i++ {
		if dense {
			fmt.Fprintf(&b, "Plans bought on %s %d, 2026 get %d days [%d]; see https://help.acme.example/refunds/%d where it says "+
				"\"refunds always go back to the original payment method\" for %d seats. ", months[i%12], 1+i%28, 10+i%50, 1+i%3, i, 20+i%80)
		} else {
			fmt.Fprintf(&b, "You can get a refund on an annual plan within %d days of purchase, and the money goes back to "+
				"the card you paid with. Priority tickets are answered within %d hours. ", 10+i%50, 2+i%10)
		}
	}
	return b.String()[:want]
}

// detectorInput is the detectors' input for an answer and evidence text.
func detectorInput(answer, evidenceText string) *decision.DetectorInput {
	items := evidenceItems(evidenceText)
	return &decision.DetectorInput{
		Response: answer, Evidence: items, UserRequest: "How long do I have to get a refund on an annual plan?",
		SystemInstructions: "You are the Acme support assistant. Answer only from the documents below.",
		PreDedupItems:      len(items), MinInteger: decision.DefaultMinInteger,
	}
}

// Corpus sizes: evidence tokens (1 MiB is report-only) and answer tokens.
var (
	evidenceSizes = []struct {
		name   string
		tokens int
	}{{"8k", 8000}, {"32k", 32000}, {"1MiB", int(float64(1<<20) / charsPerToken)}}
	answerSizes = []struct {
		name   string
		tokens int
	}{{"200", 200}, {"1k", 1000}, {"6k", 6000}}
)

func BenchmarkDetect(b *testing.B) {
	for _, ev := range evidenceSizes {
		text := evidenceText(ev.tokens)
		for _, ans := range answerSizes {
			in := detectorInput(answerText(ans.tokens, false), text)
			b.Run("evidence="+ev.name+"/answer="+ans.name, func(b *testing.B) {
				benchDetect(b, in)
			})
		}
	}
	// The worst case the budget covers: 32k-token evidence and a 1k-token answer in which every
	// sentence gives each detector something to check.
	in := detectorInput(answerText(1000, true), evidenceText(32000))
	b.Run("dense", func(b *testing.B) { benchDetect(b, in) })
}

func benchDetect(b *testing.B, in *decision.DetectorInput) {
	b.ReportAllocs()
	b.SetBytes(int64(len(in.Response)))
	for b.Loop() {
		if _, err := decision.Detect(in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDetectOne is each detector alone on the budget case (8k evidence, 1k dense answer).
func BenchmarkDetectOne(b *testing.B) {
	in := detectorInput(answerText(1000, true), evidenceText(8000))
	for _, id := range decision.DetectorIDs {
		b.Run(id, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := decision.DetectOne(id, in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// repoPolicy is config/policies.yaml as the gateway loads it.
func repoPolicy(tb testing.TB, reg *evaluators.Registry) *policy.Policy {
	tb.Helper()
	p, err := policy.LoadFile(filepath.Join("..", "..", "..", "config", "policies.yaml"), policy.DefaultSeeds(), reg)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

// chatBody is a support-bot request whose system prompt carries the evidence as one document.
func chatBody(evidenceText string) []byte {
	return []byte(fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"system","content":%q},{"role":"user","content":"How long do I have to get a refund on an annual plan?"}]}`,
		"You are the Acme support assistant. Answer only from the documents below.\n\n<document id=\"refund-policy.md\">"+evidenceText+"</document>"))
}

// supportExchange is a support-bot exchange (flag) with 8k-token evidence and a 1k-token answer,
// its evidence extracted as the handler does.
func supportExchange(tb testing.TB, p *policy.Policy) *gate.Exchange {
	tb.Helper()
	req, err := llmapi.ParseChatRequest(chatBody(evidenceText(8000)))
	if err != nil {
		tb.Fatal(err)
	}
	h := http.Header{}
	h.Set("X-TrustAgent-Route", "support-bot")
	route := p.Match(&policy.Request{Path: "/v1/chat/completions", Model: req.Model, Headers: h})
	settings := route.Evidence.Extraction()
	ev, err := evidence.Extract(req, &settings)
	if err != nil {
		tb.Fatal(err)
	}
	trace := &gate.TraceStart{
		TraceID: "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90", CreatedAt: time.Now(), Source: gate.SourceTrafficgen,
		APIFormat: llmapi.FormatOpenAIChat, Path: "/v1/chat/completions", Route: route.Name, PolicyVersion: p.Version,
		Mode: route.Mode, Model: req.Model, UserRequest: ev.UserRequest,
	}
	return &gate.Exchange{Trace: trace, Policy: p, Route: route, Request: req, Evidence: ev, Response: answerText(1000, false)}
}

// BenchmarkGatePreJev is the gate around the fast-check call on the budget case: the detectors,
// compile, decide and the audit plan, with the decision model off, so no call is made and only
// the gate's own work is timed.
func BenchmarkGatePreJev(b *testing.B) {
	reg, err := evaluators.Builtins()
	if err != nil {
		b.Fatal(err)
	}
	off, err := jev.New(jev.Config{Backend: jev.BackendOff})
	if err != nil {
		b.Fatal(err)
	}
	g, err := gate.New(gate.Config{Jev: off, Evaluators: reg, Budget: decision.DefaultBudget(), UniformAuditsPerDay: 50,
		Rand: func() float64 { return 0.5 }, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		b.Fatal(err)
	}
	x := supportExchange(b, repoPolicy(b, reg))
	b.ReportAllocs()
	for b.Loop() {
		if r := g.Evaluate(context.Background(), x); r.Decision == nil {
			b.Fatal("no decision")
		}
	}
}

// BenchmarkDecide is decide() alone on the budget case's compiled evaluators and scripted answers.
func BenchmarkDecide(b *testing.B) {
	reg, err := evaluators.Builtins()
	if err != nil {
		b.Fatal(err)
	}
	specs := reg.Specs()
	x := supportExchange(b, repoPolicy(b, reg))
	route := x.Route
	cr := &decision.CompileRoute{OnNoEvidence: route.OnNoEvidence, EvidenceSources: route.Evidence.Sources, Model: jev.DefaultMockModel}
	th := map[string]decision.Thresholds{}
	for _, e := range route.Evaluators {
		cr.Evaluators = append(cr.Evaluators, decision.RouteEvaluator{Ref: e.Ref, Stage: e.StageOrDefault(), Enabled: e.Enabled, Params: e.Params})
		th[e.Ref] = e.Thresholds.Decision()
	}
	compiled, err := decision.Compile(cr, specs, &decision.CompileInputs{
		UserRequest: x.Evidence.UserRequest, AssistantResponse: x.Response, SystemInstructions: x.Evidence.SystemInstructions,
		Evidence: x.Evidence.DecisionItems(),
	}, decision.DefaultBudget())
	if err != nil {
		b.Fatal(err)
	}
	evs, err := compiled.DecideEvaluators(specs, th)
	if err != nil {
		b.Fatal(err)
	}
	dets, err := decision.Detect(detectorInput(x.Response, evidenceText(8000)))
	if err != nil {
		b.Fatal(err)
	}
	answers := map[string]*decision.RawAnswer{}
	status := map[string]string{}
	for k, r := range compiled.Requests {
		status[fmt.Sprint(k)] = decision.RequestOK
		for qid := range r.Questions {
			answers[qid] = &decision.RawAnswer{Type: "noul", Noul: ptr(0.08)}
		}
	}
	in := &decision.Input{
		Policy:     decision.Policy{ConfiguredMode: route.Mode, Fail: route.Fail, RequireCalibration: route.RequireCalibration, OnNoEvidence: route.OnNoEvidence},
		Evaluators: evs, QuestionMap: compiled.QuestionMap, Excluded: compiled.Excluded, Calibration: map[string]decision.CalibrationEntry{},
		Meta:      decision.Meta{Backend: jev.BackendMock, Stream: "none", EvidencePresent: true, RequestStatus: status, QIDRequest: compiled.QIDRequest},
		Detectors: decision.DetectorStates(dets), Answers: answers,
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := decision.Decide(in); err != nil {
			b.Fatal(err)
		}
	}
}

func ptr[T any](v T) *T { return &v }
