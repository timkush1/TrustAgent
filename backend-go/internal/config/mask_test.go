package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMaskSecretAndDisplayValue(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "abc": "****", "sk-or-v1-0123456789abcdef": "sk-…cdef", "fifteen-chars!!": "****",
	} {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q, want %q", in, got, want)
		}
	}
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_API_KEYS=app=ta-app-secret-0001,short=abc",
		"OPENROUTER_API_KEY=sk-or-v1-0123456789abcdef",
		"TRUTHTABLE_PORT=9000",
	}})
	if got := entry(t, c, "TRUTHTABLE_API_KEYS").DisplayValue(); got != "app=ta-…0001,short=****" {
		t.Errorf("key list display %q", got)
	}
	if got := entry(t, c, "OPENROUTER_API_KEY").DisplayValue(); got != "sk-…cdef" {
		t.Errorf("secret display %q", got)
	}
	if got := entry(t, c, "TRUTHTABLE_PORT").DisplayValue(); got != "9000" {
		t.Errorf("non-secret display %q", got)
	}
	secrets := c.Secrets()
	for _, want := range []string{"ta-app-secret-0001", "abc", "sk-or-v1-0123456789abcdef"} {
		found := false
		for _, s := range secrets {
			found = found || s == want
		}
		if !found {
			t.Errorf("Secrets() %v lacks %q", secrets, want)
		}
	}
}

// TestRedactorMasksSecretsInLogs: no loaded secret and no credential pattern survives a log line,
// in the message, string and error attributes, WithAttrs attributes or groups.
func TestRedactorMasksSecretsInLogs(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_ADMIN_KEYS=root=ta-root-secret-777",
		"TRUTHTABLE_UPSTREAM_API_KEY=upstream-credential-xyz",
		"TRUTHTABLE_DATABASE_URL=postgres://ta:dbpass-secret@db/ta",
	}})
	r := c.Redactor()
	minted := "ta_" + strings.Repeat("A", 43)
	lines := []func(*slog.Logger){
		func(l *slog.Logger) { l.Info("key ta-root-secret-777 used") },
		func(l *slog.Logger) { l.Warn("upstream", "value", "upstream-credential-xyz") },
		func(l *slog.Logger) {
			l.Error("db", "err", errors.New("dial postgres://ta:dbpass-secret@db/ta failed"))
		},
		func(l *slog.Logger) { l.Info("header", "h", "Authorization: Bearer abcdef.ghijk") },
		func(l *slog.Logger) { l.Info("openai", "k", "sk-proj-ABCDEFGH12345") },
		func(l *slog.Logger) { l.Info("x", "h", "x-api-key: anthropic-secret-1") },
		func(l *slog.Logger) { l.With("key", "ta-root-secret-777").Info("with attrs") },
		func(l *slog.Logger) { l.Info("group", slog.Group("g", "inner", "upstream-credential-xyz")) },
		func(l *slog.Logger) { l.Info("minted " + minted) },
	}
	secrets := []string{"ta-root-secret-777", "upstream-credential-xyz", "dbpass-secret", "abcdef.ghijk", "sk-proj-ABCDEFGH12345", "anthropic-secret-1", minted}
	for _, mode := range []string{"replace-attr", "handler"} {
		var buf bytes.Buffer
		var h slog.Handler
		if mode == "replace-attr" {
			h = slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: r.ReplaceAttr})
		} else {
			h = r.Handler(slog.NewJSONHandler(&buf, nil))
		}
		l := slog.New(h)
		for _, line := range lines {
			line(l)
		}
		out := buf.String()
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s: %q leaked:\n%s", mode, s, out)
			}
		}
		if !strings.Contains(out, Masked) {
			t.Errorf("%s: nothing masked:\n%s", mode, out)
		}
		if mode == "handler" {
			for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
				if !json.Valid([]byte(ln)) {
					t.Errorf("handler output is not JSON: %s", ln)
				}
			}
		}
	}
	// Short values are not masked verbatim (they would shred ordinary words).
	short := NewRedactor("abc")
	if got := short.Redact("abc def"); got != "abc def" {
		t.Errorf("short secret masked: %q", got)
	}
	short.Add("longer-secret-1")
	if got := short.Redact("x longer-secret-1 y"); got != "x "+Masked+" y" {
		t.Errorf("Add: %q", got)
	}
	var nilRedactor *Redactor
	if nilRedactor.Redact("sk-abcdefghij") != "sk-abcdefghij" {
		t.Error("a nil Redactor is a no-op")
	}
}

func TestWriteReportMasksSecrets(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_API_KEYS=app=ta-app-secret-0001", "TRUTHTABLE_PORT=9000", "UPSTREAM_LLM_URL=http://u:1", "TRUTHTABLE_PROT=1",
	}})
	var buf bytes.Buffer
	if err := WriteReport(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"dotenv: none", "TRUTHTABLE_PORT", "9000", "app=ta-…0001", "TRUTHTABLE_UPSTREAM_URL (as UPSTREAM_LLM_URL)", "1 warning(s)", "did you mean TRUTHTABLE_PORT?"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ta-app-secret-0001") {
		t.Errorf("report leaks a key:\n%s", out)
	}
	buf.Reset()
	if err := WriteReport(&buf, LoadWith(LoadOptions{Environ: []string{}})); err != nil || !strings.Contains(buf.String(), "every key is at its default") || !strings.Contains(buf.String(), "No warnings.") {
		t.Errorf("empty report:\n%s", buf.String())
	}
}

