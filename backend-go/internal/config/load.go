package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Source says where an effective value came from (api.ts SettingKeyView.source).
type Source string

// Value sources at boot.
const (
	SourceDefault Source = "default"
	SourceDotenv  Source = "dotenv"
	SourceEnv     Source = "env"
)

// Entry is one registry key's effective value.
type Entry struct {
	Key *Key
	// Value is the effective value in env-var syntax, after shorthand and ${NAME} expansion: the
	// supplied value, or the default when none was supplied or the supplied one was invalid.
	Value string
	// Source is where Value came from; an invalid supplied value reports SourceDefault.
	Source Source
	// Var is the variable that supplied the value: the canonical name or an alias ("" for the
	// default).
	Var string
	// Invalid is set when a supplied value was refused and the default is in effect.
	Invalid bool
}

// Warning is a configuration warning (SPEC §2.7 Warning{key, message, hint}). Messages never
// contain the value of a secret key.
type Warning struct {
	Key     string `json:"key"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// String renders the warning as "KEY: message (hint)".
func (w Warning) String() string {
	s := w.Key + ": " + w.Message
	if w.Hint != "" {
		s += " (" + w.Hint + ")"
	}
	return s
}

// LoadOptions override where Load reads from; the zero value reads the process.
type LoadOptions struct {
	// Environ is the process environment as KEY=VALUE pairs (default os.Environ()).
	Environ []string
	// WorkDir is where the automatic .env search starts (default the working directory).
	WorkDir string
	// AutoDotenv enables the automatic .env search (TRUTHTABLE_ENV_FILE unset or "auto"). The
	// default is on, except inside `go test` binaries, so a developer's .env never leaks into
	// tests; an explicit TRUTHTABLE_ENV_FILE is always honoured.
	AutoDotenv *bool
	// Registry replaces the embedded registry (tests).
	Registry *Registry
}

// Load reads the proxy configuration from the process environment and the dotenv file.
func Load() *Config { return LoadWith(LoadOptions{}) }

// LoadWith reads the configuration as Load does, from the given sources.
func LoadWith(o LoadOptions) *Config {
	reg := o.Registry
	if reg == nil {
		reg = MustRegistry()
	}
	environ := o.Environ
	if environ == nil {
		environ = os.Environ()
	}
	workDir := o.WorkDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	auto := !testing.Testing()
	if o.AutoDotenv != nil {
		auto = *o.AutoDotenv
	}

	l := &loader{reg: reg, vars: map[string]string{}, sources: map[string]Source{}}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		if _, dup := l.vars[name]; !dup { // the first occurrence wins, as in os.Getenv
			l.vars[name] = value
			l.sources[name] = SourceEnv
		}
	}
	c := &Config{registry: reg}
	c.EnvFile = l.readDotenv(workDir, auto)
	l.resolve()
	l.bind(c)
	l.crossChecks(c)
	l.unknownKeys()

	c.entries = l.entries
	c.Diagnostics = l.warnings
	c.Warnings = make([]string, 0, len(l.warnings))
	for _, w := range l.warnings {
		c.Warnings = append(c.Warnings, w.String())
	}
	return c
}

// loader resolves registry keys against the merged environment and collects warnings.
type loader struct {
	reg      *Registry
	vars     map[string]string // process env, then dotenv assignments of names not already set
	sources  map[string]Source
	entries  []Entry
	byName   map[string]*Entry
	warnings []Warning
}

func (l *loader) warn(key, message, hint string) {
	l.warnings = append(l.warnings, Warning{Key: key, Message: message, Hint: hint})
}

// readDotenv merges the dotenv file into vars without overriding set variables and returns its
// path ("" when no file was read).
func (l *loader) readDotenv(workDir string, auto bool) string {
	value, set := l.vars[EnvFileVar]
	path, explicit := envFileChoice(value, set, workDir, auto)
	if path == "" {
		return ""
	}
	data, err := readDotenv(path)
	if err != nil {
		if explicit || !isNotExist(err) {
			l.warn(EnvFileVar, "the dotenv file cannot be read: "+scrubPathError(err), "fix the path or unset "+EnvFileVar)
		}
		return ""
	}
	vars, problems := parseDotenv(data, func(name string) (string, bool) {
		v, ok := l.vars[name]
		return v, ok
	})
	for _, p := range problems {
		l.warn(EnvFileVar, fmt.Sprintf("%s line %d: %s", path, p.Line, p.Message), "the line is ignored")
	}
	merged := map[string]string{}
	order := []string{}
	for _, v := range vars {
		if _, seen := merged[v.Name]; !seen {
			order = append(order, v.Name)
		}
		merged[v.Name] = v.Value // a later assignment wins
	}
	for _, name := range order {
		if _, already := l.vars[name]; already {
			continue // never override a variable that is already set
		}
		l.vars[name] = merged[name]
		l.sources[name] = SourceDotenv
	}
	return path
}

// scrubPathError keeps an os error readable without repeating file contents.
func scrubPathError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + " " + pe.Path + ": " + pe.Err.Error()
	}
	return err.Error()
}

// supplied returns the first non-empty value among a key's canonical name and aliases.
func (l *loader) supplied(k *Key) (value, name string, src Source) {
	for _, n := range k.Names() {
		if v, ok := l.vars[n]; ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, n, l.sources[n]
			}
		}
	}
	return "", "", SourceDefault
}

// resolve computes the effective value of every proxy key: supplied values first, then
// shorthands and ${NAME} references (which may name any resolved key), then validation.
func (l *loader) resolve() {
	keys := l.reg.ForService(ServiceProxy)
	l.entries = make([]Entry, len(keys))
	l.byName = make(map[string]*Entry, len(keys))
	for i, k := range keys {
		v, name, src := l.supplied(k)
		if src == SourceDefault {
			v = k.DefaultValue()
		}
		l.entries[i] = Entry{Key: k, Value: v, Source: src, Var: name}
		l.byName[k.Name] = &l.entries[i]
	}
	ref := func(name string) string {
		if e, ok := l.byName[name]; ok {
			return e.Value
		}
		if k, ok := l.reg.Lookup(name); ok {
			if v, _, src := l.supplied(k); src != SourceDefault {
				return v
			}
			return k.DefaultValue()
		}
		return ""
	}
	// Expansion reads the first-pass values, so every entry expands against the same snapshot.
	expanded := make([]string, len(l.entries))
	for i := range l.entries {
		e := &l.entries[i]
		v := e.Value
		if e.Source != SourceDefault {
			if target, ok := shorthand(e.Key, v); ok {
				v = expandRefs(target, ref)
			}
		} else {
			v = expandRefs(v, ref)
		}
		expanded[i] = v
	}
	for i := range l.entries {
		e := &l.entries[i]
		e.Value = expanded[i]
		if e.Source == SourceDefault {
			continue
		}
		norm, err := validate(e.Key, e.Value)
		if err != nil {
			l.invalid(e, err.Error())
			e.Value, e.Source, e.Var, e.Invalid = expandRefs(e.Key.DefaultValue(), ref), SourceDefault, "", true
			continue
		}
		e.Value = norm
	}
}

// invalid reports a refused value without echoing a secret.
func (l *loader) invalid(e *Entry, want string) {
	msg := fmt.Sprintf("%q is not %s", e.Value, want)
	if e.Key.Secret {
		msg = "the value is not " + want
	}
	if e.Var != "" && e.Var != e.Key.Name {
		msg += " (set as " + e.Var + ")"
	}
	def := e.Key.DefaultValue()
	hint := "using the default " + strconv.Quote(def)
	if def == "" {
		hint = "using the default (unset)"
	}
	l.warn(e.Key.Name, msg, hint)
}

// shorthand expands a registry shorthand (for example TRUTHTABLE_UPSTREAM_URL=ollama).
func shorthand(k *Key, v string) (string, bool) {
	for name, target := range k.Shorthands {
		if strings.EqualFold(name, v) {
			return target, true
		}
	}
	return "", false
}

// validate checks a value against its key's kind, enum and bounds and returns it normalized (enum
// values in their registry spelling). The error text completes "… is not <error>".
func validate(k *Key, v string) (string, error) {
	switch k.Kind {
	case KindInt, KindBytes:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || (k.Kind == KindBytes && n < 0) || !inBounds(k, float64(n)) {
			return "", fmt.Errorf("%s", wantFor(k))
		}
	case KindFloat:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || !inBounds(k, f) {
			return "", fmt.Errorf("%s", wantFor(k))
		}
	case KindBool:
		if _, err := strconv.ParseBool(v); err != nil {
			return "", fmt.Errorf("%s", wantFor(k))
		}
	case KindDuration:
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return "", fmt.Errorf("%s", wantFor(k))
		}
	case KindURL:
		if !validURL(v) {
			return "", fmt.Errorf("%s", wantFor(k))
		}
	case KindEnum:
		for _, allowed := range k.Enum {
			if strings.EqualFold(allowed, v) {
				return allowed, nil
			}
		}
		return "", fmt.Errorf("%s", wantFor(k))
	case KindList:
		items := splitList(v)
		if len(k.Enum) > 0 {
			for i, item := range items {
				ok := false
				for _, allowed := range k.Enum {
					if strings.EqualFold(allowed, item) {
						items[i], ok = allowed, true
					}
				}
				if !ok {
					return "", fmt.Errorf("%s", wantFor(k))
				}
			}
		}
		return strings.Join(items, ","), nil
	}
	return v, nil
}

// wantFor describes what a key accepts.
func wantFor(k *Key) string {
	switch k.Kind {
	case KindInt:
		return "an integer" + bounds(k)
	case KindBytes:
		return "a byte count (a non-negative integer)" + bounds(k)
	case KindFloat:
		return "a number" + bounds(k)
	case KindBool:
		return "true or false"
	case KindDuration:
		return "a non-negative Go duration such as 800ms, 30s or 5m"
	case KindURL:
		return "an absolute URL such as https://host"
	case KindEnum:
		return "one of " + strings.Join(k.Enum, ", ")
	case KindList:
		return "a comma-separated list of " + strings.Join(k.Enum, ", ")
	}
	return "valid"
}

func bounds(k *Key) string {
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	switch {
	case k.Min != nil && k.Max != nil:
		return " from " + f(*k.Min) + " to " + f(*k.Max)
	case k.Min != nil:
		return " of at least " + f(*k.Min)
	case k.Max != nil:
		return " of at most " + f(*k.Max)
	}
	return ""
}

func inBounds(k *Key, x float64) bool {
	return (k.Min == nil || x >= *k.Min) && (k.Max == nil || x <= *k.Max)
}

// validURL accepts an absolute URL: a scheme and, for http(s), a host (postgres:// and redis://
// connection strings are URLs too).
func validURL(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || !strings.Contains(v, "://") {
		return false
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.Host != ""
	}
	return true
}

// splitList splits a comma-separated list, trimming items and dropping empty ones.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

var durationType = reflect.TypeOf(time.Duration(0))

// bind sets every tagged Config field from its entry.
func (l *loader) bind(c *Config) {
	l.bindStruct(reflect.ValueOf(c).Elem())
	if c.UpstreamURL == "" {
		c.UpstreamURL = DefaultUpstreamURL
	}
	c.TrustedProxies = l.networks("TRUTHTABLE_TRUSTED_PROXIES", c.TrustedProxies)
	if c.Jev.APIKey == "" {
		c.Jev.APIKey = l.secretFile("TRUTHTABLE_JEV_API_KEY_FILE")
	}
}

// secretFile reads a credential from the file a *_FILE key names (Docker secrets). A file that
// cannot be read is a warning (without its contents) and yields no credential.
func (l *loader) secretFile(key string) string {
	e, ok := l.byName[key]
	if !ok || e.Value == "" {
		return ""
	}
	b, err := os.ReadFile(e.Value) // #nosec G304 -- the operator names the credential file (a registry *_FILE key)
	if err != nil {
		l.warn(key, fmt.Sprintf("the file %q cannot be read", e.Value), "no credential is loaded from it")
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (l *loader) bindStruct(v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		tag, ok := f.Tag.Lookup("env")
		if !ok {
			if f.Type.Kind() == reflect.Struct && f.Type != durationType && f.IsExported() {
				l.bindStruct(fv)
			}
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		e, found := l.byName[name]
		if !found {
			panic(fmt.Sprintf("config: field %s is bound to %s, which is not a proxy key of the registry", f.Name, name))
		}
		value := e.Value
		switch opts {
		case "repopath":
			value = RepoPath(value)
		case "repodir":
			value = repoDir(value)
		}
		if err := l.set(fv, e, value); err != nil {
			panic(fmt.Sprintf("config: field %s (%s, kind %s): %v", f.Name, name, e.Key.Kind, err))
		}
	}
}

// set converts a validated value into a field. An error is a binding defect (a field type that
// does not fit the key's kind), which the package tests rule out.
func (l *loader) set(fv reflect.Value, e *Entry, value string) error {
	if value == "" && fv.Kind() != reflect.String && fv.Kind() != reflect.Slice {
		fv.SetZero() // a key without a static default (for example TRUTHTABLE_JEV_PRICE_PER_MTOK)
		return nil
	}
	switch {
	case fv.Type() == durationType:
		if value == "" {
			fv.SetInt(0)
			return nil
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
	case fv.Kind() == reflect.String:
		fv.SetString(value)
	case fv.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case fv.Kind() == reflect.Int || fv.Kind() == reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
		if fv.OverflowInt(n) {
			return fmt.Errorf("%d overflows %s", n, fv.Type())
		}
		fv.SetInt(n)
	case fv.Kind() == reflect.Float64:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	case fv.Type() == reflect.TypeOf([]string(nil)):
		fv.Set(reflect.ValueOf(splitList(value)))
	case fv.Type() == reflect.TypeOf([]APIKey(nil)):
		if e.Key.Kind != KindKeyList {
			return fmt.Errorf("[]APIKey needs a key_list")
		}
		fv.Set(reflect.ValueOf(l.keyList(e.Key.Name, value)))
	default:
		return fmt.Errorf("unsupported field type %s", fv.Type())
	}
	return nil
}

// keyList parses "label=key,..." (kind key_list). An entry without a label is kept as key<n> with a
// warning; an entry missing its label or key is ignored with a warning. Keys are never echoed.
func (l *loader) keyList(key, value string) []APIKey {
	var out []APIKey
	seen := map[string]bool{}
	for i, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		label, secret, ok := strings.Cut(item, "=")
		label, secret = strings.TrimSpace(label), strings.TrimSpace(secret)
		if !ok {
			label, secret = fmt.Sprintf("key%d", i+1), item
			l.warn(key, fmt.Sprintf("entry %d has no label", i+1), "write it as label=key; it is used as "+label)
		}
		if label == "" || secret == "" {
			l.warn(key, fmt.Sprintf("entry %d needs both a label and a key", i+1), "the entry is ignored")
			continue
		}
		if seen[label] {
			l.warn(key, fmt.Sprintf("label %q is used twice", label), "give every key its own label")
		}
		seen[label] = true
		out = append(out, APIKey{Label: label, Key: secret})
	}
	return out
}

// networks keeps the CIDRs and IP addresses of a list; an entry that is neither is ignored with a
// warning. Empty means none (nil).
func (l *loader) networks(key string, items []string) []string {
	var out []string
	for _, item := range items {
		if _, _, err := net.ParseCIDR(item); err == nil || net.ParseIP(item) != nil {
			out = append(out, item)
			continue
		}
		l.warn(key, fmt.Sprintf("entry %q is not a CIDR or an IP address", item), "the entry is ignored")
	}
	return out
}

// minSetupTokenLen is the shortest TRUTHTABLE_SETUP_TOKEN accepted as a seed.
const minSetupTokenLen = 16

// crossChecks reports combinations of values that are refused or risky.
func (l *loader) crossChecks(c *Config) {
	// Inject mode refuses open ingest (SPEC §0 #1, §2.4): the proxy would otherwise spend its own
	// upstream credential on anonymous callers.
	if c.IngestAuth == "open" && c.Injecting() {
		l.warn("TRUTHTABLE_INGEST_AUTH", "open is refused while TRUTHTABLE_UPSTREAM_API_KEY injects an upstream credential",
			"ingest keys stay required; set TRUTHTABLE_API_KEYS (label=key)")
		c.IngestAuth = "keys"
	}
	// One secret in several key lists: its scopes are the union, which is rarely intended.
	lists := []struct {
		name string
		keys []APIKey
	}{{"TRUTHTABLE_API_KEYS", c.APIKeys}, {"TRUTHTABLE_READ_KEYS", c.Auth.ReadKeys}, {"TRUTHTABLE_ADMIN_KEYS", c.Auth.AdminKeys}}
	owner := map[string]string{}
	for _, list := range lists {
		for _, k := range list.keys {
			if prev, ok := owner[k.Key]; ok && prev != list.name {
				l.warn(list.name, fmt.Sprintf("the key labelled %q is also in %s", k.Label, prev), "its scopes are the union of both lists")
				continue
			}
			owner[k.Key] = list.name
		}
	}
	if s := c.Auth.SecretKey; s != "" {
		if b, err := base64.StdEncoding.DecodeString(s); err != nil || len(b) != 32 {
			l.warn("TRUTHTABLE_SECRET_KEY", "the value is not 32 bytes of standard base64",
				"generate one with: openssl rand -base64 32; the key file is used instead")
			c.Auth.SecretKey = ""
		}
	}
	if t := c.Auth.SetupToken; t != "" && len(t) < minSetupTokenLen {
		l.warn("TRUTHTABLE_SETUP_TOKEN", fmt.Sprintf("the value is shorter than %d characters", minSetupTokenLen),
			"a random token is generated instead")
		c.Auth.SetupToken = ""
	}
}

// unknownKeys warns about TRUTHTABLE_* names that no service's registry knows, and about
// unprefixed names within 2 edits of a proxy key with the same first segment (for example
// OPENROUTER_API_KY). Empty variables are ignored.
func (l *loader) unknownKeys() {
	var unprefixed []string
	for _, k := range l.reg.ForService(ServiceProxy) {
		for _, n := range k.Names() {
			if !strings.HasPrefix(n, "TRUTHTABLE_") {
				unprefixed = append(unprefixed, n)
			}
		}
	}
	sort.Strings(unprefixed)
	all := l.reg.AllNames()

	names := make([]string, 0, len(l.vars))
	for n, v := range l.vars {
		if strings.TrimSpace(v) != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		upper := strings.ToUpper(name)
		if l.reg.Known(upper) && upper == name {
			continue
		}
		if strings.HasPrefix(upper, "TRUTHTABLE_") {
			hint := "not in the configuration registry"
			if s := Closest(upper, all, 2); s != "" {
				hint = "did you mean " + s + "?"
			}
			l.warn(name, "unknown key", hint)
			continue
		}
		first, _, _ := strings.Cut(upper, "_")
		var candidates []string
		for _, n := range unprefixed {
			if strings.HasPrefix(n, first+"_") {
				candidates = append(candidates, n)
			}
		}
		if s := Closest(upper, candidates, 2); s != "" && s != name {
			l.warn(name, "unknown key", "did you mean "+s+"?")
		}
	}
}
