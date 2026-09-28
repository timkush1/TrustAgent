package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRegistryParsesTheRealRegistry loads contracts/config/keys.v1.json as embedded in the build.
func TestRegistryParsesTheRealRegistry(t *testing.T) {
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.Version != "1" || len(reg.Groups) == 0 {
		t.Fatalf("version %q, %d groups", reg.Version, len(reg.Groups))
	}
	proxy := reg.ForService(ServiceProxy)
	if len(proxy) < 100 {
		t.Errorf("only %d proxy keys", len(proxy))
	}
	for alias, canonical := range map[string]string{
		"TRUTHTABLE_ADMIN_KEY": "TRUTHTABLE_ADMIN_KEYS", "UPSTREAM_LLM_URL": "TRUTHTABLE_UPSTREAM_URL",
		"TRUTHTABLE_REDIS_URL": "REDIS_URL", "KNOWLEDGE_ENABLED": "TRUTHTABLE_KNOWLEDGE_ENABLED", "RAG_ENABLED": "TRUTHTABLE_KNOWLEDGE_ENABLED",
	} {
		k, ok := reg.Lookup(alias)
		if !ok || k.Name != canonical {
			t.Errorf("alias %s resolves to %+v, want %s", alias, k, canonical)
		}
	}
	for _, name := range []string{"LLM_PROVIDER", "VITE_APP_MODE", "COMPOSE_PROFILES", "TRUTHTABLE_ENGINE_MAX_RPCS"} {
		if !reg.Known(name) {
			t.Errorf("%s (another service's key) is not in the union registry", name)
		}
		if k, _ := reg.Lookup(name); k.HasService(ServiceProxy) {
			t.Errorf("%s is not a proxy key", name)
		}
	}
	if !strings.Contains(string(reg.Raw()), `"TRUTHTABLE_PORT"`) {
		t.Error("Raw does not return the document")
	}
	if MustRegistry() != reg {
		t.Error("the embedded registry is parsed once")
	}
}

// TestRegistryDefaultsValidate: every proxy key's default (after ${NAME} expansion) is a valid
// value of its kind, so an invalid supplied value can always fall back to it.
func TestRegistryDefaultsValidate(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{}})
	for _, e := range c.Entries() {
		if e.Value == "" {
			continue
		}
		if _, err := validate(e.Key, e.Value); err != nil {
			t.Errorf("%s: default %q is not %v", e.Key.Name, e.Value, err)
		}
		if strings.Contains(e.Value, "${") {
			t.Errorf("%s: default %q was not expanded", e.Key.Name, e.Value)
		}
	}
}

// TestEveryFieldIsBoundToAProxyKey: every tagged Config field names a proxy key of the registry,
// no key is bound twice, and the default of each bound key converts into the field (a registry
// kind that does not fit the Go type would panic in Load).
func TestEveryFieldIsBoundToAProxyKey(t *testing.T) {
	reg := MustRegistry()
	fields := BoundFields()
	seen := map[string]string{}
	for name, field := range fields {
		k, ok := reg.Lookup(name)
		if !ok || k.Name != name {
			t.Errorf("field %s: %s is not a canonical registry key", field, name)
			continue
		}
		if !k.HasService(ServiceProxy) {
			t.Errorf("field %s: %s is not a proxy key", field, name)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("%s is bound to %s and %s", name, prev, field)
		}
		seen[name] = field
	}
	if len(fields) < 70 {
		t.Errorf("only %d bound fields", len(fields))
	}
	// Loading the defaults exercises every conversion.
	_ = LoadWith(LoadOptions{Environ: []string{}})
}

