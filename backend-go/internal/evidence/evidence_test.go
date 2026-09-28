package evidence

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision/vectors"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// TestEvidenceVectors runs contracts/vectors/evidence against the extractor. The openai_chat
// vectors must pass; the other formats are skipped until llmapi reads them (P2).
func TestEvidenceVectors(t *testing.T) {
	all, err := vectors.Load(contracts.FS, "vectors")
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, v := range all {
		if v.Kind != "evidence" {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			var in struct {
				APIFormat string          `json:"api_format"`
				Body      json.RawMessage `json:"body"`
				Settings  struct {
					Sources        []string     `json:"sources"`
					SystemMode     string       `json:"system_mode"`
					LineHeaders    bool         `json:"line_headers"`
					TagPatterns    []TagPattern `json:"tag_patterns"`
					UntrustedTools []string     `json:"untrusted_tools"`
				} `json:"settings"`
			}
			if err := json.Unmarshal(v.Input, &in); err != nil {
				t.Fatal(err)
			}
			if in.APIFormat != llmapi.FormatOpenAIChat {
				t.Skipf("api_format %s: extraction lands with the llmapi format in P2", in.APIFormat)
			}
			ran++
			req, err := llmapi.ParseChatRequest(in.Body)
			if err != nil {
				t.Fatal(err)
			}
			s := Settings(in.Settings)
			res, err := Extract(req, &s)
			if err != nil {
				t.Fatal(err)
			}
			fwd, err := llmapi.ForwardBody(in.Body)
			if err != nil {
				t.Fatal(err)
			}
			items := []any{}
			for _, it := range res.Items {
				m := map[string]any{"id": it.ID, "source": it.Source, "text": it.Text}
				if it.Untrusted {
					m["untrusted"] = true
				}
				items = append(items, m)
			}
			var forwarded any
			if err := json.Unmarshal(fwd, &forwarded); err != nil {
				t.Fatal(err)
			}
			got := map[string]any{
				"items": items, "system_instructions": res.SystemInstructions, "user_request": res.UserRequest,
				"pre_dedup_items": float64(res.PreDedupItems), "max_marker": float64(res.MaxMarker), "forwarded_body": forwarded,
			}
			var want any
			if err := json.Unmarshal(v.Expected, &want); err != nil {
				t.Fatal(err)
			}
			if diffs := vectors.Compare(want, got, v.Tol()); len(diffs) > 0 {
				t.Errorf("%s:\n  %s", v.Path, strings.Join(diffs, "\n  "))
			}
		})
	}
	if ran == 0 {
		t.Fatal("no openai_chat evidence vectors ran")
	}
}

