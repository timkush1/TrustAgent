package runtimecfg

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

const (
	fixtureSecret = "sk-or-v1-fixture-0000000000000000000000000000000000000000000000000000000000000000"
	// pushUnmatchedSecret matches none of the redaction patterns (P4-PLAN §0 rule 8).
	pushUnmatchedSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
)

func sp(s string) *string { return &s }

// fixtureSnapshot is the runtime configuration the shared push fixture pins (D34).
func fixtureSnapshot() *Snapshot {
	s := &Snapshot{Version: 7, Epoch: "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90", Settings: seedSettings(nil)}
	s.Settings.Judge = JudgeSettings{Provider: "openrouter", Model: sp("anthropic/claude-haiku-4.5")}
	s.Settings.Bindings.Judge = sp("openrouter")
	s.Credentials = CredentialSet{"openrouter": {
		Name: "openrouter", Provider: "openrouter", Source: "dashboard", Secret: fixtureSecret,
		Masked: "sk-…0000", Fingerprint: "fp_3f2a9c1d0b7e", AllowedOrigins: []string{"https://openrouter.ai"},
	}}
	return s
}

func loadPushFixture(t *testing.T) *pb.RuntimeConfig {
	t.Helper()
	b, err := contracts.FS.ReadFile("fixtures/grpc/RuntimeConfig.openrouter-judge.json")
	if err != nil {
		t.Fatal(err)
	}
	var rc pb.RuntimeConfig
	if err := protojson.Unmarshal(b, &rc); err != nil {
		t.Fatal(err)
	}
	return &rc
}

// TestBuildRuntimeConfigMatchesTheFixture (D34): the push built from the fixture's settings is
// exactly contracts/fixtures/grpc/RuntimeConfig.openrouter-judge.json, plus the price table; an
// auto judge resolves to the bound credential's provider and builds the same push.
func TestBuildRuntimeConfigMatchesTheFixture(t *testing.T) {
	want := loadPushFixture(t)
	for name, edit := range map[string]func(*Snapshot){
		"explicit provider": func(*Snapshot) {},
		"auto provider":     func(s *Snapshot) { s.Settings.Judge.Provider = "auto" },
	} {
		t.Run(name, func(t *testing.T) {
			snap := fixtureSnapshot()
			edit(snap)
			got := BuildRuntimeConfig(snap, []byte(`{"version":"t"}`))
			if string(got.GetPriceTableJson()) != `{"version":"t"}` {
				t.Fatalf("price table = %s", got.GetPriceTableJson())
			}
			got.PriceTableJson = nil
			if !proto.Equal(got, want) {
				t.Fatalf("push differs from the fixture:\n got  %v\n want %v", protojson.Format(got), protojson.Format(want))
			}
		})
	}
}

// TestBuildRuntimeConfigSendsOnlyTheJudgeCredential: the decision model's and the upstreams'
// credentials never reach the engine.
func TestBuildRuntimeConfigSendsOnlyTheJudgeCredential(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Settings.Bindings.DecisionModel = sp("typesafe")
	snap.Settings.Bindings.UpstreamDefault = sp("upstream-default")
	snap.Credentials["typesafe"] = Credential{Name: "typesafe", Provider: "typesafe", Secret: "ts-test-0001", AllowedOrigins: []string{"https://api.typesafe.ai"}}
	snap.Credentials["upstream-default"] = Credential{Name: "upstream-default", Provider: "generic", Secret: "sk-test-upstream-0001"}
	rc := BuildRuntimeConfig(snap, nil)
	if len(rc.GetCredentials()) != 1 || rc.GetCredentials()["openrouter"].GetSecret() != fixtureSecret {
		t.Fatalf("credentials pushed: %v", keys(rc.GetCredentials()))
	}
	if rc.GetDecision() != nil {
		t.Fatal("decision is set; P4 never pushes it")
	}
	if !bytes.Equal(rc.GetPriceTableJson(), pushContractsPriceTable()) || len(rc.GetPriceTableJson()) == 0 {
		t.Fatal("without an effective table the contracts table is not pushed")
	}
}

