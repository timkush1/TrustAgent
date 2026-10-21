package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/truthtable/contracts"
)

// RegistryPath is the registry's path inside contracts.FS (SPEC §2.7: contracts/config/keys.v1.json
// is the single configuration registry).
const RegistryPath = "config/keys.v1.json"

// ServiceProxy is the registry service id of the Go proxy.
const ServiceProxy = "proxy"

// Registry kinds (contracts/schema/keys.v1.schema.json).
const (
	KindString   = "string"
	KindInt      = "int"
	KindFloat    = "float"
	KindBool     = "bool"
	KindDuration = "duration"
	KindBytes    = "bytes"
	KindURL      = "url"
	KindEnum     = "enum"
	KindList     = "list"
	KindPath     = "path"
	KindKeyList  = "key_list"
)

var knownKinds = map[string]bool{
	KindString: true, KindInt: true, KindFloat: true, KindBool: true, KindDuration: true, KindBytes: true,
	KindURL: true, KindEnum: true, KindList: true, KindPath: true, KindKeyList: true,
}

// keyNamePattern is the registry's name and alias syntax.
var keyNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*[A-Z0-9]$`)

// Key is one registry entry.
type Key struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Services []string `json:"services"`
	Kind     string   `json:"kind"`
	// Default is the built-in default in env-var syntax; nil means there is no static default
	// (generated, derived or backend-specific; see DefaultNote). It may reference another key as
	// ${NAME}.
	Default        *string           `json:"default"`
	DefaultNote    string            `json:"default_note,omitempty"`
	ComposeDefault string            `json:"compose_default,omitempty"`
	Enum           []string          `json:"enum,omitempty"`
	Min            *float64          `json:"min,omitempty"`
	Max            *float64          `json:"max,omitempty"`
	Shorthands     map[string]string `json:"shorthands,omitempty"`
	Secret         bool              `json:"secret"`
	Advanced       bool              `json:"advanced"`
	Restart        bool              `json:"restart"`
	Runtime        string            `json:"runtime,omitempty"`
	Group          string            `json:"group"`
	Description    string            `json:"description"`
}

// DefaultValue is the default in env-var syntax ("" when the key has no static default).
func (k *Key) DefaultValue() string {
	if k.Default == nil {
		return ""
	}
	return *k.Default
}

// HasService reports whether svc reads the key.
func (k *Key) HasService(svc string) bool {
	for _, s := range k.Services {
		if s == svc {
			return true
		}
	}
	return false
}

// Names returns the canonical name followed by the aliases.
func (k *Key) Names() []string { return append([]string{k.Name}, k.Aliases...) }

// Group is a registry display group.
type Group struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// Registry is the parsed configuration key registry.
type Registry struct {
	Version string  `json:"version"`
	Groups  []Group `json:"groups"`
	Keys    []Key   `json:"keys"`

	raw    []byte
	byName map[string]*Key // canonical names and aliases
}

// ParseRegistry parses and checks a registry document: known kinds, valid names, no name or
// alias used twice, an enum for every enum key, defaults inside their enum, groups that exist.
func ParseRegistry(b []byte) (*Registry, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var r Registry
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("config registry: %w", err)
	}
	if r.Version != "1" {
		return nil, fmt.Errorf("config registry: version %q, want 1", r.Version)
	}
	groups := map[string]bool{}
	for _, g := range r.Groups {
		groups[g.ID] = true
	}
	r.byName = make(map[string]*Key, len(r.Keys)*2)
	for i := range r.Keys {
		k := &r.Keys[i]
		if !knownKinds[k.Kind] {
			return nil, fmt.Errorf("config registry: %s: unknown kind %q", k.Name, k.Kind)
		}
		if !groups[k.Group] {
			return nil, fmt.Errorf("config registry: %s: unknown group %q", k.Name, k.Group)
		}
		if k.Kind == KindEnum && len(k.Enum) == 0 {
			return nil, fmt.Errorf("config registry: %s: an enum key needs enum values", k.Name)
		}
		if k.Kind == KindEnum && k.DefaultValue() != "" && !containsFold(k.Enum, k.DefaultValue()) {
			return nil, fmt.Errorf("config registry: %s: default %q is not in its enum", k.Name, k.DefaultValue())
		}
		for _, n := range k.Names() {
			if !keyNamePattern.MatchString(n) {
				return nil, fmt.Errorf("config registry: invalid key name %q", n)
			}
			if prev, dup := r.byName[n]; dup {
				return nil, fmt.Errorf("config registry: %s is used by %s and %s", n, prev.Name, k.Name)
			}
			r.byName[n] = k
		}
	}
	r.raw = append([]byte(nil), b...)
	return &r, nil
}

var embedded = sync.OnceValues(func() (*Registry, error) {
	b, err := contracts.FS.ReadFile(RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("config registry: %w", err)
	}
	return ParseRegistry(b)
})

// LoadRegistry returns the registry embedded from contracts/config/keys.v1.json. It is parsed once.
func LoadRegistry() (*Registry, error) { return embedded() }

// MustRegistry is LoadRegistry for callers that cannot continue without it; the embedded registry
// is checked by this package's tests, so a failure is a build defect.
func MustRegistry() *Registry {
	r, err := LoadRegistry()
	if err != nil {
		panic(err)
	}
	return r
}

// Lookup returns the key a canonical name or an alias names.
func (r *Registry) Lookup(name string) (*Key, bool) {
	k, ok := r.byName[name]
	return k, ok
}

// Known reports whether name is a canonical name or an alias of any service's key (the union
// registry the unknown-key check uses).
func (r *Registry) Known(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// ForService returns the keys svc reads, in registry order.
func (r *Registry) ForService(svc string) []*Key {
	var out []*Key
	for i := range r.Keys {
		if r.Keys[i].HasService(svc) {
			out = append(out, &r.Keys[i])
		}
	}
	return out
}

// AllNames returns every canonical name and alias, sorted.
func (r *Registry) AllNames() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Raw returns the registry document as parsed.
func (r *Registry) Raw() []byte { return append([]byte(nil), r.raw...) }

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}
