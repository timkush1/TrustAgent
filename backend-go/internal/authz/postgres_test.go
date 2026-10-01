package authz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// newPostgresAuth opens a migrated store in a fresh schema and an Authenticator on it (no env
// admin). It skips without TEST_DATABASE_URL.
func newPostgresAuth(t *testing.T, clk *clock) (*Authenticator, *store.PostgresStore, *storetest.Schema) {
	t.Helper()
	schema := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	a := NewAuthenticator(Options{
		Keys: []Key{{Label: "app", Secret: appKey, Scopes: APIKeyScopes}}, Store: pg, SessionKey: sessionKey,
		PublicURL: "http://localhost:3000", Now: clk.Now,
	})
	return a, pg, schema
}

// TestSetupConcurrentClaimRacePostgres: of many simultaneous claims of one setup token exactly one
// succeeds (the atomic UPDATE … RETURNING); the others get "used", and exactly one setup-admin key
// and one setup.claim audit row exist.
func TestSetupConcurrentClaimRacePostgres(t *testing.T) {
	a, pg, schema := newPostgresAuth(t, &clock{t: time.Now()})
	ctx := context.Background()
	token, _, err := a.Setup().Mint(ctx, "", false, "startup")
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		used    int
		secrets []string
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, secret, err := a.Setup().Claim(ctx, token)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				secrets = append(secrets, secret)
			case errors.Is(err, ErrSetupTokenUsed):
				used++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 || used != n-1 {
		t.Fatalf("%d claims won, %d saw used", wins, used)
	}
	keys, err := pg.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Label != SetupAdminLabel || keys[0].CreatedBy != "setup" {
		t.Fatalf("keys %+v %v", keys, err)
	}
	if id, err := a.AuthenticateContext(ctx, secrets[0]); err != nil || !id.Has(ScopeAdmin) {
		t.Errorf("the winner's admin key: %v", err)
	}
	if req, _ := a.Setup().Required(ctx); req {
		t.Error("setup is complete")
	}
	var claims int
	if err := schema.Pool(t).QueryRow(ctx, `SELECT count(*) FROM admin_audit_log WHERE action = 'setup.claim'`).Scan(&claims); err != nil || claims != 1 {
		t.Errorf("%d setup.claim audit rows (%v)", claims, err)
	}
}

// TestSessionRevocationPostgres: a session opened with a dashboard key ends within
// RevalidateEvery of the key's revocation on another replica, and at once on the revoking one; an
// epoch bump ends every session.
func TestSessionRevocationPostgres(t *testing.T) {
	clk := &clock{t: time.Now()}
	a, pg, _ := newPostgresAuth(t, clk)
	ctx := context.Background()
	created, err := a.CreateKey(ctx, "dash", []Scope{ScopeRead}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.AuthenticateContext(ctx, created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := a.IssueSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{a: a, clock: clk}
	if res := resolveCookie(f, c.Value); res.Identity == nil || res.Identity.Label != "dash" {
		t.Fatalf("session: %+v", res)
	}
	if _, revoked, err := pg.RevokeAPIKey(ctx, created.Record.ID, "other-replica"); err != nil || !revoked {
		t.Fatal(err)
	}
	if res := resolveCookie(f, c.Value); res.Identity == nil {
		t.Error("cached for up to RevalidateEvery")
	}
	clk.Advance(RevalidateEvery)
	if res := resolveCookie(f, c.Value); res.Identity != nil {
		t.Error("revoked on another replica: the session ends within RevalidateEvery")
	}

	envID, _ := a.AuthenticateContext(ctx, appKey)
	c2, _, err := a.IssueSession(ctx, envID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.RevokeAllSessions(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if res := resolveCookie(f, c2.Value); res.Identity != nil {
		t.Error("the epoch bump ends the session at once")
	}
	if ep, err := pg.SessionEpoch(ctx); err != nil || ep != 1 {
		t.Errorf("epoch %d %v", ep, err)
	}
}