func keys(m map[string]*pb.Credential) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBuildRuntimeConfigJudgeResolution: no judge without a provider or a bound judge credential
// (the engine reverts to its env judge); mock, none and ollama carry no credential; a bound
// credential of another provider is never sent.
func TestBuildRuntimeConfigJudgeResolution(t *testing.T) {
	for _, tc := range []struct {
		name           string
		edit           func(*Snapshot)
		provider, cred string // "" provider: no judge pushed
	}{
		{"unbound auto judge", func(s *Snapshot) { s.Settings.Judge.Provider, s.Settings.Bindings.Judge = "auto", nil }, "", ""},
		{"bound credential missing", func(s *Snapshot) { s.Settings.Judge.Provider = "auto"; delete(s.Credentials, "openrouter") }, "", ""},
		{"auto with a decision-only credential", func(s *Snapshot) {
			s.Settings.Judge.Provider, s.Settings.Bindings.Judge = "auto", sp("typesafe")
			s.Credentials["typesafe"] = Credential{Name: "typesafe", Provider: "typesafe", Secret: "ts-test-0002"}
		}, "", ""},
		{"mock", func(s *Snapshot) { s.Settings.Judge.Provider = "mock" }, "mock", ""},
		{"none", func(s *Snapshot) { s.Settings.Judge.Provider = "none" }, "none", ""},
		{"ollama", func(s *Snapshot) { s.Settings.Judge.Provider = "ollama" }, "ollama", ""},
		{"explicit provider without a binding", func(s *Snapshot) { s.Settings.Bindings.Judge = nil }, "openrouter", ""},
		{"provider differs from the credential", func(s *Snapshot) { s.Settings.Judge.Provider = "anthropic" }, "anthropic", ""},
		{"anthropic credential", func(s *Snapshot) {
			s.Settings.Judge.Provider, s.Settings.Bindings.Judge = "auto", sp("anthropic")
			s.Credentials["anthropic"] = Credential{Name: "anthropic", Provider: "anthropic", Secret: "sk-ant-test-0003", AllowedOrigins: []string{"https://api.anthropic.com"}}
		}, "anthropic", "anthropic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := fixtureSnapshot()
			tc.edit(snap)
			rc := BuildRuntimeConfig(snap, nil)
			if rc.GetJudge().GetProvider() != tc.provider || rc.GetJudge().GetCredential() != tc.cred {
				t.Fatalf("judge = %v, want provider %q credential %q", rc.GetJudge(), tc.provider, tc.cred)
			}
			if rc.GetJudge().GetBaseUrl() != "" {
				t.Fatal("judge base_url is set")
			}
			if wantCreds := map[bool]int{true: 1, false: 0}[tc.cred != ""]; len(rc.GetCredentials()) != wantCreds {
				t.Fatalf("credentials pushed: %v", keys(rc.GetCredentials()))
			}
		})
	}
}

// fakeEngine scripts ApplyRuntimeConfig and records the pushes.
type fakeEngine struct {
	mu     sync.Mutex
	pushes []*pb.RuntimeConfig
	answer func(n int, rc *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error)
	pushed chan struct{}
}

func newFakeEngine(answer func(n int, rc *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error)) *fakeEngine {
	if answer == nil {
		answer = func(_ int, rc *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error) {
			return &pb.ApplyRuntimeConfigResponse{AppliedVersion: rc.GetVersion(), AppliedEpoch: rc.GetEpoch(), JudgeStatus: "configured"}, nil
		}
	}
	return &fakeEngine{answer: answer, pushed: make(chan struct{}, 1000)}
}

func (f *fakeEngine) ApplyRuntimeConfig(_ context.Context, rc *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error) {
	f.mu.Lock()
	f.pushes = append(f.pushes, proto.Clone(rc).(*pb.RuntimeConfig))
	n := len(f.pushes)
	f.mu.Unlock()
	defer func() { f.pushed <- struct{}{} }()
	return f.answer(n, rc)
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushes)
}

func (f *fakeEngine) last() *pb.RuntimeConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushes[len(f.pushes)-1]
}

// waitPushes waits until the engine has received n pushes.
func (f *fakeEngine) waitPushes(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for f.count() < n {
		select {
		case <-f.pushed:
		case <-deadline:
			t.Fatalf("the engine received %d pushes, want %d", f.count(), n)
		}
	}
}

