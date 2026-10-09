package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// clearProxyEnv blanks every TRUTHTABLE_* variable of the host for the test.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TRUTHTABLE_") || k == "UPSTREAM_LLM_URL" || k == "OPENROUTER_API_KEY" || k == "REDIS_URL" {
			t.Setenv(k, "")
		}
	}
}

func boot(t *testing.T, args ...string) (*config.Config, int, bool, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg, code, exit := bootConfig(args, &stdout, &stderr)
	return cfg, code, exit, stdout.String(), stderr.String()
}

func TestBootConfigModes(t *testing.T) {
	clearProxyEnv(t)
	cfg, code, exit, _, _ := boot(t)
	if exit || cfg == nil || code != 0 {
		t.Fatalf("plain boot: %v %d", exit, code)
	}

	_, code, exit, out, _ := boot(t, "-dump-config-spec")
	var spec config.Spec
	if !exit || code != 0 || json.Unmarshal([]byte(out), &spec) != nil || spec.Service != "proxy" || len(spec.Keys) < 100 {
		t.Errorf("dump-config-spec: exit %v code %d, %d keys", exit, code, len(spec.Keys))
	}

	_, code, exit, out, _ = boot(t, "-check-config")
	if !exit || code != 0 || !strings.Contains(out, "No warnings.") {
		t.Errorf("check-config, clean: %d\n%s", code, out)
	}
	t.Setenv("TRUTHTABLE_API_KEYS", "app=ta-check-secret-123")
	t.Setenv("TRUTHTABLE_PROT", "9000")
	_, code, _, out, _ = boot(t, "-check-config")
	if code != config.StrictExitCode || !strings.Contains(out, "did you mean TRUTHTABLE_PORT?") || strings.Contains(out, "ta-check-secret-123") {
		t.Errorf("check-config with a typo: %d\n%s", code, out)
	}

	for name, args := range map[string][]string{
		"force alone":   {"-force"},
		"two modes":     {"-check-config", "-dump-config-spec"},
		"unknown flag":  {"-nope"},
		"extra operand": {"serve"},
	} {
		if _, code, exit, _, _ := boot(t, args...); !exit || code != exitUsage {
			t.Errorf("%s: exit %v code %d", name, exit, code)
		}
	}
	if _, code, exit, _, errOut := boot(t, "-h"); !exit || code != 0 || !strings.Contains(errOut, "-new-setup-token") {
		t.Errorf("-h: %d %s", code, errOut)
	}
}

// TestStrictConfigExitCode: TRUTHTABLE_STRICT_CONFIG=true refuses to start (exit 2) on any
// warning and lists the warnings, without echoing secrets.
func TestStrictConfigExitCode(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("TRUTHTABLE_STRICT_CONFIG", "true")
	if cfg, _, exit, _, _ := boot(t); exit || cfg == nil {
		t.Fatal("strict mode without warnings starts")
	}
	t.Setenv("TRUTHTABLE_INGEST_AUTH", "open")
	t.Setenv("TRUTHTABLE_UPSTREAM_API_KEY", "sk-strict-upstream-secret")
	cfg, code, exit, _, errOut := boot(t)
	if !exit || cfg != nil || code != 2 || !strings.Contains(errOut, "refusing to start with 1 configuration warning") ||
		!strings.Contains(errOut, "open is refused") || strings.Contains(errOut, "sk-strict-upstream-secret") {
		t.Errorf("strict: exit %v code %d\n%s", exit, code, errOut)
	}
	t.Setenv("TRUTHTABLE_STRICT_CONFIG", "false")
	if cfg, _, exit, _, _ := boot(t); exit || cfg == nil || cfg.IngestAuth != "keys" {
		t.Error("without strict mode the proxy starts, with open refused")
	}
}

