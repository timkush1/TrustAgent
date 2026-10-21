package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/store"
)

const schemaSuggestions = "api.v1.schema.json#/$defs/EvidenceSuggestionList"

func TestSuggestEvidenceFromTraffic(t *testing.T) {
	env, _ := pgEnv(t, envOpts{adminKey: studioAdminKey})
	scenarios := loadDemoScenarios(t)
	// Two answers whose system prompt holds a <policy> block the doc-blocks rules do not know (so
	// they were checked against nothing), and one with evidence.
	sent := map[string]bool{}
	for _, id := range []string{"untagged-custom-tag", "untagged-custom-tag", "grounded-pass"} {
		traceID := env.send(scenarios[id]).Header.Get("X-TrustAgent-Trace-Id")
		env.settledTrace(traceID)
		if id == "untagged-custom-tag" {
			sent[traceID] = true
		}
	}
	code, body := env.get("/api/evidence/suggestions?window=24h&route=support-bot", studioAdminKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaSuggestions, body)
	var list struct {
		Items []evidence.Suggestion `json:"items"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Items) != 2 {
		t.Fatalf("items = %s", body)
	}
	s := list.Items[0]
	if s.Kind != evidence.SuggestTagPattern || s.Label != "<policy> blocks in the system prompt" || s.WouldGain != 2 || s.Sample != 2 ||
		s.ExampleTraceID == nil || !sent[*s.ExampleTraceID] || s.Pattern == nil {
		t.Errorf("suggestion = %+v", s)
	}
	if ext := list.Items[1]; ext.Kind != evidence.SuggestExtension || ext.Sample != 2 {
		t.Errorf("extension = %+v", ext)
	}
	// Another route has nothing to scan.
	code, body = env.get("/api/evidence/suggestions?route=faq-bot", studioAdminKey)
	if code != http.StatusOK || string(body) != `{"items":[]}` {
		t.Errorf("faq-bot: %d %s", code, body)
	}
}

func TestSuggestEvidenceHandler(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) {
		msgs, _ := json.Marshal([]evidence.ScanMessage{{Role: "system", Content: "<kb_article>" + studioDoc + "</kb_article>"}, {Role: "user", Content: "Refund?"}})
		r.db.scan = []store.ScanBody{
			{TraceID: "a", Route: "support-bot", Messages: msgs},
			{TraceID: "b", Route: "support-bot", Messages: msgs},
			{TraceID: "c", Route: "support-bot", Messages: json.RawMessage(`{"not":"a list"}`)},
		}
	})
	code, body := studioDo(t, r.url, http.MethodGet, "/api/evidence/suggestions", "", nil)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaSuggestions, body)
	var list struct {
		Items []evidence.Suggestion `json:"items"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Items) != 2 || list.Items[0].WouldGain != 2 || list.Items[0].Sample != 2 {
		t.Errorf("items = %s", body)
	}
	// The scan reads at most the 500 most recent bodies of the default 7 d window.
	if f := r.db.scanFilter; f.Limit != store.MaxScanBodies || f.Route != "" || f.Now.Sub(f.Since) != 7*24*time.Hour {
		t.Errorf("scan filter = %+v", f)
	}
	studioDo(t, r.url, http.MethodGet, "/api/evidence/suggestions?window=24h&route=support-bot", "", nil)
	if f := r.db.scanFilter; f.Route != "support-bot" || f.Now.Sub(f.Since) != 24*time.Hour {
		t.Errorf("scan filter = %+v", f)
	}
	if code, body := studioDo(t, r.url, http.MethodGet, "/api/evidence/suggestions?window=2d", "", nil); code != http.StatusBadRequest {
		t.Errorf("bad window: %d %s", code, body)
	}
	noDB := newStudioRig(t, func(r *studioRig) { r.api.db = nil })
	if code, body := studioDo(t, noDB.url, http.MethodGet, "/api/evidence/suggestions", "", nil); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("no database: %d %s", code, body)
	}
}
