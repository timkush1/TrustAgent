package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
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
		"TRUTHTABLE_JEV_BREAKER_FAILURES": "<nil>",
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
