package runtimecfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// The dotted paths of RuntimeSettings (api.ts RuntimeSettings): what PATCH /api/settings writes,
// what env_managed lists (D33) and what settings_updated names. Objects nest by dots
// (upstream.default.url); pricing_overrides is one leaf, the whole array.

// LeafKind is the JSON type a RuntimeSettings leaf takes.
type LeafKind int

const (
	// LeafString is a string that is never null (an enum such as decision_model.backend).
	LeafString LeafKind = iota
	// LeafNullableString is a string or null (null: unset, the default applies).
	LeafNullableString
	// LeafNumber is a JSON number (a budget in USD).
	LeafNumber
	// LeafPrices is the pricing_overrides array, written whole.
	LeafPrices
)

// Leaf is one RuntimeSettings path a write may set.
type Leaf struct {
	Path string
	Kind LeafKind
	// Key is the registry key whose env value seeds the path and pins it when non-empty ("" when
	// none does; the judge keys live in the engine's registry, D33).
	Key string
}

// Leaves are every RuntimeSettings leaf, in display order.
var Leaves = []Leaf{
	{"decision_model.backend", LeafString, "TRUTHTABLE_JEV_BACKEND"},
	{"decision_model.model", LeafNullableString, "TRUTHTABLE_JEV_MODEL"},
	{"decision_model.base_url", LeafNullableString, "TRUTHTABLE_JEV_BASE_URL"},
	{"decision_model.path", LeafNullableString, "TRUTHTABLE_JEV_PATH"},
	{"judge.provider", LeafString, keyLLMProvider},
	{"judge.model", LeafNullableString, keyLLMModel},
	{"upstream.default.url", LeafNullableString, "TRUTHTABLE_UPSTREAM_URL"},
	{"upstream.default.credential", LeafNullableString, ""},
	{"upstream.default.auth_header", LeafString, "TRUTHTABLE_UPSTREAM_AUTH_HEADER"},
	{"upstream.anthropic.url", LeafNullableString, "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL"},
	{"upstream.anthropic.credential", LeafNullableString, ""},
	{"upstream.anthropic.auth_header", LeafString, ""},
	{"upstream.fallback.url", LeafNullableString, "TRUTHTABLE_FALLBACK_UPSTREAM_URL"},
	{"upstream.fallback.credential", LeafNullableString, ""},
	{"upstream.fallback.auth_header", LeafString, ""},
	{"upstream.fallback.format", LeafString, "TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT"},
	{"bindings.decision_model", LeafNullableString, ""},
	{"bindings.judge", LeafNullableString, ""},
	{"bindings.upstream_default", LeafNullableString, ""},
	{"budgets.decision_usd_day", LeafNumber, "TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY"},
	{"budgets.llm_usd_day", LeafNumber, "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY"},
	{"pricing_overrides", LeafPrices, ""},
}

// LookupLeaf returns the leaf at path.
func LookupLeaf(path string) (Leaf, bool) {
	i := slices.IndexFunc(Leaves, func(l Leaf) bool { return l.Path == path })
	if i < 0 {
		return Leaf{}, false
	}
	return Leaves[i], true
}

// IsBranch reports whether path is an object of RuntimeSettings that holds leaves (decision_model,
// upstream, upstream.default, ...), as opposed to a leaf or an unknown path.
func IsBranch(path string) bool {
	return slices.ContainsFunc(Leaves, func(l Leaf) bool { return strings.HasPrefix(l.Path, path+".") })
}

// SettingPath is the RuntimeSettings path the registry key seeds ("" when it seeds none).
func SettingPath(key string) string {
	for _, l := range Leaves {
		if l.Key != "" && l.Key == key {
			return l.Path
		}
	}
	return ""
}

// Flatten returns every leaf of s as its JSON value, by path.
func Flatten(s RuntimeSettings) map[string]json.RawMessage {
	doc, err := json.Marshal(s)
	if err != nil {
		return map[string]json.RawMessage{} // RuntimeSettings always marshals
	}
	out := make(map[string]json.RawMessage, len(Leaves))
	for _, l := range Leaves {
		if v, ok := lookupRaw(doc, l.Path); ok {
			out[l.Path] = compact(v)
		}
	}
	return out
}

