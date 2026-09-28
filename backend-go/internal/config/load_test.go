package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

// repoWithEnv makes a fake repository (VERSION at its root) with a .env and returns the root and a
// subdirectory to start from.
func repoWithEnv(t *testing.T, dotenv string) (root, sub string) {
	t.Helper()
	root = t.TempDir()
	sub = filepath.Join(root, "backend-go", "cmd")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("2.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dotenv != "" {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(dotenv), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, sub
}

func entry(t *testing.T, c *Config, name string) Entry {
	t.Helper()
	for _, e := range c.Entries() {
		if e.Key.Name == name {
			return e
		}
	}
	t.Fatalf("no entry %s", name)
	return Entry{}
}

func hasWarning(c *Config, key, contains string) bool {
	for _, w := range c.Diagnostics {
		if w.Key == key && strings.Contains(w.String(), contains) {
			return true
		}
	}
	return false
}

// TestDotenvPrecedence: default < .env < process env, and a dotenv assignment never overrides a
// variable that is already set (even to an empty value).
func TestDotenvPrecedence(t *testing.T) {
	root, sub := repoWithEnv(t, strings.Join([]string{
		"# comment",
		"TRUTHTABLE_PORT=9100",
		"export TRUTHTABLE_WORKER_COUNT=7",
		"TRUTHTABLE_QUEUE_SIZE=55",
		"TRUTHTABLE_LOG_LEVEL=debug",
		"TRUTHTABLE_METRICS_PORT=9002",
		"TRUTHTABLE_METRICS_PORT=9003",
	}, "\n"))
	c := LoadWith(LoadOptions{
		Environ:    []string{"TRUTHTABLE_PORT=9000", "TRUTHTABLE_QUEUE_SIZE=", "HOME=/home/x"},
		WorkDir:    sub,
		AutoDotenv: boolPtr(true),
	})
	if c.EnvFile != filepath.Join(root, ".env") {
		t.Fatalf("env file %q", c.EnvFile)
	}
	if c.ServerPort != 9000 || entry(t, c, "TRUTHTABLE_PORT").Source != SourceEnv {
		t.Errorf("process env wins: port %d", c.ServerPort)
	}
	if c.WorkerCount != 7 || entry(t, c, "TRUTHTABLE_WORKER_COUNT").Source != SourceDotenv {
		t.Errorf(".env beats the default: workers %d", c.WorkerCount)
	}
	if c.QueueSize != 1000 || entry(t, c, "TRUTHTABLE_QUEUE_SIZE").Source != SourceDefault {
		t.Errorf("a variable set to empty is not overridden by .env (and empty means default): %d", c.QueueSize)
	}
	if c.LogLevel != "debug" || c.MetricsPort != 9003 {
		t.Errorf("later .env assignment wins: log %s metrics %d", c.LogLevel, c.MetricsPort)
	}
	if c.GRPCAddress != "localhost:50051" || entry(t, c, "TRUTHTABLE_GRPC_ADDRESS").Source != SourceDefault {
		t.Errorf("untouched key keeps its default: %s", c.GRPCAddress)
	}
	if len(c.Diagnostics) != 0 {
		t.Errorf("warnings %v", c.Warnings)
	}
}

func TestEnvFileSelection(t *testing.T) {
	root, sub := repoWithEnv(t, "TRUTHTABLE_PORT=9100\n")
	other := filepath.Join(root, "other.env")
	if err := os.WriteFile(other, []byte("TRUTHTABLE_PORT=9200\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func(environ []string, auto bool) *Config {
		return LoadWith(LoadOptions{Environ: environ, WorkDir: sub, AutoDotenv: boolPtr(auto)})
	}
	if c := load([]string{"TRUTHTABLE_ENV_FILE=auto"}, true); c.ServerPort != 9100 {
		t.Errorf("auto walks up to VERSION: %d", c.ServerPort)
	}
	if _, set := os.LookupEnv(EnvFileVar); !set {
		if c := load(nil, true); c.EnvFile != filepath.Join(root, ".env") {
			t.Errorf("a nil Environ reads the process environment: env file %q", c.EnvFile)
		}
	}
	if c := load([]string{"TRUTHTABLE_ENV_FILE="}, true); c.ServerPort != 8080 || c.EnvFile != "" {
		t.Errorf("set but empty disables dotenv: %d %q", c.ServerPort, c.EnvFile)
	}
	if c := load([]string{"TRUTHTABLE_ENV_FILE=" + other}, false); c.ServerPort != 9200 || c.EnvFile != other {
		t.Errorf("an explicit file is honoured even without auto discovery: %d %q", c.ServerPort, c.EnvFile)
	}
	if c := load([]string{"TRUTHTABLE_ENV_FILE=../../other.env"}, false); c.ServerPort != 9200 {
		t.Errorf("a relative explicit file is relative to the working directory: %d", c.ServerPort)
	}
	if c := load([]string{}, false); c.ServerPort != 8080 || c.EnvFile != "" {
		t.Errorf("without auto discovery (the go test default) no .env is read: %d", c.ServerPort)
	}
	if c := LoadWith(LoadOptions{Environ: []string{}, WorkDir: sub}); c.EnvFile != "" {
		t.Errorf("inside go test, auto discovery is off by default: %q", c.EnvFile)
	}
	c := load([]string{"TRUTHTABLE_ENV_FILE=" + filepath.Join(root, "missing.env")}, true)
	if c.EnvFile != "" || !hasWarning(c, EnvFileVar, "cannot be read") {
		t.Errorf("a missing explicit file is a warning: %v", c.Warnings)
	}
	if c := load([]string{}, true); len(c.Diagnostics) != 0 {
		t.Errorf("a found auto .env gives no warning: %v", c.Warnings)
	}
	noEnv, sub2 := repoWithEnv(t, "")
	if c := LoadWith(LoadOptions{Environ: []string{}, WorkDir: sub2, AutoDotenv: boolPtr(true)}); c.EnvFile != "" || len(c.Diagnostics) != 0 {
		t.Errorf("a missing auto .env (%s) is silent: %q %v", noEnv, c.EnvFile, c.Warnings)
	}
}

func TestParseDotenv(t *testing.T) {
	data := strings.Join([]string{
		"\uFEFFPLAIN=value # trailing comment",
		"  SPACED = padded  ",
		"export EXPORTED=yes",
		`DOUBLE="a \"quoted\" \n value" # comment`,
		`SINGLE='literal ${PLAIN} \n'`,
		`REF=${PLAIN}-${FROM_ENV}-${MISSING}`,
		`HASH=abc#def`,
		`MULTI="line one`,
		`line two"`,
		`EMPTY=`,
		`not a line`,
		`BAD NAME=x`,
		`UNTERMINATED='oops`,
		`TRAILING="x" y`,
	}, "\n")
	vars, problems := parseDotenv([]byte(data), func(n string) (string, bool) {
		if n == "FROM_ENV" {
			return "env", true
		}
		return "", false
	})
	got := map[string]string{}
	for _, v := range vars {
		got[v.Name] = v.Value
	}
	want := map[string]string{
		"PLAIN": "value", "SPACED": "padded", "EXPORTED": "yes", "DOUBLE": "a \"quoted\" \n value",
		"SINGLE": `literal ${PLAIN} \n`, "REF": "value-env-", "HASH": "abc#def", "MULTI": "line one\nline two",
		"EMPTY": "", "TRAILING": "x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed %#v\nwant %#v", got, want)
	}
	lines := []int{}
	for _, p := range problems {
		lines = append(lines, p.Line)
		if strings.Contains(p.Message, "oops") || strings.Contains(p.Message, "x") && strings.Contains(p.Message, "BAD") {
			t.Errorf("a problem echoes the line's value: %q", p.Message)
		}
	}
	if !reflect.DeepEqual(lines, []int{11, 12, 13, 14}) {
		t.Errorf("problem lines %v (%+v)", lines, problems)
	}
	if _, problems := parseDotenv([]byte("A=\"never closed\nB=1\n"), nil); len(problems) != 1 || problems[0].Line != 1 {
		t.Errorf("an unterminated double quote: %+v", problems)
	}
}

// TestDotenvProblemsAreWarnings: bad dotenv lines become warnings that name the line, never the
// value.
func TestDotenvProblemsAreWarnings(t *testing.T) {
	_, sub := repoWithEnv(t, "TRUTHTABLE_API_KEYS='app=ta-secret-value\n")
	c := LoadWith(LoadOptions{Environ: []string{}, WorkDir: sub, AutoDotenv: boolPtr(true)})
	if !hasWarning(c, EnvFileVar, "line 1: unterminated single quote") {
		t.Fatalf("warnings %v", c.Warnings)
	}
	for _, w := range c.Warnings {
		if strings.Contains(w, "ta-secret-value") {
			t.Errorf("warning echoes a secret: %s", w)
		}
	}
}

func TestAliasesAndShorthands(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{"UPSTREAM_LLM_URL=http://legacy:9000", "TRUTHTABLE_ADMIN_KEY=root=ta-root-secret"}})
	if c.UpstreamURL != "http://legacy:9000" || entry(t, c, "TRUTHTABLE_UPSTREAM_URL").Var != "UPSTREAM_LLM_URL" {
		t.Errorf("alias: %s", c.UpstreamURL)
	}
	if len(c.Auth.AdminKeys) != 1 || c.Auth.AdminKeys[0] != (APIKey{Label: "root", Key: "ta-root-secret"}) {
		t.Errorf("admin key alias: %+v", c.Auth.AdminKeys)
	}
	c = LoadWith(LoadOptions{Environ: []string{"UPSTREAM_LLM_URL=http://legacy:9000", "TRUTHTABLE_UPSTREAM_URL=https://gw.example/v1base"}})
	if c.UpstreamURL != "https://gw.example/v1base" || entry(t, c, "TRUTHTABLE_UPSTREAM_URL").Var != "TRUTHTABLE_UPSTREAM_URL" {
		t.Errorf("the canonical name wins: %s", c.UpstreamURL)
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_UPSTREAM_URL=", "UPSTREAM_LLM_URL=http://legacy:9000"}})
	if c.UpstreamURL != "http://legacy:9000" {
		t.Errorf("an empty canonical value falls through to the alias: %s", c.UpstreamURL)
	}
	for _, tc := range []struct{ env []string }{
		{[]string{"TRUTHTABLE_UPSTREAM_URL=ollama"}},
		{[]string{"TRUTHTABLE_UPSTREAM_URL=Ollama", "OLLAMA_BASE_URL=http://ollama:11434"}},
	} {
		c = LoadWith(LoadOptions{Environ: tc.env})
		want := "http://localhost:11434"
		if len(tc.env) == 2 {
			want = "http://ollama:11434"
		}
		if c.UpstreamURL != want || len(c.Diagnostics) != 0 {
			t.Errorf("%v: %s %v", tc.env, c.UpstreamURL, c.Warnings)
		}
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_UPSTREAM_URL=openrouter"}})
	if c.UpstreamURL != "https://openrouter.ai/api" {
		t.Errorf("openrouter shorthand: %s", c.UpstreamURL)
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_DATA_DIR=/srv/ta"}})
	if c.Auth.SecretKeyFile != "/srv/ta/secret.key" || c.DataDir != "/srv/ta" {
		t.Errorf("the secret key file default follows TRUTHTABLE_DATA_DIR: %s", c.Auth.SecretKeyFile)
	}
}

func TestValidationFallsBackWithWarnings(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_PORT=70000",                // above max 65535
		"TRUTHTABLE_MAX_BODY_BYTES=-1",         // bytes are non-negative (min 1)
		"TRUTHTABLE_JEV_CHARS_PER_TOKEN=9",     // above max 6
		"TRUTHTABLE_READ_TIMEOUT=-5s",          // negative duration
		"TRUTHTABLE_UPSTREAM_TIMEOUT=five",     // not a duration
		"TRUTHTABLE_PUBLIC_URL=localhost:3000", // not absolute
		"TRUTHTABLE_LOG_LEVEL=DEBUG",           // enum values are case-insensitive
		"TRUTHTABLE_JEV_BACKEND=OpenRouter-Decisions",
		"TRUTHTABLE_DATABASE_URL=postgres://u:p@db:5432/x?sslmode=disable",
		"REDIS_URL=redis://redis:6379/0",
		"TRUTHTABLE_WRITE_TIMEOUT=0",
	}})
	if c.ServerPort != 8080 || c.MaxBodyBytes != 1<<20 || c.Jev.CharsPerToken != 4 || c.ReadTimeout != 30*time.Second ||
		c.UpstreamTimeout != 5*time.Minute || c.PublicURL != "http://localhost:3000" {
		t.Errorf("invalid values fall back: %+v", c)
	}
	if c.LogLevel != "debug" || c.Jev.Backend != "openrouter-decisions" || c.WriteTimeout != 0 {
		t.Errorf("normalized enums: %s %s", c.LogLevel, c.Jev.Backend)
	}
	if c.DatabaseURL == "" || c.RedisURL == "" {
		t.Error("postgres:// and redis:// URLs are URLs")
	}
	for key, text := range map[string]string{
		"TRUTHTABLE_PORT":                `"70000" is not an integer from 1 to 65535 (using the default "8080")`,
		"TRUTHTABLE_MAX_BODY_BYTES":      "a byte count",
		"TRUTHTABLE_JEV_CHARS_PER_TOKEN": "a number from 2.5 to 6",
		"TRUTHTABLE_READ_TIMEOUT":        "non-negative Go duration",
		"TRUTHTABLE_UPSTREAM_TIMEOUT":    "non-negative Go duration",
		"TRUTHTABLE_PUBLIC_URL":          "an absolute URL",
	} {
		if !hasWarning(c, key, text) {
			t.Errorf("no %s warning containing %q in %v", key, text, c.Warnings)
		}
	}
	if len(c.Diagnostics) != 6 {
		t.Errorf("%d warnings: %v", len(c.Diagnostics), c.Warnings)
	}
	if e := entry(t, c, "TRUTHTABLE_PORT"); !e.Invalid || e.Source != SourceDefault || e.Value != "8080" {
		t.Errorf("invalid entry %+v", e)
	}
	c = LoadWith(LoadOptions{Environ: []string{"UPSTREAM_LLM_URL=nope"}})
	if !hasWarning(c, "TRUTHTABLE_UPSTREAM_URL", "(set as UPSTREAM_LLM_URL)") {
		t.Errorf("a warning names the alias that was used: %v", c.Warnings)
	}
}