func TestDumpSpec(t *testing.T) {
	reg := MustRegistry()
	b, err := DumpSpecJSON(reg)
	if err != nil {
		t.Fatal(err)
	}
	var spec Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Service != "proxy" || len(spec.Keys) != len(reg.ForService(ServiceProxy)) {
		t.Fatalf("service %s, %d keys", spec.Service, len(spec.Keys))
	}
	byName := map[string]SpecKey{}
	for i, k := range spec.Keys {
		if i > 0 && spec.Keys[i-1].Name >= k.Name {
			t.Errorf("not sorted at %s", k.Name)
		}
		byName[k.Name] = k
	}
	field := func(name string) string {
		if f := byName[name].Field; f != nil {
			return *f
		}
		return "<nil>"
	}
	for name, want := range map[string]string{
		"TRUTHTABLE_PORT": "ServerPort", "TRUTHTABLE_JEV_ATTEMPT_TIMEOUT": "Jev.AttemptTimeout",
		"TRUTHTABLE_ADMIN_KEYS": "Auth.AdminKeys", "TRUTHTABLE_WS_HEARTBEAT": "WS.Heartbeat",
		"TRUTHTABLE_JEV_BREAKER_FAILURES": "Jev.BreakerFailures", "TRUTHTABLE_STREAM_IDLE_TIMEOUT": "StreamIdleTimeout",
		"TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT": "FallbackUpstreamFormat", "TRUTHTABLE_ENV_FILE": "<nil>",
	} {
		if got := field(name); got != want {
			t.Errorf("%s field %s, want %s", name, got, want)
		}
	}
	admin := byName["TRUTHTABLE_ADMIN_KEYS"]
	if !admin.Secret || len(admin.Aliases) != 1 || admin.Aliases[0] != "TRUTHTABLE_ADMIN_KEY" || admin.Default == nil || *admin.Default != "" {
		t.Errorf("admin keys spec %+v", admin)
	}
	if d := byName["TRUTHTABLE_SECRET_KEY_FILE"].Default; d == nil || *d != "${TRUTHTABLE_DATA_DIR}/secret.key" {
		t.Errorf("defaults are dumped as the registry writes them: %v", d)
	}
	if !bytes.HasSuffix(b, []byte("}\n")) || !strings.Contains(string(b), `"field": null`) {
		t.Error("indented JSON ending in a newline, with null fields for unbound keys")
	}
}

// TestRedactingWriterMasksGinRecoveryDumps: gin's panic recovery dumps the request line and
// headers (masking only Authorization) in debug mode (TRUTHTABLE_LOG_LEVEL=debug) and on a broken
// pipe; through Redactor.Writer no TrustAgent credential survives.
func TestRedactingWriterMasksGinRecoveryDumps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(gin.TestMode) })
	c := LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_API_KEYS=app=ta-app-secret-0001"}})
	var buf bytes.Buffer
	r := gin.New()
	r.Use(gin.RecoveryWithWriter(c.Redactor().Writer(&buf)))
	r.GET("/ws", func(*gin.Context) { panic("boom") })

	minted := "ta_" + strings.Repeat("Q", 43)
	unknownKey := "not-a-configured-key-9f8e7d"
	session := "eyJzaWQiOiJhYmMifQ.c2lnbmF0dXJlLXZhbHVl"
	ticket := "tkt-0123456789abcdefghij"
	req := httptest.NewRequest(http.MethodGet, "/ws?protocol=2&ticket="+ticket, nil)
	req.Header.Set("X-TrustAgent-Key", unknownKey)
	req.Header.Set("X-API-Key", "ta-app-secret-0001")
	req.Header.Set("X-Other", minted)
	req.Header.Set("Cookie", "theme=dark; ta_session="+session+"; lang=en")
	gin.SetMode(gin.DebugMode) // the header dump
	r.ServeHTTP(httptest.NewRecorder(), req)
	gin.SetMode(gin.TestMode)

	out := buf.String()
	if !strings.Contains(out, "panic recovered") || !strings.Contains(out, "theme=dark") || !strings.Contains(out, "protocol=2") {
		t.Fatalf("the dump is still useful:\n%s", out)
	}
	for name, secret := range map[string]string{
		"X-TrustAgent-Key": unknownKey, "env key": "ta-app-secret-0001", "minted key": minted,
		"session cookie": session, "ticket": ticket,
	} {
		if strings.Contains(out, secret) {
			t.Errorf("the %s survives the recovery dump:\n%s", name, out)
		}
	}
	if w := NewRedactor().Writer(&buf); w == nil {
		t.Fatal("writer")
	} else if n, err := w.Write([]byte("Bearer abc")); err != nil || n != len("Bearer abc") {
		t.Errorf("Write reports the input length: %d %v", n, err)
	}
}

// TestUpstreamCredentialsAreMasked: every upstream's injected credential is a loaded secret: the
// Redactor masks it in logs and the settings view shows it masked.
func TestUpstreamCredentialsAreMasked(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{
		"TRUTHTABLE_UPSTREAM_API_KEY=openai-upstream-credential-1",
		"TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY=anthropic-upstream-credential-2",
		"TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY=fallback-upstream-credential-3",
		"TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://fallback.example",
	}})
	secrets := []string{"openai-upstream-credential-1", "anthropic-upstream-credential-2", "fallback-upstream-credential-3"}
	for _, s := range secrets {
		if !slices.Contains(c.Secrets(), s) {
			t.Errorf("%s is not a loaded secret", s)
		}
	}
	line := c.Redactor().Redact("keys " + strings.Join(secrets, " "))
	for _, s := range secrets {
		if strings.Contains(line, s) {
			t.Errorf("%s survives redaction: %s", s, line)
		}
	}
	for _, name := range []string{"TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY", "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY"} {
		e := entry(t, c, name)
		if !e.Key.Secret || !strings.Contains(e.DisplayValue(), "…") || strings.Contains(e.DisplayValue(), "credential") {
			t.Errorf("%s shown as %q", name, e.DisplayValue())
		}
	}
}
