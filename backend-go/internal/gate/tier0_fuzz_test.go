package gate_test

import (
	"encoding/json"
	"path"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

// Fuzz input caps: long enough for every pattern, short enough for -fuzztime 30s to explore.
const (
	fuzzMaxResponse = 4 << 10
	fuzzMaxEvidence = 8 << 10
)

// detectorVector is the input of a contracts/vectors/detectors vector.
type detectorVector struct {
	Input struct {
		Response string `json:"response"`
		Evidence []struct {
			Text string `json:"text"`
		} `json:"evidence"`
		UserRequest        string            `json:"user_request"`
		SystemInstructions string            `json:"system_instructions"`
		Rules              map[string]string `json:"rules"`
		Meta               struct {
			PreDedupItems int `json:"pre_dedup_items"`
			MaxMarker     int `json:"max_marker"`
		} `json:"evidence_meta"`
	} `json:"input"`
}

// FuzzDetect: on any response and evidence the detectors never panic or fail, report every
// detector in DetectorIDs order, and every finding they align is a half-open UTF-16 span inside
// the response whose text is that span (secret_pattern masks its text). Seeded from every
// contracts/vectors/detectors vector.
func FuzzDetect(f *testing.F) {
	entries, err := contracts.FS.ReadDir("vectors/detectors")
	if err != nil {
		f.Fatal(err)
	}
	for _, e := range entries {
		b, err := contracts.FS.ReadFile(path.Join("vectors/detectors", e.Name()))
		if err != nil {
			f.Fatal(err)
		}
		var v detectorVector
		if err := json.Unmarshal(b, &v); err != nil {
			f.Fatalf("%s: %v", e.Name(), err)
		}
		in := v.Input
		texts := make([]string, len(in.Evidence))
		for i, d := range in.Evidence {
			texts[i] = d.Text
		}
		var rules []string
		for _, r := range in.Rules {
			rules = append(rules, r)
		}
		f.Add(in.Response, strings.Join(texts, "\x00"), in.UserRequest, in.SystemInstructions, strings.Join(rules, "\x00"),
			in.Meta.PreDedupItems, in.Meta.MaxMarker)
	}
	f.Fuzz(func(t *testing.T, response, evidence, userRequest, system, rules string, preDedup, maxMarker int) {
		response, evidence = truncate(response, fuzzMaxResponse), truncate(evidence, fuzzMaxEvidence)
		in := &decision.DetectorInput{
			Response: response, UserRequest: truncate(userRequest, fuzzMaxResponse), SystemInstructions: truncate(system, fuzzMaxEvidence),
			PreDedupItems: max(min(preDedup, 1000), 0), MaxMarker: max(min(maxMarker, 1000), 0), MinInteger: decision.DefaultMinInteger,
			Rules: map[string]string{},
		}
		for i, text := range strings.Split(evidence, "\x00") {
			if text != "" {
				in.Evidence = append(in.Evidence, decision.EvidenceItem{ID: "doc-" + string(rune('a'+i%26)), Source: "request_system", Text: text})
			}
		}
		for i, r := range strings.Split(truncate(rules, fuzzMaxEvidence), "\x00") {
			if r != "" {
				in.Rules["rule_"+string(rune('a'+i%26))] = r
			}
		}
		results, err := decision.Detect(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != len(decision.DetectorIDs) {
			t.Fatalf("%d results, want one per detector", len(results))
		}
		units := utf16.Encode([]rune(response))
		for i, r := range results {
			if r.ID != decision.DetectorIDs[i] {
				t.Fatalf("result %d is %s, want %s", i, r.ID, decision.DetectorIDs[i])
			}
			if r.Fired != (len(r.Findings) > 0) || !r.Ran && r.Fired {
				t.Errorf("%s: ran %v, fired %v with %d findings", r.ID, r.Ran, r.Fired, len(r.Findings))
			}
			for _, fd := range r.Findings {
				if fd.Start == -1 && fd.End == -1 {
					continue // unaligned
				}
				if fd.Start < 0 || fd.Start > fd.End || fd.End > len(units) {
					t.Fatalf("%s: span [%d, %d) outside the response's %d UTF-16 units", r.ID, fd.Start, fd.End, len(units))
				}
				if r.ID == decision.DetSecret {
					continue
				}
				if got := string(utf16.Decode(units[fd.Start:fd.End])); got != fd.Text {
					t.Fatalf("%s: span [%d, %d) is %q, the finding says %q", r.ID, fd.Start, fd.End, got, fd.Text)
				}
			}
		}
	})
}

// truncate cuts s to at most n bytes on a rune boundary (and replaces invalid UTF-8, which a
// decoded JSON request never holds).
func truncate(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
