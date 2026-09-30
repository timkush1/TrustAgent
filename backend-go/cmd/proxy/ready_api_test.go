package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/contractstest"
)

const schemaReady = "api.v1.schema.json#/$defs/ReadyStatus"

// pingFailStore is a store whose database does not answer.
type pingFailStore struct{ *memStore }

func (pingFailStore) Ping(context.Context) error { return errors.New("connection refused") }

// pingCountStore is a store that counts database pings and fails them on demand.
type pingCountStore struct {
	*memStore
	pings *atomic.Int32
	fail  *atomic.Bool
}

func (p pingCountStore) Ping(context.Context) error {
	p.pings.Add(1)
	if p.fail.Load() {
		return errors.New("connection refused")
	}
	return nil
}

func getReady(t *testing.T, env *testEnv, query string) (int, readyStatus) {
	t.Helper()
	code, body := env.get("/health/ready"+query, "") // public: no key
	if code != http.StatusOK && code != http.StatusServiceUnavailable {
		t.Fatalf("/health/ready%s: HTTP %d %s", query, code, body)
	}
	contractstest.ValidateJSON(t, schemaReady, body)
	var r readyStatus
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	return code, r
}

func TestReady(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	code, r := getReady(t, env, "")
	want := map[string]string{
		"proxy": "up", "policy": "up", "database": "up", "engine": "up", "judge": "up", "decision_model": "up",
		"knowledge": "disabled", "redis": "disabled",
	}
	if code != http.StatusOK || r.Status != readyReady || len(r.Components) != len(want) {
		t.Fatalf("ready: %d %+v", code, r)
	}
	for k, v := range want {
		if r.Components[k] != v {
			t.Errorf("component %s = %q, want %q", k, r.Components[k], v)
		}
	}
	if code, r := getReady(t, env, "?strict=1"); code != http.StatusOK || r.Status != readyReady {
		t.Errorf("strict and ready: %d %s", code, r.Status)
	}
	if code, body := env.get("/health/ready?strict=yes", ""); code != http.StatusBadRequest {
		t.Errorf("strict=yes: %d %s", code, body)
	} else {
		contractstest.ValidateJSON(t, schemaError, body)
	}

	// An engine that does not answer degrades the proxy; strict turns that into 503.
	down := newTestEnv(t, envOpts{engineDown: true})
	if code, r := getReady(t, down, ""); code != http.StatusOK || r.Status != readyDegraded || r.Components["engine"] != "down" {
		t.Errorf("engine down: %d %+v", code, r)
	}
	if code, r := getReady(t, down, "?strict=1"); code != http.StatusServiceUnavailable || r.Status != readyDegraded {
		t.Errorf("engine down, strict: %d %+v", code, r)
	}

	// A configured database that does not answer: not ready.
	noDB := newTestEnv(t, envOpts{store: pingFailStore{newMemStore()}})
	if code, r := getReady(t, noDB, ""); code != http.StatusOK || r.Status != readyNotReady || r.Components["database"] != "down" {
		t.Errorf("database down: %d %+v", code, r)
	}
	if code, _ := getReady(t, noDB, "?strict=1"); code != http.StatusServiceUnavailable {
		t.Errorf("database down, strict: %d", code)
	}

	// Without a database the proxy is still ready: persistence is off, not broken.
	none := newTestEnv(t, envOpts{noStore: true})
	if _, r := getReady(t, none, ""); r.Status != readyReady || r.Components["database"] != "disabled" {
		t.Errorf("no database: %+v", r)
	}
}

func TestReadinessMapping(t *testing.T) {
	for state, want := range map[string]string{
		"ready": "up", "mock": "up", "off": "disabled", "degraded": "degraded", "checking": "unknown",
		"breaker_open": "down", "unauthorized": "down", "no_credits": "down", "budget_exhausted": "down",
		"misconfigured": "down", "unreachable": "down",
	} {
		if got := decisionModelComponent(state); got != want {
			t.Errorf("decision model %s -> %s, want %s", state, got, want)
		}
	}
	up := func(changes map[string]string) map[string]string {
		c := map[string]string{"proxy": "up", "policy": "up", "database": "up", "engine": "up", "judge": "up",
			"decision_model": "up", "knowledge": "disabled", "redis": "disabled"}
		for k, v := range changes {
			c[k] = v
		}
		return c
	}
	cases := []struct {
		components map[string]string
		dm, want   string
	}{
		{up(nil), "ready", readyReady},
		{up(map[string]string{"decision_model": "disabled", "engine": "disabled", "judge": "disabled"}), "off", readyReady},
		{up(map[string]string{"judge": "degraded"}), "ready", readyDegraded},
		{up(map[string]string{"engine": "unknown"}), "ready", readyDegraded},
		{up(map[string]string{"decision_model": "down"}), "breaker_open", readyDegraded},
		{up(map[string]string{"decision_model": "down"}), "unauthorized", readyNotReady},
		{up(map[string]string{"decision_model": "down"}), "misconfigured", readyNotReady},
		{up(map[string]string{"database": "down"}), "ready", readyNotReady},
		{up(map[string]string{"policy": "degraded"}), "ready", readyDegraded},
	}
	for _, tc := range cases {
		if got := overallReadiness(tc.components, tc.dm); got != tc.want {
			t.Errorf("%v (%s): %s, want %s", tc.components, tc.dm, got, tc.want)
		}
	}
	for status, want := range map[string]string{"ok": "up", "diverged": "up", "missing": "degraded", "invalid": "degraded"} {
		if got := policyComponent(statusPolicy{FileStatus: status}); got != want {
			t.Errorf("policy file %s -> %s, want %s", status, got, want)
		}
	}
}

// TestReadySharesTheDatabaseProbe: the public endpoint costs the database one probe per
// readyProbeTTL however many checks arrive, concurrent ones included; an older probe is repeated.
func TestReadySharesTheDatabaseProbe(t *testing.T) {
	st := pingCountStore{memStore: newMemStore(), pings: new(atomic.Int32), fail: new(atomic.Bool)}
	env := newTestEnv(t, envOpts{store: st})
	now := time.Now()
	a := &readyAPI{s: env.srv, now: func() time.Time { return now }}
	base := st.pings.Load()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := a.readiness().Components["database"]; got != stateUp {
				t.Errorf("database %q", got)
			}
		}()
	}
	wg.Wait()
	if n := st.pings.Load() - base; n != 1 {
		t.Fatalf("20 checks pinged the database %d times, want 1", n)
	}
	st.fail.Store(true)
	if got := a.databaseState(); got != stateUp {
		t.Errorf("within the probe's lifetime: %q, want the cached up", got)
	}
	now = now.Add(readyProbeTTL)
	if got := a.databaseState(); got != stateDown {
		t.Errorf("after the probe's lifetime: %q, want down", got)
	}
	if n := st.pings.Load() - base; n != 2 {
		t.Errorf("%d pings, want 2", n)
	}
}
