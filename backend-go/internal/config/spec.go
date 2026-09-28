package config

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// SpecKey is one key of the proxy's configuration spec (-dump-config-spec), in the shape of the
// engine's `python -m truthtable.config --dump-spec`, for scripts/gen_config_docs.py.
type SpecKey struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	// Field is the Config field path the key is bound to (for example "Jev.AttemptTimeout"), or
	// null for a registry key of the proxy that no field reads yet (it is still validated).
	Field   *string `json:"field"`
	Default *string `json:"default"`
	Secret  bool    `json:"secret"`
}

// Spec is the proxy's configuration spec.
type Spec struct {
	Service string    `json:"service"`
	Keys    []SpecKey `json:"keys"`
}

// DumpSpec returns every proxy key of the registry with the Config field bound to it, sorted by
// name.
func DumpSpec(reg *Registry) Spec {
	fields := BoundFields()
	spec := Spec{Service: ServiceProxy, Keys: []SpecKey{}}
	for _, k := range reg.ForService(ServiceProxy) {
		sk := SpecKey{Name: k.Name, Aliases: append([]string{}, k.Aliases...), Default: k.Default, Secret: k.Secret}
		if f, ok := fields[k.Name]; ok {
			sk.Field = &f
		}
		spec.Keys = append(spec.Keys, sk)
	}
	sort.Slice(spec.Keys, func(i, j int) bool { return spec.Keys[i].Name < spec.Keys[j].Name })
	return spec
}

// DumpSpecJSON is DumpSpec as indented JSON with a trailing newline.
func DumpSpecJSON(reg *Registry) ([]byte, error) {
	b, err := json.MarshalIndent(DumpSpec(reg), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// BoundFields maps every registry key a Config field is bound to onto the field's path.
func BoundFields() map[string]string {
	out := map[string]string{}
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if tag, ok := f.Tag.Lookup("env"); ok {
				name, _, _ := strings.Cut(tag, ",")
				out[name] = prefix + f.Name
				continue
			}
			if f.Type.Kind() == reflect.Struct && f.Type != durationType && f.IsExported() {
				walk(f.Type, prefix+f.Name+".")
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "")
	return out
}