// TestUnknownKeysAndTypoHints: unknown TRUTHTABLE_* keys and near-miss proxy keys are warnings with
// a "did you mean" hint within Levenshtein distance 2.
func TestUnknownKeysAndTypoHints(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_PROT=9000",
		"TRUTHTABLE_API_KYES=a=b1234567",
		"TRUTHTABLE_SOMETHING_NEW=1",
		"TRUTHTABLE_EMPTY_UNKNOWN=",
		"OPENROUTER_API_KY=sk-or-123",
		"REDIS_URI=redis://x",
		"LLM_PROVIDER=mock",            // an engine key: known, no warning
		"TRUTHTABLE_ENGINE_MAX_RPCS=8", // an engine key with the prefix: known
		"PATH=/usr/bin", "HOME=/root", "OLLAMA_HOST=0.0.0.0", "http_proxy=http://p",
	}})
	want := map[string]string{
		"TRUTHTABLE_PROT":          "did you mean TRUTHTABLE_PORT?",
		"TRUTHTABLE_API_KYES":      "did you mean TRUTHTABLE_API_KEYS?",
		"TRUTHTABLE_SOMETHING_NEW": "not in the configuration registry",
		"OPENROUTER_API_KY":        "did you mean OPENROUTER_API_KEY?",
		"REDIS_URI":                "did you mean REDIS_URL?",
	}
	if len(c.Diagnostics) != len(want) {
		t.Errorf("warnings %v", c.Warnings)
	}
	for key, hint := range want {
		if !hasWarning(c, key, "unknown key ("+hint+")") {
			t.Errorf("%s: want hint %q in %v", key, hint, c.Warnings)
		}
	}
	// Dotenv keys are checked too.
	_, sub := repoWithEnv(t, "TRUTHTABLE_WORKER_CUONT=3\n")
	c = LoadWith(LoadOptions{Environ: []string{}, WorkDir: sub, AutoDotenv: boolPtr(true)})
	if !hasWarning(c, "TRUTHTABLE_WORKER_CUONT", "did you mean TRUTHTABLE_WORKER_COUNT?") {
		t.Errorf("dotenv typo: %v", c.Warnings)
	}
}