// TestDefaultsComeFromTheRegistry: a Config loaded from an empty environment holds the registry's
// defaults, including the ones S0 hard-coded differently (write timeout 0, shutdown 30s).
func TestDefaultsComeFromTheRegistry(t *testing.T) {
	c := LoadWith(LoadOptions{Environ: []string{}})
	if len(c.Diagnostics) != 0 {
		t.Fatalf("warnings %v", c.Warnings)
	}
	checks := map[string]any{
		"ServerPort": 8080, "WriteTimeout": time.Duration(0), "ShutdownTimeout": 30 * time.Second,
		"GRPCTimeout": 120 * time.Second, "MaxBodyBytes": int64(1 << 20), "IngestAuth": "keys",
		"PublicURL": "http://localhost:3000", "LogLevel": "info", "UpstreamURL": DefaultUpstreamURL,
		"InternalTokenFile": DefaultInternalTokenFile, "SpendCapDecisionUSDDay": 2.0,
	}
	v := reflect.ValueOf(*c)
	for field, want := range checks {
		if got := v.FieldByName(field).Interface(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", field, got, want)
		}
	}
	a := c.Auth
	if a.SessionTTL != 12*time.Hour || a.SetupTokenTTL != time.Hour || a.CookieSecure != "auto" || a.SecretKey != "" || a.SetupToken != "" {
		t.Errorf("auth defaults %+v", a)
	}
	if !strings.HasSuffix(a.SecretKeyFile, ".trustagent/secret.key") || !strings.HasSuffix(c.DataDir, ".trustagent") {
		t.Errorf("secret key file %q / data dir %q: ${TRUTHTABLE_DATA_DIR} expansion", a.SecretKeyFile, c.DataDir)
	}
	for _, e := range c.Entries() {
		if e.Source != SourceDefault || e.Var != "" || e.Invalid {
			t.Errorf("%s: %+v in an empty environment", e.Key.Name, e)
		}
	}
}

func TestParseRegistryRejectsBrokenDocuments(t *testing.T) {
	good := `{"version":"1","groups":[{"id":"core","title":"Core"}],"keys":[` +
		`{"name":"A_KEY","aliases":["B_KEY"],"services":["proxy"],"kind":"enum","enum":["x","y"],"default":"x",` +
		`"secret":false,"advanced":true,"restart":true,"group":"core","description":"d"}]}`
	if _, err := ParseRegistry([]byte(good)); err != nil {
		t.Fatalf("good registry: %v", err)
	}
	for name, doc := range map[string]string{
		"version":       strings.Replace(good, `"version":"1"`, `"version":"2"`, 1),
		"unknown kind":  strings.Replace(good, `"kind":"enum"`, `"kind":"money"`, 1),
		"unknown group": strings.Replace(good, `"group":"core"`, `"group":"nope"`, 1),
		"enum default":  strings.Replace(good, `"default":"x"`, `"default":"z"`, 1),
		"bad name":      strings.Replace(good, `"name":"A_KEY"`, `"name":"a-key"`, 1),
		"alias clash":   strings.Replace(good, `"aliases":["B_KEY"]`, `"aliases":["A_KEY"]`, 1),
		"unknown field": strings.Replace(good, `"secret":false`, `"secret":false,"sekret":1`, 1),
		"no enum":       strings.Replace(good, `"enum":["x","y"],`, ``, 1),
		"not json":      `{`,
	} {
		if _, err := ParseRegistry([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLevenshteinAndClosest(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{
		{"", "", 0}, {"abc", "", 3}, {"kitten", "sitting", 3}, {"TRUTHTABLE_PROT", "TRUTHTABLE_PORT", 2},
		{"TRUTHTABLE_PORT", "TRUTHTABLE_PORT", 0}, {"ab", "ba", 2}, {"héllo", "hello", 1},
	} {
		if got := Levenshtein(c.a, c.b); got != c.d {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.d)
		}
	}
	cands := []string{"TRUTHTABLE_PORT", "TRUTHTABLE_METRICS_PORT", "TRUTHTABLE_POLICY_FILE"}
	if got := Closest("TRUTHTABLE_PROT", cands, 2); got != "TRUTHTABLE_PORT" {
		t.Errorf("closest %q", got)
	}
	if got := Closest("TRUTHTABLE_XYZ", cands, 2); got != "" {
		t.Errorf("nothing is within 2 edits, got %q", got)
	}
	if got := Closest("AB", []string{"AC", "AD", "AA"}, 2); got != "AA" {
		t.Errorf("a tie goes to the alphabetically first candidate, got %q", got)
	}
}