// lookupRaw returns the JSON value at the dotted path of the JSON object doc.
func lookupRaw(doc json.RawMessage, path string) (json.RawMessage, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		v, ok := obj[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// compact is v without insignificant whitespace, so equal values compare equal as bytes.
func compact(v json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if err := json.Compact(&b, v); err != nil {
		return v
	}
	return b.Bytes()
}

// SetLeaves returns s with each leaf path set to its JSON value. Every path must be a leaf and
// every value must have the leaf's JSON type (CheckLeaf), else an error names the path.
func SetLeaves(s RuntimeSettings, leaves map[string]json.RawMessage) (RuntimeSettings, error) {
	if len(leaves) == 0 {
		return cloneSettings(s), nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return s, err
	}
	for _, path := range sortedPaths(leaves) {
		l, ok := LookupLeaf(path)
		if !ok {
			return s, fmt.Errorf("%s is not a runtime setting", path)
		}
		if err := CheckLeaf(l, leaves[path]); err != nil {
			return s, fmt.Errorf("%s %w", path, err)
		}
		setPath(doc, path, json.RawMessage(compact(leaves[path])))
	}
	b, err = json.Marshal(doc)
	if err != nil {
		return s, err
	}
	var out RuntimeSettings
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return s, err
	}
	if out.PricingOverrides == nil {
		out.PricingOverrides = s.PricingOverrides[:0:0]
	}
	return out, nil
}

// cloneSettings is a deep copy of s (its pointers and slice are not shared).
func cloneSettings(s RuntimeSettings) RuntimeSettings {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out RuntimeSettings
	if json.Unmarshal(b, &out) != nil {
		return s
	}
	if out.PricingOverrides == nil {
		out.PricingOverrides = s.PricingOverrides[:0:0]
	}
	return out
}

// CheckLeaf reports whether raw has the JSON type of the leaf l: the error says what it must be.
func CheckLeaf(l Leaf, raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	isNull := string(raw) == "null"
	switch l.Kind {
	case LeafString:
		var v string
		if isNull || json.Unmarshal(raw, &v) != nil {
			return errMustBe("a string")
		}
	case LeafNullableString:
		var v *string
		if json.Unmarshal(raw, &v) != nil {
			return errMustBe("a string or null")
		}
	case LeafNumber:
		var v float64
		if isNull || json.Unmarshal(raw, &v) != nil {
			return errMustBe("a number")
		}
	case LeafPrices:
		var v []json.RawMessage
		if isNull || json.Unmarshal(raw, &v) != nil {
			return errMustBe("an array of prices")
		}
		var prices []decision.PriceEntry
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&prices); err != nil {
			return errMustBe("an array of prices {match, provider, input_per_mtok, output_per_mtok}")
		}
	}
	return nil
}

// errMustBe is the type error of a leaf.
type errMustBe string

func (e errMustBe) Error() string { return "must be " + string(e) }

// setPath sets the dotted path of doc to v, creating (or replacing non-object) parents.
func setPath(doc map[string]any, path string, v any) {
	segs := strings.Split(path, ".")
	cur := doc
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = v
}

// sortedPaths are the keys of leaves in Leaves order (unknown paths last, sorted).
func sortedPaths(leaves map[string]json.RawMessage) []string {
	out := make([]string, 0, len(leaves))
	for p := range leaves {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b string) int {
		ia := slices.IndexFunc(Leaves, func(l Leaf) bool { return l.Path == a })
		ib := slices.IndexFunc(Leaves, func(l Leaf) bool { return l.Path == b })
		switch {
		case ia < 0 && ib < 0:
			return strings.Compare(a, b)
		case ia < 0:
			return 1
		case ib < 0:
			return -1
		}
		return ia - ib
	})
	return out
}

// ChangedLeaves lists the leaf paths whose value differs between before and after, in Leaves
// order.
func ChangedLeaves(before, after RuntimeSettings) []string {
	a, b := Flatten(before), Flatten(after)
	var out []string
	for _, l := range Leaves {
		if !bytes.Equal(a[l.Path], b[l.Path]) {
			out = append(out, l.Path)
		}
	}
	return out
}

// RowsWithLeaves returns the settings rows a write of the leaves paths of after stores: each
// touched row is its stored value (rows, as read) with those leaves set to their value in after,
// so a row keeps the fields it held and gains only the ones written. pricing_overrides is whole.
// The other rows are not returned.
func RowsWithLeaves(rows map[string]json.RawMessage, after RuntimeSettings, paths []string) (map[string]json.RawMessage, error) {
	vals := Flatten(after)
	docs := map[string]map[string]any{}
	out := map[string]json.RawMessage{}
	for _, path := range paths {
		if _, ok := LookupLeaf(path); !ok {
			return nil, fmt.Errorf("%s is not a runtime setting", path)
		}
		top, rest, nested := strings.Cut(path, ".")
		if !nested {
			out[top] = vals[path]
			continue
		}
		doc, ok := docs[top]
		if !ok {
			doc = map[string]any{}
			if raw, stored := rows[top]; stored && json.Unmarshal(raw, &doc) != nil {
				doc = map[string]any{} // a stored row that is not an object is replaced
			}
			if doc == nil {
				doc = map[string]any{}
			}
			docs[top] = doc
		}
		setPath(doc, rest, json.RawMessage(vals[path]))
	}
	for top, doc := range docs {
		b, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		out[top] = compact(b)
	}
	return out, nil
}

// StoredPaths lists the leaf paths the stored rows set, in Leaves order: what a settings view
// shows with source "dashboard".
func StoredPaths(rows map[string]json.RawMessage) []string {
	var out []string
	for _, l := range Leaves {
		top, rest, nested := strings.Cut(l.Path, ".")
		raw, ok := rows[top]
		if !ok {
			continue
		}
		if !nested {
			out = append(out, l.Path)
			continue
		}
		if _, ok := lookupRaw(raw, rest); ok {
			out = append(out, l.Path)
		}
	}
	return out
}
