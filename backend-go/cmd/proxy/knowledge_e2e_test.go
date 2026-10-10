package main

// The knowledge add-on in process (D131; the Docker run is P6). config/policies.knowledge.yaml is
// the policy of a stack with the add-on: generated from config/policies.yaml and the handbook
// route of config/policies.pending.yaml, which the stacks without the add-on cannot load.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/contracts"
)

var updateKnowledgePolicy = flag.Bool("update-knowledge-policy", false,
	"rewrite config/policies.knowledge.yaml from config/policies.yaml and config/policies.pending.yaml")

// knowledgePolicyFile is the generated policy of a knowledge stack, under the repository root.
const knowledgePolicyFile = "config/policies.knowledge.yaml"

// knowledgePolicyPath is knowledgePolicyFile's path (beside config/policies.yaml, which exists even
// before the generated file does).
func knowledgePolicyPath() string {
	return filepath.Join(filepath.Dir(config.RepoPath("config/policies.yaml")), filepath.Base(knowledgePolicyFile))
}

// knowledgeRoute is the route of config/policies.pending.yaml the knowledge policy adds.
const knowledgeRoute = "handbook"

// knowledgePolicyHeader opens the generated file (after the schema modeline).
const knowledgePolicyHeader = `#
# GENERATED: do not edit. It is config/policies.yaml with the handbook route of
# config/policies.pending.yaml appended; regenerate it after changing either:
#   go -C backend-go test ./cmd/proxy -run TestKnowledgePolicyInSync -update-knowledge-policy
#
# The policy of a stack with the knowledge add-on (TRUTHTABLE_KNOWLEDGE_ENABLED=true, D131): every
# demo route, plus handbook, whose evidence comes from the knowledge base when a request carries no
# documents (contracts/demo/scenarios.v1.json knowledge-only-capped). The gateway refuses the
# knowledge source without the add-on, which is why handbook waits in config/policies.pending.yaml
# for the stacks that run without it.
`

