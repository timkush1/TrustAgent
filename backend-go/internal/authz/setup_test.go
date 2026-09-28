package authz

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSetupTokenLifecycle: the token exists only while no admin credential exists, is stored
// hashed with a 1 h TTL, is printed as "SETUP: open <public URL>/app/setup#token=<t>", and its
// claim creates the setup-admin key once.
func TestSetupTokenLifecycle(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Keys = o.Keys[:2] }) // no env admin
	setup := f.a.Setup()
	ctx := context.Background()
	if req, err := setup.Required(ctx); err != nil || !req {
		t.Fatalf("required %v %v", req, err)
	}
	var out bytes.Buffer
	printed, exp, err := setup.Announce(ctx, &out, "")
	if err != nil || !printed || !exp.Equal(f.clock.Now().Add(time.Hour)) {
		t.Fatalf("announce %v %v %v", printed, exp, err)
	}
	line := strings.TrimSpace(out.String())
	prefix := "SETUP: open http://localhost:3000/app/setup#token="
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("setup line %q", line)
	}
	token := strings.TrimPrefix(line, prefix)
	if b, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(b) != 32 {
		t.Errorf("token is not 32 random bytes: %q", token)
	}
	state := f.st.SetupToken()
	if state == nil || len(state.Tokens) != 1 || state.Tokens[0].SHA256 != hashHex(token) || state.Tokens[0].Forced {
		t.Fatalf("stored %+v: only the hash is stored", state)
	}
	// Another start (or replica) prints a second URL; the first one keeps working.
	var second bytes.Buffer
	if printed, _, err := setup.Announce(ctx, &second, ""); err != nil || !printed || second.String() == out.String() {
		t.Fatalf("second announce %v %v", printed, err)
	}
	if n := len(f.st.SetupToken().Tokens); n != 2 {
		t.Errorf("%d pending tokens", n)
	}

	if _, _, err := setup.Claim(ctx, "wrong-token"); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Errorf("wrong token: %v", err)
	}
	id, secret, err := setup.Claim(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if id.Label != SetupAdminLabel || !id.Has(ScopeAdmin) || id.Kind != KindAPIKey || !IsMintedKey(secret) {
		t.Errorf("claim identity %+v secret %q", id, secret)
	}
	if got, err := f.a.AuthenticateContext(ctx, secret); err != nil || got.KeyID != id.KeyID {
		t.Errorf("the created admin key works: %v", err)
	}
	if _, _, err := setup.Claim(ctx, token); !errors.Is(err, ErrSetupTokenUsed) {
		t.Errorf("second claim: %v", err)
	}
	if req, _ := setup.Required(ctx); req {
		t.Error("an admin exists now")
	}
	if printed, _, err := setup.Announce(ctx, &out, ""); printed || err != nil {
		t.Error("no token is printed once an admin exists")
	}
	if _, _, err := setup.Mint(ctx, "", false, "cli"); !errors.Is(err, ErrSetupNotRequired) {
		t.Errorf("mint without force: %v", err)
	}

	// Recovery: -force mints a token that can be claimed although an admin exists.
	forced, _, err := setup.Mint(ctx, "", true, "cli")
	if st := f.st.SetupToken(); err != nil || len(st.Tokens) != 1 || !st.Tokens[0].Forced {
		t.Fatalf("forced mint: %v", err)
	}
	if _, _, err := setup.Claim(ctx, forced); err != nil {
		t.Errorf("forced claim: %v", err)
	}

	// Expiry.
	late, _, err := setup.Mint(ctx, "", true, "cli")
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	if _, _, err := setup.Claim(ctx, late); !errors.Is(err, ErrSetupTokenExpired) {
		t.Errorf("expired: %v", err)
	}

	// A plain token minted while no admin existed cannot be claimed once one exists.
	g := newFixture(t, func(o *Options) { o.Keys = o.Keys[:2] })
	plain, _, err := g.a.Setup().Mint(ctx, "a-seeded-setup-token-123", false, "startup")
	if err != nil || plain != "a-seeded-setup-token-123" {
		t.Fatalf("seeded mint %q %v", plain, err)
	}
	g.mint(t, "other-admin", ScopeAdmin)
	if _, _, err := g.a.Setup().Claim(ctx, plain); !errors.Is(err, ErrSetupComplete) {
		t.Errorf("claim after an admin appeared: %v", err)
	}
	if _, _, err := g.a.Setup().Claim(ctx, "  "); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Errorf("blank token: %v", err)
	}
	if !strings.Contains(strings.Join(f.st.AuditActions(), ","), "setup.claim") {
		t.Errorf("audit %v", f.st.AuditActions())
	}
}

