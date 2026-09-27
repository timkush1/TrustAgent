package decision

import (
	"slices"
	"strings"
	"testing"
)

func TestDetectRunsEveryDetectorInOrder(t *testing.T) {
	res, err := Detect(&DetectorInput{
		Response:      "You can get a full refund within 60 days of purchase.",
		Evidence:      []EvidenceItem{refundItem},
		UserRequest:   "Can I still get a refund on my annual plan? I bought it 45 days ago.",
		PreDedupItems: 1,
		MinInteger:    DefaultMinInteger,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range res {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, DetectorIDs) {
		t.Fatalf("ids %v, want %v", ids, DetectorIDs)
	}
	states := DetectorStates(res)
	if s := states[DetNumbers]; !s.Ran || !s.Fired {
		t.Errorf("numbers %+v, want ran and fired on 60", s)
	}
	for _, id := range []string{DetDates, DetURLs, DetQuotes, DetCitation, DetSecret} {
		if s := states[id]; !s.Ran || s.Fired {
			t.Errorf("%s %+v, want ran and not fired", id, s)
		}
	}
	if s := states[DetPromptLeak]; s.Ran || s.Fired {
		t.Errorf("system_prompt_leak %+v, want not run (no system instructions)", s)
	}
	for _, r := range res {
		if r.Findings == nil {
			t.Errorf("%s: findings must be an empty array, not null", r.ID)
		}
	}
}

func TestDetectWithoutEvidenceOrRules(t *testing.T) {
	res, err := Detect(&DetectorInput{Response: "It costs $25 and ships on 2026-03-02, see https://example.com.", MinInteger: DefaultMinInteger})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		want := r.ID == DetSecret // the only detector that always runs
		if r.Ran != want {
			t.Errorf("%s: ran %v, want %v", r.ID, r.Ran, want)
		}
	}
}

func TestDetectUntrustedEvidenceIsNoReference(t *testing.T) {
	in := &DetectorInput{
		Response:   "Delivery takes 14 days.",
		Evidence:   []EvidenceItem{{ID: "tool1", Source: "tool_results", Text: "Delivery takes 14 days.", Untrusted: true}},
		MinInteger: DefaultMinInteger,
	}
	r, err := DetectOne(DetNumbers, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ran {
		t.Errorf("numbers ran on untrusted-only evidence: %+v", r)
	}
	in.Rules = map[string]string{"allowed_commitments": "Delivery within 14 days."}
	if r, _ = DetectOne(DetNumbers, in); !r.Ran || r.Fired {
		t.Errorf("numbers with rules %+v, want ran (rules are a reference) and not fired", r)
	}
}

func TestDetectOneUnknown(t *testing.T) {
	if _, err := DetectOne("nope", &DetectorInput{}); err == nil {
		t.Error("DetectOne accepted an unknown detector")
	}
}

func TestSecretFindingsAreMasked(t *testing.T) {
	key := "sk-ant-TESTONLY-not-a-real-key-0000"
	r, err := DetectOne(DetSecret, &DetectorInput{Response: "Use " + key + " now."})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Fired || len(r.Findings) != 1 {
		t.Fatalf("got %+v, want one finding", r)
	}
	f := r.Findings[0]
	if strings.Contains(f.Text, "TESTONLY") || f.Text != "sk-a…0000" {
		t.Errorf("finding text %q is not masked as expected", f.Text)
	}
	if f.Start != 4 || f.End != 4+len(key) {
		t.Errorf("finding span [%d,%d), want [4,%d)", f.Start, f.End, 4+len(key))
	}
}

func TestMaskSecret(t *testing.T) {
	for in, want := range map[string]string{
		"":                     "",
		"short":                "*****",
		"exactly12chr":         "************",
		"AKIAABCDEFGHIJKLMNOP": "AKIA…MNOP",
	} {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDatesPrefixAndOverlap(t *testing.T) {
	in := &DetectorInput{
		Response:   "It shipped on 15 March 2026 and arrives in April 2026.",
		Evidence:   []EvidenceItem{{ID: "e", Source: "request_system", Text: "Shipped 2026-03-15. Delivery expected 2026-04-02."}},
		MinInteger: DefaultMinInteger,
	}
	r, err := DetectOne(DetDates, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fired {
		t.Errorf("dates fired %+v; 15 March 2026 is known exactly and April 2026 by prefix", r.Findings)
	}
	in.Response = "It arrives on May 3, 2026."
	if r, _ = DetectOne(DetDates, in); !r.Fired || r.Findings[0].Text != "May 3, 2026" {
		t.Errorf("dates %+v, want one finding for May 3, 2026", r)
	}
}

func TestPromptLeakFinalWindow(t *testing.T) {
	// A 260-code-point prompt: windows start at 0 and 50, plus the final window [60, 260). Only
	// the tail is repeated, so only the final window can match.
	sys := strings.Repeat("a", 60) + strings.Repeat("0123456789", 20)
	r, err := DetectOne(DetPromptLeak, &DetectorInput{SystemInstructions: sys, Response: "Here: " + sys[60:]})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ran || !r.Fired || r.Findings[0].Start != -1 || r.Findings[0].End != -1 {
		t.Errorf("prompt leak %+v, want fired with an unaligned finding", r)
	}
}