// knowledgePolicy generates config/policies.knowledge.yaml from the text of config/policies.yaml
// (base) and config/policies.pending.yaml (pending): base's schema modeline, the header, base from
// its first key on (routes is its last key), then the handbook route block of pending.
func knowledgePolicy(base, pending []byte) ([]byte, error) {
	lines := strings.Split(strings.TrimRight(string(base), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "# yaml-language-server:") {
		return nil, errors.New("config/policies.yaml does not start with its schema modeline")
	}
	body := 1
	for body < len(lines) && (strings.HasPrefix(lines[body], "#") || strings.TrimSpace(lines[body]) == "") {
		body++
	}
	var lastKey string
	for _, l := range lines[body:] {
		if l != "" && l[0] != ' ' && l[0] != '#' {
			lastKey, _, _ = strings.Cut(l, ":")
		}
	}
	if lastKey != "routes" {
		return nil, errors.New("config/policies.yaml must end with its routes list, so a route can be appended")
	}

	plines := strings.Split(strings.TrimRight(string(pending), "\n"), "\n")
	start := slices.Index(plines, "  - name: "+knowledgeRoute)
	if start < 0 {
		return nil, errors.New("config/policies.pending.yaml has no route " + knowledgeRoute)
	}
	end := start + 1
	for end < len(plines) && !strings.HasPrefix(plines[end], "  - name: ") && (plines[end] == "" || plines[end][0] == ' ' || plines[end][0] == '#') {
		end++
	}
	block := strings.TrimRight(strings.Join(plines[start:end], "\n"), "\n")

	var b strings.Builder
	b.WriteString(lines[0] + "\n")
	b.WriteString(knowledgePolicyHeader)
	b.WriteString(strings.Join(lines[body:], "\n") + "\n")
	b.WriteString(block + "\n")
	return []byte(b.String()), nil
}

// TestKnowledgePolicyInSync keeps config/policies.knowledge.yaml equal to what knowledgePolicy
// makes of the two source files (-update-knowledge-policy rewrites it), and checks what it means:
// config/policies.yaml's document with the handbook route of config/policies.pending.yaml added,
// valid with the knowledge add-on, and refused without it.
func TestKnowledgePolicyInSync(t *testing.T) {
	base, err := os.ReadFile(config.RepoPath("config/policies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := os.ReadFile(config.RepoPath("config/policies.pending.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := knowledgePolicy(base, pending)
	if err != nil {
		t.Fatal(err)
	}
	path := knowledgePolicyPath()
	if *updateKnowledgePolicy {
		if err := os.WriteFile(path, want, 0o644); err != nil { // #nosec G306 -- a policy file checked into the repository
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path) // #nosec G304 -- a repository file
	if err != nil {
		t.Fatalf("%s: %v (generate it with -update-knowledge-policy)", knowledgePolicyFile, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date: regenerate it with go -C backend-go test ./cmd/proxy -run TestKnowledgePolicyInSync -update-knowledge-policy", knowledgePolicyFile)
	}

	// What it means: the base document, plus the handbook route as pending defines it.
	var gen, b, p map[string]any
	for _, d := range []struct {
		src []byte
		out *map[string]any
	}{{got, &gen}, {base, &b}, {pending, &p}} {
		if err := yaml.Unmarshal(d.src, d.out); err != nil {
			t.Fatal(err)
		}
	}
	routes := func(doc map[string]any) []any { r, _ := doc["routes"].([]any); return r }
	var handbook any
	for _, r := range routes(p) {
		if r.(map[string]any)["name"] == knowledgeRoute {
			handbook = r
		}
	}
	wantDoc := map[string]any{}
	for k, v := range b {
		wantDoc[k] = v
	}
	wantDoc["routes"] = append(slices.Clone(routes(b)), handbook)
	if !reflect.DeepEqual(gen, wantDoc) {
		t.Errorf("%s is not config/policies.yaml plus the handbook route of config/policies.pending.yaml", knowledgePolicyFile)
	}

	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	env := policy.Env{Seeds: policy.DefaultSeeds(), Specs: reg, Enforcing: true, Knowledge: true}
	if res := policy.Check(got, env); len(res.Errors) > 0 {
		t.Fatalf("%s with the knowledge add-on: %v", knowledgePolicyFile, res.Err())
	}
	env.Knowledge = false
	res := policy.Check(got, env)
	if !slices.ContainsFunc(res.Errors, func(e policy.FieldError) bool { return strings.Contains(e.Message, "knowledge add-on") }) {
		t.Errorf("%s loads without the knowledge add-on: %v", knowledgePolicyFile, res.Errors)
	}
}

// knowledgeEngine is the judged engine with the knowledge add-on: its health says the add-on
// runs, and RetrieveContext answers every query with the knowledge documents of the scenarios
// whose request it matches (as a knowledge base holding them would). It records the queries.
type knowledgeEngine struct {
	*enginetest.Engine
	mu      sync.Mutex
	queries []*pb.RetrieveContextRequest
}

// knowledgeHealth is the health of an engine whose knowledge add-on runs.
func knowledgeHealth(context.Context) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Healthy: true, Version: "test", KnowledgeEnabled: true, Dependencies: map[string]bool{"judge": true}}, nil
}

func newKnowledgeEngine(t *testing.T) *knowledgeEngine {
	t.Helper()
	docs := map[string][]*pb.ContextDocument{} // by the scenario's user question
	for _, s := range loadKnowledgeScenarios(t) {
		for _, k := range s.Knowledge {
			docs[s.question] = append(docs[s.question], &pb.ContextDocument{Id: k.ID, Content: k.Text, Label: k.ID, Source: "knowledge"})
		}
	}
	ke := &knowledgeEngine{}
	ke.Engine = enginetest.Start(t, &enginetest.Engine{
		EvaluateFunc: judgedAudit(t),
		HealthFunc:   knowledgeHealth,
		RetrieveContextFunc: func(_ context.Context, req *pb.RetrieveContextRequest) (*pb.RetrieveContextResponse, error) {
			ke.mu.Lock()
			ke.queries = append(ke.queries, req)
			ke.mu.Unlock()
			return &pb.RetrieveContextResponse{IndexState: "ready", Documents: docs[req.GetQuery()]}, nil
		},
	})
	return ke
}

// knowledgeScenario is a scenario of set knowledge: its user question and knowledge documents.
type knowledgeScenario struct {
	ID       string `json:"id"`
	Sets     []string
	question string
	Request  struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	} `json:"request"`
	Knowledge []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"knowledge"`
}

// loadKnowledgeScenarios reads the main pack's knowledge scenarios in file order.
func loadKnowledgeScenarios(t *testing.T) []knowledgeScenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []knowledgeScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	var out []knowledgeScenario
	for _, s := range pack.Scenarios {
		if !slices.Contains(s.Sets, "knowledge") {
			continue
		}
		for _, m := range s.Request.Messages {
			if m.Role == "user" {
				s.question = m.Content
			}
		}
		if s.question == "" || len(s.Knowledge) == 0 {
			t.Fatalf("knowledge scenario %s needs a user question and knowledge documents", s.ID)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		t.Fatal("the main pack has no knowledge scenario")
	}
	return out
}

// TestKnowledgeScenarioEndToEnd is the knowledge stack in process (D131): the gateway with the add-on
// on (TRUTHTABLE_KNOWLEDGE_ENABLED) and the generated knowledge policy, the mock decision backend
// and scripted answers, and an engine whose knowledge base holds the scenario's documents. trafficgen
// --expect sends set knowledge: the request carries no documents, so the gate asks the knowledge
// base (when: fallback), checks against what it returns (Checked-Against knowledge:1), and caps the
// role-bearing evaluators at warn because all its evidence came from the knowledge base (SPEC §7.1),
// so the answer is flagged, never blocked.
func TestKnowledgeScenarioEndToEnd(t *testing.T) {
	ke := newKnowledgeEngine(t)
	env := newScenarioEnvWith(t, func(cfg *config.Config) {
		cfg.KnowledgeEnabled = true
		cfg.PolicyFile = knowledgePolicyPath()
	}, ke.Engine)
	if !env.srv.policies.Engine.Env().Knowledge {
		t.Fatal("the gateway's policy loader refuses the knowledge source although TRUTHTABLE_KNOWLEDGE_ENABLED is set " +
			"(cmd/proxy/policy_wiring.go passes no Knowledge in policy.Env)")
	}
	cmd := exec.Command(buildTrafficgen(t), "--once", "--expect", "--scenarios", "knowledge", "--phase", "p5",
		"--base-url", env.http.URL, "--audit-timeout", "30s")
	cmd.Env = append(os.Environ(), "TRAFFICGEN_API_KEY="+testKey)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("trafficgen --scenarios knowledge: %v\n%s", err, out)
	}
	t.Logf("trafficgen --scenarios knowledge:\n%s", out)
	scenarios := loadKnowledgeScenarios(t)
	for _, s := range scenarios {
		if !strings.Contains(out, "ok   "+s.ID+" ") {
			t.Errorf("%s did not pass:\n%s", s.ID, out)
		}
	}
	// Spelled out for the handbook scenario: checked against the one knowledge document, and the
	// critical answers capped at warn, so the answer is flagged.
	for _, m := range okLine.FindAllStringSubmatch(out, -1) {
		if m[1] != "knowledge-only-capped" {
			continue
		}
		code, body := env.get("/api/traces/"+m[2], testKey)
		var d struct {
			Action         string   `json:"action"`
			Reasons        []string `json:"reasons"`
			CheckedAgainst struct {
				Used []struct {
					Source string `json:"source"`
					Items  int    `json:"items"`
				} `json:"used"`
			} `json:"checked_against"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &d) != nil {
			t.Fatalf("GET /api/traces/%s: %d %s", m[2], code, body)
		}
		used := d.CheckedAgainst.Used
		if len(used) != 1 || used[0].Source != "knowledge" || used[0].Items != 1 {
			t.Errorf("checked against %+v, want knowledge:1", used)
		}
		if d.Action != "flag" || !slices.Contains(d.Reasons, "contradiction:warn") || !slices.Contains(d.Reasons, "groundedness:warn") {
			t.Errorf("action %s reasons %v, want a flag capped at warn", d.Action, d.Reasons)
		}
	}

	// The knowledge base was asked once per scenario, with the user's question and the route's
	// knowledge settings (top_k 5, min_score 0.3), and before any answer existed.
	ke.mu.Lock()
	defer ke.mu.Unlock()
	if len(ke.queries) != len(scenarios) {
		t.Fatalf("%d knowledge queries for %d scenarios", len(ke.queries), len(scenarios))
	}
	for _, q := range ke.queries {
		if !slices.ContainsFunc(scenarios, func(s knowledgeScenario) bool { return s.question == q.GetQuery() }) {
			t.Errorf("query %q is no scenario's question", q.GetQuery())
		}
		if q.GetTopK() != 5 || q.GetMinScore() != 0.3 {
			t.Errorf("query top_k %d min_score %v, want the handbook route's 5 and 0.3", q.GetTopK(), q.GetMinScore())
		}
	}

	// The deep audit got the knowledge documents as its evidence.
	for _, req := range ke.Requests() {
		ctxDocs := req.GetContext()
		if len(ctxDocs) == 0 || slices.ContainsFunc(ctxDocs, func(d *pb.ContextDocument) bool { return d.GetSource() != "knowledge" }) {
			t.Errorf("audit %s: evidence %v, want the knowledge documents only", req.GetTraceId(), ctxDocs)
		}
	}
}