// TestStrictFailure: TRUTHTABLE_STRICT_CONFIG=true turns any warning into a failure (the proxy
// exits with StrictExitCode); without warnings it starts.
func TestStrictFailure(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_STRICT_CONFIG=true", "TRUTHTABLE_PROT=1"}})
	if !c.StrictConfig || !c.StrictFailure() || StrictExitCode != 2 {
		t.Errorf("strict with a warning: %v %v", c.StrictConfig, c.Warnings)
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_STRICT_CONFIG=true"}})
	if c.StrictFailure() {
		t.Errorf("strict without warnings: %v", c.Warnings)
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_PROT=1"}})
	if c.StrictFailure() {
		t.Error("warnings without strict mode do not fail")
	}
}

// TestInjectModeRefusesOpenIngest: TRUTHTABLE_INGEST_AUTH=open is refused while an upstream
// credential is injected (B1, SPEC §0 #1).
func TestInjectModeRefusesOpenIngest(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_INGEST_AUTH=open", "TRUTHTABLE_UPSTREAM_API_KEY=sk-upstream-1234567890"}})
	if c.IngestAuth != "keys" || !c.Injecting() || !hasWarning(c, "TRUTHTABLE_INGEST_AUTH", "open is refused") {
		t.Errorf("ingest %s injecting %v warnings %v", c.IngestAuth, c.Injecting(), c.Warnings)
	}
	for _, w := range c.Warnings {
		if strings.Contains(w, "sk-upstream") {
			t.Errorf("the upstream key leaked into a warning: %s", w)
		}
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_INGEST_AUTH=open"}})
	if c.IngestAuth != "open" || c.Injecting() || len(c.Diagnostics) != 0 {
		t.Errorf("passthrough keeps open: %s %v", c.IngestAuth, c.Warnings)
	}
}

func TestAuthKeysAndCrossChecks(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_API_KEYS=app=ta-app-key-1,shared=ta-shared-key",
		"TRUTHTABLE_READ_KEYS=viewer=ta-viewer-key",
		"TRUTHTABLE_ADMIN_KEYS=root=ta-shared-key",
		"TRUTHTABLE_SECRET_KEY=not-base64!",
		"TRUTHTABLE_SETUP_TOKEN=short",
		"TRUTHTABLE_SESSION_TTL=2h",
		"TRUTHTABLE_COOKIE_SECURE=TRUE",
	}})
	if len(c.APIKeys) != 2 || len(c.Auth.ReadKeys) != 1 || len(c.Auth.AdminKeys) != 1 {
		t.Fatalf("keys %+v %+v %+v", c.APIKeys, c.Auth.ReadKeys, c.Auth.AdminKeys)
	}
	if c.Auth.SessionTTL != 2*time.Hour || c.Auth.CookieSecure != "true" {
		t.Errorf("auth %+v", c.Auth)
	}
	if c.Auth.SecretKey != "" || c.Auth.SetupToken != "" {
		t.Error("an unusable secret key or setup token seed is dropped")
	}
	for key, text := range map[string]string{
		"TRUTHTABLE_ADMIN_KEYS":  `the key labelled "root" is also in TRUTHTABLE_API_KEYS`,
		"TRUTHTABLE_SECRET_KEY":  "not 32 bytes of standard base64",
		"TRUTHTABLE_SETUP_TOKEN": "shorter than 16 characters",
	} {
		if !hasWarning(c, key, text) {
			t.Errorf("no %s warning %q in %v", key, text, c.Warnings)
		}
	}
	for _, w := range c.Warnings {
		for _, secret := range []string{"ta-shared-key", "not-base64!", "short"} {
			if strings.Contains(w, secret) && !strings.Contains(w, "shorter") {
				t.Errorf("warning %q echoes a secret", w)
			}
		}
	}
	good := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // base64 of 32 bytes
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_SECRET_KEY=" + good, "TRUTHTABLE_SETUP_TOKEN=abcdefghijklmnopqrstuvwxyz"}})
	if c.Auth.SecretKey != good || c.Auth.SetupToken == "" || len(c.Diagnostics) != 0 {
		t.Errorf("valid values kept: %v", c.Warnings)
	}
}

func TestRepoDirAndRepoPath(t *testing.T) {
	root, sub := repoWithEnv(t, "")
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	if got := repoDir("./.trustagent/secret.key"); got != filepath.Join(root, ".trustagent", "secret.key") {
		t.Errorf("repoDir %q", got)
	}
	if got := repoDir("/abs/dir"); got != "/abs/dir" {
		t.Errorf("absolute: %q", got)
	}
	if got := repoDir(""); got != "" {
		t.Errorf("empty: %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "policies.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RepoPath("policies.yaml"); got != filepath.Join(root, "policies.yaml") {
		t.Errorf("RepoPath %q", got)
	}
	outside := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	if got := repoDir("./.trustagent"); got != "./.trustagent" {
		t.Errorf("outside a repository a relative directory stays relative: %q", got)
	}
}
