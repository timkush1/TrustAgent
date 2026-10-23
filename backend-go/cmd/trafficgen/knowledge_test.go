package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestKnowledgeDocs: the documents of the selected scenarios, once per id; one id with two texts
// is refused.
func TestKnowledgeDocs(t *testing.T) {
	all, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := selectScenarios(all, knowledgeSet, "")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := knowledgeDocs(append(sel, sel...))
	if err != nil || len(docs) == 0 {
		t.Fatalf("docs %v: %v", docs, err)
	}
	ids := map[string]bool{}
	for _, d := range docs {
		if ids[d.ID] {
			t.Errorf("document %s twice", d.ID)
		}
		ids[d.ID] = true
	}
	clash := sel[0]
	clash.Knowledge = []knowledgeDoc{{ID: docs[0].ID, Text: "Something else."}}
	if _, err := knowledgeDocs(append(sel, clash)); err == nil || !strings.Contains(err.Error(), "two different texts") {
		t.Errorf("a clashing id: %v", err)
	}
}

// TestSeedKnowledgeUpload: the upload is one multipart JSON file of {content, metadata} with the
// admin key, and a non-admin refusal says so without printing the key.
func TestSeedKnowledgeUpload(t *testing.T) {
	const key = "admin-secret-for-seed"
	var got []struct {
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata"`
	}
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/ready" {
			_, _ = io.WriteString(w, `{"status":"ready","components":{"engine":"up","knowledge":"up"}}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/kb/documents" {
			http.NotFound(w, r)
			return
		}
		if refuse || r.Header.Get("X-TrustAgent-Key") != key {
			http.Error(w, `{"error":"admin scope required"}`, http.StatusForbidden)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no file part: %v", err)
			return
		}
		b, _ := io.ReadAll(f)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("the file is not the upload JSON: %s", b)
		}
		_, _ = io.WriteString(w, `{"documents_ingested":1,"claims_accepted":2,"claims_quarantined":0,"conflicts_detected":0,"status":"success"}`)
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "admin.key")
	if err := os.WriteFile(file, []byte("\n"+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runTG(t, "--seed-knowledge", "--admin-key-file", file, "--base-url", srv.URL)
	if code != exitOK || !strings.Contains(out, "seeded 1 knowledge document(s) (handbook-leave): 1 ingested, 2 claims accepted") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(got) != 1 || got[0].Metadata["doc_id"] != "handbook-leave" || got[0].Metadata["source"] != knowledgeSource || got[0].Content == "" {
		t.Errorf("uploaded %+v", got)
	}

	refuse = true
	code, out = runTG(t, "--seed-knowledge", "--admin-key-file", file, "--base-url", srv.URL)
	if code != exitMismatch || !strings.Contains(out, "not an admin key") || strings.Contains(out, key) {
		t.Errorf("a refused key: exit %d:\n%s", code, out)
	}
	if _, err := (&generator{opts: options{baseURL: "http://127.0.0.1:1"}, client: http.DefaultClient}).seedKnowledge(context.Background(),
		[]knowledgeDoc{{ID: "a", Text: "b"}}, key); err == nil {
		t.Error("a gateway that is not there: no error")
	}
}

// TestWaitEngineReady: the seed waits while the gateway reports its engine link down (the
// knowledge engine still opening its gRPC server), goes on once it is up, and gives up with the
// last components it saw when the time runs out.
func TestWaitEngineReady(t *testing.T) {
	old := readyPoll
	readyPoll = 5 * time.Millisecond
	defer func() { readyPoll = old }()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"not_ready","components":{"engine":"down","knowledge":"down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ready","components":{"engine":"up","knowledge":"up"}}`)
	}))
	defer srv.Close()
	g := &generator{opts: options{baseURL: srv.URL}, client: http.DefaultClient}
	if err := g.waitEngineReady(context.Background(), time.Second); err != nil {
		t.Fatalf("an engine link that comes up: %v", err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("asked %d times, want 3", n)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"not_ready","components":{"engine":"down","knowledge":"down"}}`)
	}))
	defer down.Close()
	g = &generator{opts: options{baseURL: down.URL}, client: http.DefaultClient}
	err := g.waitEngineReady(context.Background(), 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), `engine "down"`) {
		t.Errorf("an engine link that stays down: %v", err)
	}
}