func extract(t *testing.T, body string, s Settings) *Result {
	t.Helper()
	req, err := llmapi.ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Extract(req, &s)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestS0ScenarioEvidence(t *testing.T) {
	res := extract(t, `{"model":"gpt-4o-mini","messages":[
	  {"role":"system","content":"You are the Acme support assistant. Answer only from the documents below. If they do not cover the question, say so.\n\n<document id=\"refund-policy.md\">Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.</document>"},
	  {"role":"user","content":"Can I still get a refund on my annual plan? I bought it 45 days ago."}]}`, DefaultSettings())
	want := []Item{{
		ID: "refund-policy.md", Source: SourceRequestSystem, Label: "refund-policy.md",
		Text: "Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.",
	}}
	if !reflect.DeepEqual(res.Items, want) {
		t.Errorf("items %+v, want %+v", res.Items, want)
	}
	if res.SystemInstructions != "You are the Acme support assistant. Answer only from the documents below. If they do not cover the question, say so." {
		t.Errorf("system_instructions %q", res.SystemInstructions)
	}
	if res.UserRequest != "Can I still get a refund on my annual plan? I bought it 45 days ago." {
		t.Errorf("user_request %q", res.UserRequest)
	}
	sum := Summarize(res, DefaultSettings().Sources, false, 4)
	if got := sum.CheckedAgainst(); got != "request_system:1" {
		t.Errorf("checked against %q", got)
	}
	if !reflect.DeepEqual(sum.Empty, []string{SourceRequestContext, SourceToolResults, SourceExtension}) {
		t.Errorf("empty %v", sum.Empty)
	}
	if sum.Knowledge != KnowledgeNotRequested || sum.Tokens <= 0 || sum.Used[0].Labels[0] != "refund-policy.md" {
		t.Errorf("summary %+v", sum)
	}

	none := extract(t, `{"messages":[{"role":"system","content":"Answer billing questions briefly."},{"role":"user","content":"Refund?"}]}`, DefaultSettings())
	if len(none.Items) != 0 || none.SystemInstructions != "Answer billing questions briefly." {
		t.Errorf("no-evidence result %+v", none)
	}
	if got := Summarize(none, DefaultSettings().Sources, false, 4).CheckedAgainst(); got != "none" {
		t.Errorf("checked against %q, want none", got)
	}
}

func TestExtensionAndLabels(t *testing.T) {
	res := extract(t, `{"messages":[{"role":"user","content":"SLA?"}],
	  "trustagent":{"context":[{"id":"sla","text":"4 hours.","source":"crm"},{"text":"1 day."},{"text":"   "}]}}`, DefaultSettings())
	want := []Item{
		{ID: "sla", Source: SourceExtension, Label: "crm", Text: "4 hours."},
		{ID: "ext2", Source: SourceExtension, Label: LabelExtension, Text: "1 day."},
	}
	if !reflect.DeepEqual(res.Items, want) {
		t.Errorf("items %+v, want %+v", res.Items, want)
	}
	if res.PreDedupItems != 2 {
		t.Errorf("pre-dedup %d: an empty context text is not an item", res.PreDedupItems)
	}
}

func TestSourceOrderAndUnconfiguredSources(t *testing.T) {
	body := `{"messages":[{"role":"system","content":"<doc>System fact.</doc>"},{"role":"user","content":"Q?"}],
	  "trustagent":{"context":[{"text":"Extension fact."}]}}`
	s := DefaultSettings()
	s.Sources = []string{SourceExtension, SourceRequestSystem}
	res := extract(t, body, s)
	if len(res.Items) != 2 || res.Items[0].Source != SourceExtension || res.Items[1].Source != SourceRequestSystem {
		t.Errorf("items %+v: want extension first (route priority order)", res.Items)
	}
	s.Sources = []string{SourceExtension}
	res = extract(t, body, s)
	if len(res.Items) != 1 || res.PreDedupItems != 1 || res.SystemInstructions != "" {
		t.Errorf("result %+v: request_system items dropped, its blocks still removed", res)
	}
}

func TestSystemModeOffRemovesBlocksWithoutItems(t *testing.T) {
	s := DefaultSettings()
	s.SystemMode = SystemModeOff
	res := extract(t, `{"messages":[{"role":"system","content":"Rules.\n<document>Doc.</document>"}]}`, s)
	if len(res.Items) != 0 || res.SystemInstructions != "Rules." {
		t.Errorf("result %+v", res)
	}
}

func TestRouteTagPatternsAndCRLF(t *testing.T) {
	s := DefaultSettings()
	s.TagPatterns = []TagPattern{{Pattern: `\{\{kb:(.*?)\}\}`, Flags: "s"}, {Pattern: `FACT: [^\n]+`}}
	res := extract(t, `{"messages":[{"role":"system","content":"Intro.\r\n{{kb: Plans renew yearly. }}\r\nFACT: Support is 24/7.\r\nEnd."}]}`, s)
	texts := []string{}
	for _, it := range res.Items {
		texts = append(texts, it.Text)
	}
	if !reflect.DeepEqual(texts, []string{"Plans renew yearly.", "FACT: Support is 24/7."}) {
		t.Errorf("items %q", texts)
	}
	// "Intro.\n" + "\n" + "\nEnd.": the run of three newlines left by the blocks collapses to two.
	if res.SystemInstructions != "Intro.\n\nEnd." {
		t.Errorf("system_instructions %q", res.SystemInstructions)
	}
	s.TagPatterns = []TagPattern{{Pattern: `(`}}
	req, _ := llmapi.ParseChatRequest([]byte(`{}`))
	if _, err := Extract(req, &s); err == nil {
		t.Error("an invalid tag pattern must fail extraction")
	}
}

func TestMarkerLabelsAndDocumentAttributes(t *testing.T) {
	res := extract(t, `{"messages":[{"role":"system","content":"<document TITLE='Pricing' ID='bad id'>A.</document>\n[Source 7]\nB."}]}`, DefaultSettings())
	want := []Item{
		// The id attribute is the first label source even when it is not a valid item id.
		{ID: "sys1", Source: SourceRequestSystem, Label: "bad id", Text: "A."},
		{ID: "sys2", Source: SourceRequestSystem, Label: "Source 7", Text: "B."},
	}
	if !reflect.DeepEqual(res.Items, want) {
		t.Errorf("items %+v, want %+v", res.Items, want)
	}
	if res.MaxMarker != 7 {
		t.Errorf("max marker %d", res.MaxMarker)
	}
}

func TestSummaryIgnoresUntrustedItemsAndReportsKnowledge(t *testing.T) {
	r := &Result{Items: []Item{
		{ID: "tool1", Source: SourceToolResults, Label: "web_search", Text: "x", Untrusted: true},
	}}
	s := Summarize(r, []string{SourceToolResults, SourceKnowledge}, true, 4)
	if s.CheckedAgainst() != "none" || s.Knowledge != KnowledgeDisabled || !s.Truncated {
		t.Errorf("summary %+v", s)
	}
	if !reflect.DeepEqual(s.Empty, []string{SourceToolResults, SourceKnowledge}) {
		t.Errorf("empty %v", s.Empty)
	}
	if r.Trusted() {
		t.Error("an untrusted-only result has no trusted evidence")
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"used":[]`) {
		t.Errorf("used must be an empty array: %s", b)
	}
}