// TestSeedKnowledgeThroughTheGateway: trafficgen --seed-knowledge uploads the knowledge documents of
// set knowledge through the gateway's upload API with the admin key of --admin-key-file, and the
// engine ingests each under its scenario id (metadata doc_id). An ingest key is refused, and
// nothing is ingested.
func TestSeedKnowledgeThroughTheGateway(t *testing.T) {
	const admin = "admin-seed-secret"
	var mu sync.Mutex
	var ingested []*pb.IngestRequest
	eng := enginetest.Start(t, &enginetest.Engine{
		EvaluateFunc: judgedAudit(t),
		HealthFunc:   knowledgeHealth,
		IngestDocumentsFunc: func(_ context.Context, req *pb.IngestRequest) (*pb.IngestResponse, error) {
			mu.Lock()
			ingested = append(ingested, req)
			mu.Unlock()
			n := int32(len(req.GetDocuments())) //nolint:gosec // a handful of documents
			return &pb.IngestResponse{DocumentsIngested: n, Status: "success", ClaimsAccepted: n}, nil
		},
	})
	env := newScenarioEnvWith(t, func(cfg *config.Config) {
		cfg.KnowledgeEnabled = true
		cfg.Auth.AdminKeys = []config.APIKey{{Label: "admin", Key: admin}}
	}, eng)
	dir := t.TempDir()
	seed := func(key string) (string, error) {
		file := filepath.Join(dir, "admin.key")
		if err := os.WriteFile(file, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(buildTrafficgen(t), "--seed-knowledge", "--admin-key-file", file, "--base-url", env.http.URL)
		b, err := cmd.CombinedOutput()
		if strings.Contains(string(b), key) {
			t.Errorf("trafficgen printed the key:\n%s", b)
		}
		return string(b), err
	}

	out, err := seed(testKey)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, "not an admin key") {
		t.Errorf("an ingest key: %v\n%s", err, out)
	}
	mu.Lock()
	if len(ingested) != 0 {
		t.Errorf("%d ingests with an ingest key", len(ingested))
	}
	mu.Unlock()

	out, err = seed(admin)
	if err != nil {
		t.Fatalf("trafficgen --seed-knowledge: %v\n%s", err, out)
	}
	scenarios := loadKnowledgeScenarios(t)
	mu.Lock()
	defer mu.Unlock()
	if len(ingested) != 1 {
		t.Fatalf("%d ingest calls, want one upload", len(ingested))
	}
	got := map[string]string{}
	for _, d := range ingested[0].GetDocuments() {
		got[d.GetMetadata()["doc_id"]] = d.GetContent()
	}
	for _, s := range scenarios {
		for _, k := range s.Knowledge {
			if got[k.ID] != k.Text {
				t.Errorf("document %s ingested as %q, want the scenario's text", k.ID, got[k.ID])
			}
			if !strings.Contains(out, k.ID) {
				t.Errorf("the summary does not name %s:\n%s", k.ID, out)
			}
		}
	}
}
