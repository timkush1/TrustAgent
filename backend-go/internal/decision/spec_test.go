package decision

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

// loadTemplates parses every built-in evaluator template in contracts/evaluators.
func loadTemplates(t *testing.T) map[string]*Spec {
	t.Helper()
	entries, err := fs.ReadDir(contracts.FS, "evaluators")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Spec{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := contracts.FS.ReadFile("evaluators/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		s, err := ParseSpec(b)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		out[s.Ref()] = s
	}
	if len(out) != 11 {
		t.Fatalf("parsed %d templates, want 11", len(out))
	}
	return out
}

func TestParseSpecTemplates(t *testing.T) {
	specs := loadTemplates(t)
	g := specs["groundedness@1"]
	if g.QuestionID() != "groundedness__v1" || g.VerdictRole != RoleUnsupported || g.Evidence.MinChars != 40 ||
		g.Outcomes.WarnAt != 0.8 || *g.Outcomes.CriticalAt != 0.95 || *g.Escalation.EscalateFrom != 0.4 ||
		!g.Escalation.NumericOrDate || !g.Outcomes.BlockEligible || len(g.Outcomes.RequiresCorroboration) != 6 {
		t.Errorf("groundedness@1 parsed as %+v", g)
	}
	r := specs["refusal@1"]
	if r.Evidence.Requires != EvidenceNone || r.Evidence.MinChars != 40 || r.Outcomes.CriticalAt != nil ||
		r.Escalation.OnError != OnErrorIgnore || r.Signal.RiskWeights["declines_or_deflects"] != 1 {
		t.Errorf("refusal@1 parsed as %+v", r)
	}
	wantOptions := []string{"answers_request", "asks_clarifying_question", "declines_or_deflects", "nothing_to_answer"}
	if got := r.Options(); !reflect.DeepEqual(got, wantOptions) {
		t.Errorf("refusal options = %v, want %v", got, wantOptions)
	}
	if p := specs["pii_secret_leak@1"]; p.Evidence.MinChars != 0 || p.Evidence.Requires != EvidenceOptional {
		t.Errorf("pii_secret_leak@1 evidence = %+v", p.Evidence)
	}
	if c := specs["commitments@1"]; !c.HasStateField(FieldRules) || c.Params["allowed_commitments"].Default == "" {
		t.Errorf("commitments@1 params = %+v", c.Params)
	}
}

func TestSpecRoundTripAndHashes(t *testing.T) {
	b, err := contracts.FS.ReadFile("evaluators/groundedness.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := CanonicalizeJSON(b)
	c, _ := CanonicalizeJSON(out)
	if string(a) != string(c) {
		t.Errorf("round trip changed the spec:\n%s\n%s", a, c)
	}
	h, err := s.Hashes()
	if err != nil {
		t.Fatal(err)
	}
	q, _ := SHA256Hex(s.QuestionBody())
	if h.QuestionSHA256 != q || h.SpecSHA256 == h.QuestionSHA256 || h.CalibrationKey == h.QuestionSHA256 {
		t.Errorf("hashes = %+v", h)
	}
}

func TestSpecDefaultsWithoutEscalation(t *testing.T) {
	s, err := ParseSpec([]byte(`{"spec_version":"evaluator/v1","id":"tone","version":2,"name":"Tone","category":"custom",
		"state_fields":["assistant_response"],"question":{"type":"score","instructions":"How polite is ` + "`assistant_response`" + `?",
		"criteria":["rude","neutral","polite"]},"evidence":{"requires":"none"},"signal":{"risk_levels":[0]},
		"outcomes":{"warn_at":0.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Escalation.OnError != OnErrorEscalate || s.Escalation.EscalateFrom != nil || s.Evidence.MinChars != 40 {
		t.Errorf("defaults = %+v %+v", s.Escalation, s.Evidence)
	}
	if got := s.Options(); !reflect.DeepEqual(got, []string{"0", "1", "2"}) {
		t.Errorf("score options = %v", got)
	}
	if s.Ref() != "tone@2" || s.QuestionID() != "tone__v2" {
		t.Errorf("ref %s, question id %s", s.Ref(), s.QuestionID())
	}
}

func TestParseSpecRejects(t *testing.T) {
	base := func(mut string) []byte {
		return []byte(strings.Replace(`{"id":"ok_id","version":1,"question":{"type":"noul","instructions":"x","criteria":{"true":"a","false":"b"}},
			"evidence":{"requires":"none"},"signal":{},"outcomes":{"warn_at":0.5}}`, "@", mut, 1))
	}
	cases := map[string][]byte{
		"not json":        []byte(`{`),
		"bad id":          []byte(strings.Replace(string(base("")), `"ok_id"`, `"Bad"`, 1)),
		"version 0":       []byte(strings.Replace(string(base("")), `"version":1`, `"version":0`, 1)),
		"no warn_at":      []byte(strings.Replace(string(base("")), `{"warn_at":0.5}`, `{}`, 1)),
		"unknown type":    []byte(strings.Replace(string(base("")), `"type":"noul"`, `"type":"rank"`, 1)),
		"choice criteria": []byte(strings.Replace(string(base("")), `"type":"noul","instructions":"x","criteria":{"true":"a","false":"b"}`, `"type":"choice","instructions":"x","criteria":["a","b"]`, 1)),
		"score criteria":  []byte(strings.Replace(string(base("")), `"type":"noul"`, `"type":"score"`, 1)),
		"evidence":        []byte(strings.Replace(string(base("")), `"requires":"none"`, `"requires":"maybe"`, 1)),
		"on_error":        []byte(strings.Replace(string(base("")), `"signal":{}`, `"signal":{},"escalation":{"on_error":"panic"}`, 1)),
	}
	if _, err := ParseSpec(base("")); err != nil {
		t.Fatalf("base spec: %v", err)
	}
	for name, doc := range cases {
		if _, err := ParseSpec(doc); err == nil {
			t.Errorf("%s: ParseSpec succeeded, want an error", name)
		}
	}
	var s Spec
	if _, err := s.MarshalJSON(); err == nil {
		t.Error("MarshalJSON of an unparsed spec succeeded")
	}
}

func TestParseRef(t *testing.T) {
	if id, v, err := ParseRef("pii_secret_leak@12"); err != nil || id != "pii_secret_leak" || v != 12 {
		t.Errorf("ParseRef = %s, %d, %v", id, v, err)
	}
	for _, bad := range []string{"groundedness", "groundedness@latest", "groundedness@0", "groundedness@01", "Groundedness@1", "g@1", "x@1@2"} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) succeeded, want an error", bad)
		}
	}
}