// TestCheckConfigStrictAutoWithoutKeys (DX-1): a fresh onboarding stack, backend auto and
// no key anywhere, passes -check-config under TRUTHTABLE_STRICT_CONFIG: a missing key is a
// state the dashboard fixes (Connect OpenRouter, or simulated checks), not a configuration error.
func TestCheckConfigStrictAutoWithoutKeys(t *testing.T) {
	clearProxyEnv(t)
	for _, k := range []string{"TYPESAFE_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "LLM_PROVIDER"} {
		t.Setenv(k, "")
	}
	t.Setenv("TRUTHTABLE_STRICT_CONFIG", "true")
	t.Setenv("TRUTHTABLE_JEV_BACKEND", "auto")
	// An empty .env of its own, so the repository's .env (if any) cannot add keys.
	envFile := t.TempDir() + "/.env"
	if err := os.WriteFile(envFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRUTHTABLE_ENV_FILE", envFile)
	_, code, exit, out, errOut := boot(t, "-check-config")
	if !exit || code != 0 || !strings.Contains(out, "No warnings.") {
		t.Errorf("check-config: exit %v code %d\n%s%s", exit, code, out, errOut)
	}
	if cfg, code, exit, _, errOut := boot(t); exit || cfg == nil || code != 0 {
		t.Errorf("strict boot: exit %v code %d\n%s", exit, code, errOut)
	}
}

func TestNewSetupTokenNeedsADatabase(t *testing.T) {
	clearProxyEnv(t)
	_, code, exit, out, errOut := boot(t, "-new-setup-token")
	if !exit || code != exitFailure || out != "" || !strings.Contains(errOut, "TRUTHTABLE_DATABASE_URL") {
		t.Errorf("no database: %d %q %q", code, out, errOut)
	}
}

// TestNewSetupTokenCLIPostgres: -new-setup-token prints a claimable SETUP line; once an admin
// exists it refuses without -force, and -force mints a recovery token.
func TestNewSetupTokenCLIPostgres(t *testing.T) {
	schema := storetest.New(t)
	clearProxyEnv(t)
	t.Setenv("TRUTHTABLE_DATABASE_URL", schema.URL)
	t.Setenv("TRUTHTABLE_PUBLIC_URL", "https://ta.example.com/")
	_, code, exit, out, errOut := boot(t, "-new-setup-token")
	prefix := "SETUP: open https://ta.example.com/app/setup#token="
	if !exit || code != 0 || !strings.HasPrefix(out, prefix) || !strings.Contains(errOut, "valid until") {
		t.Fatalf("mint: %d %q %q", code, out, errOut)
	}
	token := strings.TrimSpace(strings.TrimPrefix(out, prefix))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	a := authz.NewAuthenticator(authz.Options{Store: pg, SessionKey: make([]byte, 32)})
	if _, _, err := a.Setup().Claim(ctx, token); err != nil {
		t.Fatalf("claim the printed token: %v", err)
	}
	if _, code, _, out, errOut := boot(t, "-new-setup-token"); code != exitFailure || out != "" || !strings.Contains(errOut, "-force") {
		t.Errorf("admin exists: %d %q %q", code, out, errOut)
	}
	_, code, _, out, _ = boot(t, "-new-setup-token", "-force")
	if code != 0 || !strings.HasPrefix(out, prefix) {
		t.Fatalf("force: %d %q", code, out)
	}
	if _, _, err := a.Setup().Claim(ctx, strings.TrimSpace(strings.TrimPrefix(out, prefix))); err != nil {
		t.Errorf("claim the recovery token: %v", err)
	}
	// Each CLI mint is in the admin audit log (D44): the normal one and the forced recovery token,
	// with when they expire, never the token or its hash.
	mints := readAudit(t, schema, "setup_token.mint")
	if len(mints) != 2 || mints[0].Actor != "cli" || mints[0].Diff["forced"] != false || mints[1].Diff["forced"] != true {
		t.Fatalf("setup_token.mint rows %+v", mints)
	}
	for _, m := range mints {
		if _, ok := m.Diff["expires_at"]; !ok || len(m.Diff) != 2 {
			t.Errorf("mint diff %v: expires_at and forced only", m.Diff)
		}
	}
}