func TestSetupWithEnvAdminOrWithoutStore(t *testing.T) {
	ctx := context.Background()
	env := newFixture(t, nil) // root is an env admin
	if req, _ := env.a.Setup().Required(ctx); req {
		t.Error("an env admin key completes setup")
	}
	forced, _, err := env.a.Setup().Mint(ctx, "", true, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.a.Setup().Claim(ctx, forced); err != nil {
		t.Errorf("a forced token works despite env admins: %v", err)
	}

	none := newFixture(t, func(o *Options) { o.Store = nil; o.Keys = o.Keys[:1] })
	if req, _ := none.a.Setup().Required(ctx); !req {
		t.Error("without a store and env admins, setup is required")
	}
	if _, _, err := none.a.Setup().Mint(ctx, "", false, "x"); !errors.Is(err, ErrSetupNeedsStore) {
		t.Errorf("mint without a store: %v", err)
	}
	if _, _, err := none.a.Setup().Claim(ctx, "x"); !errors.Is(err, ErrSetupNeedsStore) {
		t.Errorf("claim without a store: %v", err)
	}
	var out bytes.Buffer
	if printed, _, err := none.a.Setup().Announce(ctx, &out, ""); printed || !errors.Is(err, ErrSetupNeedsStore) {
		t.Errorf("announce without a store: %v %v", printed, err)
	}
	outage := newFixture(t, func(o *Options) { o.Keys = o.Keys[:1] })
	outage.st.SetErr(errors.New("db down"))
	if _, err := outage.a.Setup().Required(ctx); err == nil {
		t.Error("an outage is reported")
	}
}

func TestMasterKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "secret.key")
	gen, err := LoadMasterKey("", path)
	if err != nil || gen.Source != MasterGenerated || len(gen.ID) != 16 {
		t.Fatalf("generate: %+v %v", gen, err)
	}
	st, _ := os.Stat(path)
	dst, _ := os.Stat(filepath.Dir(path))
	if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
		t.Errorf("modes file %v dir %v", st.Mode().Perm(), dst.Mode().Perm())
	}
	again, err := LoadMasterKey("", path)
	if err != nil || again.Source != MasterFromFile || again.ID != gen.ID {
		t.Errorf("reload: %+v %v", again, err)
	}
	s1, s2 := gen.Derive(InfoSession), again.Derive(InfoSession)
	if !bytes.Equal(s1, s2) || bytes.Equal(s1, gen.Derive(InfoCredentials)) || len(s1) != 32 {
		t.Error("HKDF subkeys are stable per info and distinct across infos")
	}
	env := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	fromEnv, err := LoadMasterKey(env, path)
	if err != nil || fromEnv.Source != MasterFromEnv || fromEnv.ID == gen.ID {
		t.Errorf("env: %+v %v", fromEnv, err)
	}
	if _, err := LoadMasterKey("c2hvcnQ=", path); err == nil {
		t.Error("a short env key is refused")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey("", path); err == nil || !strings.Contains(err.Error(), "readable by other users") {
		t.Errorf("a world-readable key file is refused: %v", err)
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("nonsense"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey("", bad); err == nil {
		t.Error("a corrupt key file is refused")
	}
	if _, err := LoadMasterKey("", ""); err == nil {
		t.Error("no source")
	}
	if e := EphemeralMasterKey(); e.Source != MasterEphemeralOnly || bytes.Equal(e.Derive(InfoSession), s1) {
		t.Errorf("ephemeral %+v", e)
	}
}

// TestMasterKeyConcurrentGeneration: processes sharing a data volume that start together all end
// up with the one generated key, never reading a half-written file, and leave no temporary files.
func TestMasterKeyConcurrentGeneration(t *testing.T) {
	for round := 0; round < 20; round++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "data", "secret.key")
		const n = 8
		ids := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m, err := LoadMasterKey("", path)
				errs[i] = err
				if err == nil {
					ids[i] = m.ID
				}
			}()
		}
		wg.Wait()
		for i := 0; i < n; i++ {
			if errs[i] != nil || ids[i] != ids[0] {
				t.Fatalf("round %d: loader %d got %q %v, loader 0 got %q", round, i, ids[i], errs[i], ids[0])
			}
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 || entries[0].Name() != "secret.key" {
			t.Fatalf("round %d: the data directory holds %v (%v)", round, entries, err)
		}
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("round %d: key file %v %v", round, st, err)
		}
	}
}