// runPusher starts p.Run until the test ends.
func runPusher(t *testing.T, p *Pusher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitState polls the push state until ok.
func waitState(t *testing.T, p *Pusher, ok func(PushState) bool) PushState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := p.State()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("push state %+v never reached the expected state", st)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestPusherPushesAndRePushesOnMismatch: every snapshot is pushed and acknowledged; the engine
// reporting version 0 (a restart) or another epoch gets the snapshot again; a matching report
// pushes nothing.
func TestPusherPushesAndRePushesOnMismatch(t *testing.T) {
	eng := newFakeEngine(nil)
	p := NewPusher(PusherConfig{Engine: eng})
	runPusher(t, p)
	snap := fixtureSnapshot()
	p.Want(snap)
	eng.waitPushes(t, 1)
	st := waitState(t, p, func(s PushState) bool { return !s.Pending })
	if st.Version != 7 || st.Epoch != snap.Epoch || st.JudgeStatus != "configured" || st.Warning != "" {
		t.Fatalf("state after the push: %+v", st)
	}

	p.EngineReported(7, snap.Epoch)
	time.Sleep(20 * time.Millisecond)
	if eng.count() != 1 {
		t.Fatalf("a matching engine report pushed again (%d pushes)", eng.count())
	}
	p.EngineReported(0, "") // the engine restarted
	eng.waitPushes(t, 2)
	p.EngineReported(7, "0192f3a0-0000-7000-8000-000000000000") // another epoch
	eng.waitPushes(t, 3)

	next := *snap
	next.Version = 8
	next.Settings.Bindings.Judge = nil
	next.Settings.Judge.Provider = "auto"
	p.Want(&next)
	eng.waitPushes(t, 4)
	if rc := eng.last(); rc.GetVersion() != 8 || rc.GetJudge() != nil || len(rc.GetCredentials()) != 0 {
		t.Fatalf("push after unbinding the judge: %v", protojson.Format(rc))
	}
	waitState(t, p, func(s PushState) bool { return s.Version == 8 && !s.Pending })
}

// TestPusherSendsTheEffectivePriceTable (D53): the snapshot's pricing overrides go through the
// effective-table function, and its table is what the engine receives.
func TestPusherSendsTheEffectivePriceTable(t *testing.T) {
	eng := newFakeEngine(nil)
	var seen []decision.PriceEntry
	var mu sync.Mutex
	p := NewPusher(PusherConfig{Engine: eng, Prices: func(o []decision.PriceEntry) []byte {
		mu.Lock()
		seen = o
		mu.Unlock()
		b, _ := json.Marshal(map[string]any{"overrides": o})
		return b
	}})
	runPusher(t, p)
	snap := fixtureSnapshot()
	var override decision.PriceEntry
	if err := json.Unmarshal([]byte(`{"provider":"openrouter","match":"anthropic/claude-haiku-4.5","input_per_mtok":0.5,"output_per_mtok":2,"label":"override"}`), &override); err != nil {
		t.Fatal(err)
	}
	snap.Settings.PricingOverrides = []decision.PriceEntry{override}
	p.Want(snap)
	eng.waitPushes(t, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("overrides passed to the price table: %+v", seen)
	}
	if !strings.Contains(string(eng.last().GetPriceTableJson()), "claude-haiku-4.5") {
		t.Fatalf("pushed table lacks the override: %s", eng.last().GetPriceTableJson())
	}
}

// TestPusherWarnsOnceForAnOldEngine: an engine without ApplyRuntimeConfig gets one log warning
// and a status warning, and is retried at the poll interval, not on every health answer.
func TestPusherWarnsOnceForAnOldEngine(t *testing.T) {
	eng := newFakeEngine(func(int, *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error) {
		return nil, status.Error(codes.Unimplemented, "unknown method ApplyRuntimeConfig")
	})
	var logs syncBuffer
	p := NewPusher(PusherConfig{Engine: eng, Poll: time.Minute, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	var waits []time.Duration
	var mu sync.Mutex
	polls := make(chan time.Time)
	p.after = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return polls
	}
	runPusher(t, p)
	p.Want(fixtureSnapshot())
	for i := 1; i <= 3; i++ {
		eng.waitPushes(t, i)
		p.EngineReported(0, "") // health answers do not hurry the retry
		time.Sleep(10 * time.Millisecond)
		if eng.count() != i {
			t.Fatalf("an engine report retried before the poll interval (%d pushes)", eng.count())
		}
		polls <- time.Now()
	}
	eng.waitPushes(t, 4)
	if n := strings.Count(logs.String(), "does not implement ApplyRuntimeConfig"); n != 1 {
		t.Fatalf("UNIMPLEMENTED logged %d times, want once:\n%s", n, logs.String())
	}
	if st := p.State(); !strings.Contains(st.Warning, "older than this gateway") || !st.Pending {
		t.Fatalf("state = %+v, want the old-engine warning and the push pending", st)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, d := range waits {
		if d != time.Minute {
			t.Fatalf("waited %v between retries, want the poll interval", d)
		}
	}
}

// TestPusherBacksOff: failed pushes are retried with full-jitter backoff doubling from 250 ms and
// capped at 30 s; an unreachable engine raises no status warning (the engine status says it).
func TestPusherBacksOff(t *testing.T) {
	eng := newFakeEngine(func(n int, rc *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error) {
		if n <= 5 {
			return nil, status.Error(codes.Unavailable, "connection refused")
		}
		return &pb.ApplyRuntimeConfigResponse{AppliedVersion: rc.GetVersion(), AppliedEpoch: rc.GetEpoch()}, nil
	})
	p := NewPusher(PusherConfig{Engine: eng})
	p.jitter = func() float64 { return 1 }
	var waits []time.Duration
	var mu sync.Mutex
	p.after = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	runPusher(t, p)
	p.Want(fixtureSnapshot())
	eng.waitPushes(t, 6)
	if st := waitState(t, p, func(s PushState) bool { return !s.Pending }); st.Warning != "" {
		t.Fatalf("an unreachable engine left a status warning: %q", st.Warning)
	}
	mu.Lock()
	got := append([]time.Duration(nil), waits...)
	mu.Unlock()
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("waits = %v, want %v", got, want)
		}
	}
	if d := p.backoff(12); d != 30*time.Second {
		t.Fatalf("backoff after 12 failures = %v, want the 30 s cap", d)
	}
	p.jitter = func() float64 { return 0.25 }
	if d := p.backoff(2); d != 250*time.Millisecond {
		t.Fatalf("jittered backoff = %v, want a quarter of 1 s", d)
	}
}

// TestPusherNeverLeaksTheSecret: an engine refusal that echoes the pushed secret reaches the
// status warning and the log only redacted; the log never carries the push itself.
func TestPusherNeverLeaksTheSecret(t *testing.T) {
	red := config.NewRedactor()
	if red.Redact(pushUnmatchedSecret) != pushUnmatchedSecret {
		t.Fatal("the no-pattern secret is masked by the patterns alone; pick another")
	}
	red.Add(pushUnmatchedSecret)
	eng := newFakeEngine(func(n int, rc *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error) {
		if n == 1 {
			// The secret straddles the 300-byte cut: cut first, its prefix would survive masking.
			return nil, status.Error(codes.InvalidArgument, "bad credential "+strings.Repeat("x", 275)+rc.GetCredentials()["openrouter"].GetSecret())
		}
		return &pb.ApplyRuntimeConfigResponse{AppliedVersion: rc.GetVersion(), AppliedEpoch: rc.GetEpoch(), JudgeStatus: "invalid",
			Warnings: []string{"judge key rejected: " + strings.Repeat("y", 270) + rc.GetCredentials()["openrouter"].GetSecret()}}, nil
	})
	var logs syncBuffer
	p := NewPusher(PusherConfig{Engine: eng, Redact: red.Redact, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	var mu sync.Mutex
	var warnings []string
	p.after = func(time.Duration) <-chan time.Time {
		mu.Lock()
		warnings = append(warnings, p.State().Warning)
		mu.Unlock()
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	runPusher(t, p)
	snap := fixtureSnapshot()
	cred := snap.Credentials["openrouter"]
	cred.Secret = pushUnmatchedSecret
	snap.Credentials["openrouter"] = cred
	p.Want(snap)
	eng.waitPushes(t, 2)
	st := waitState(t, p, func(s PushState) bool { return s.JudgeStatus == "invalid" })
	mu.Lock()
	defer mu.Unlock()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "InvalidArgument") {
		t.Fatalf("warning after the refusal: %q", warnings)
	}
	for _, text := range append(warnings, st.Warning, logs.String()) {
		for i := 6; i <= len(pushUnmatchedSecret); i++ {
			if strings.Contains(text, pushUnmatchedSecret[:i]) {
				t.Fatalf("a %d-character prefix of the pushed secret leaked into a warning or the log", i)
			}
		}
	}
	if !strings.Contains(st.Warning, "could not use the judge settings") {
		t.Fatalf("judge_status invalid: warning %q", st.Warning)
	}
}

// syncBuffer is a bytes.Buffer safe for the logger and the test to share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
