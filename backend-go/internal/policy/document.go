package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// A policy document is decoded twice from one YAML node tree:
//
//   - a structural walk over the typed shape (docYAML) that reports unknown fields, wrong kinds
//     and wrong scalar types with their document path, line and column;
//   - yaml.v3 with KnownFields(true) into the same typed shape, as SPEC §2.7 prescribes (after
//     the walk it only catches what the walk does not model, such as out-of-range integers).
//
// The node tree also becomes the JSON document stored in policy_versions.doc, so the stored doc
// is exactly what was written (no defaults filled in).

// document is a decoded policy document.
type document struct {
	// root is the top-level mapping node (nil for an empty document).
	root *yaml.Node
	// typed is the KnownFields decode.
	typed docYAML
	// doc is the JSON-compatible document as written.
	doc map[string]any
	// fromDoc is set when the YAML was generated from a JSON doc: line and column refer to the
	// generated text, so errors report paths only.
	fromDoc bool
}

// yamlLine matches the "line N:" prefix of yaml.v3 errors.
var yamlLine = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// decodeDocument parses src. It returns the document, or the structural errors.
func decodeDocument(src []byte) (*document, []FieldError) {
	var file yaml.Node
	if err := yaml.Unmarshal(src, &file); err != nil {
		return nil, yamlErrors(err)
	}
	d := &document{}
	if file.Kind == 0 || len(file.Content) == 0 {
		return d, []FieldError{{Path: "version", Line: 1, Col: 1, Message: "version must be 1 (the document is empty)"}}
	}
	root := file.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, []FieldError{{Line: root.Line, Col: root.Column, Message: "the policy must be a YAML mapping"}}
	}
	d.root = root
	w := &walker{}
	w.walk(root, reflect.TypeOf(docYAML{}), "")
	if len(w.errs) > 0 {
		return nil, w.errs
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&d.typed); err != nil && !errors.Is(err, io.EOF) {
		return nil, yamlErrors(err)
	}
	// A second document would be ignored silently (only the first is read): refuse it.
	var extra yaml.Node
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return nil, yamlErrors(err)
	} else if err == nil && !emptyDocument(&extra) {
		n := extra.Content[0]
		return nil, []FieldError{{Line: n.Line, Col: n.Column, Message: "the policy must be a single YAML document: remove the one after ---"}}
	}
	var generic any
	if err := root.Decode(&generic); err != nil {
		return nil, yamlErrors(err)
	}
	doc, err := jsonCompatible(generic)
	if err != nil {
		return nil, []FieldError{{Message: err.Error()}}
	}
	d.doc, _ = doc.(map[string]any)
	return d, nil
}

// emptyDocument reports a document with no content (a trailing "---").
func emptyDocument(doc *yaml.Node) bool {
	if len(doc.Content) == 0 {
		return true
	}
	n := doc.Content[0]
	return len(doc.Content) == 1 && n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" && n.Value == ""
}

// yamlErrors converts a yaml.v3 error into field errors with line numbers.
func yamlErrors(err error) []FieldError {
	var msgs []string
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs = te.Errors
	} else {
		msgs = []string{err.Error()}
	}
	out := make([]FieldError, 0, len(msgs))
	for _, m := range msgs {
		fe := FieldError{Message: strings.TrimPrefix(m, "yaml: ")}
		if sm := yamlLine.FindStringSubmatch(m); sm != nil {
			fe.Line, _ = strconv.Atoi(sm[1])
			fe.Col = 1
			fe.Message = sm[2]
		}
		out = append(out, fe)
	}
	return out
}

// jsonCompatible converts a yaml.v3 generic value into JSON types (map[string]any, []any,
// string, bool, nil, int64 and float64).
func jsonCompatible(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, bool:
		return x, nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case uint64:
		if x > math.MaxInt64 {
			return nil, fmt.Errorf("the number %d is too large", x)
		}
		return int64(x), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.New("NaN and infinite numbers are not allowed")
		}
		return x, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			c, err := jsonCompatible(e)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := jsonCompatible(e)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported value %v", v)
}

// maxWalk bounds the nodes the structural walk visits. Aliases are walked where they are used,
// so a document of nested aliases ("billion laughs") would otherwise take exponential time.
const maxWalk = 200_000

// walker checks a node tree against a Go type shaped by yaml tags.
type walker struct {
	errs    []FieldError
	visited int
}

var optFloatType = reflect.TypeOf(OptFloat{})

func (w *walker) fail(n *yaml.Node, path, format string, args ...any) {
	w.errs = append(w.errs, FieldError{Path: path, Line: n.Line, Col: n.Column, Message: fmt.Sprintf(format, args...)})
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (w *walker) walk(n *yaml.Node, t reflect.Type, path string) {
	w.visited++
	if w.visited > maxWalk {
		if w.visited == maxWalk+1 {
			w.fail(n, path, "the document is too large or expands too many aliases")
		}
		return
	}
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	isNull := n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
	if t == optFloatType {
		if !isNull && !isNumber(n) {
			w.fail(n, path, "must be a number or null")
		}
		return
	}
	if isNull {
		w.fail(n, path, "must not be null")
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			w.fail(n, path, "must be a mapping (object)")
			return
		}
		fields := yamlFields(t)
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			key := k.Value
			if seen[key] {
				w.fail(k, join(path, key), "the key %q is repeated", key)
				continue
			}
			seen[key] = true
			ft, ok := fields[key]
			if !ok {
				w.fail(k, join(path, key), "unknown field %q", key)
				continue
			}
			w.walk(v, ft, join(path, key))
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			w.fail(n, path, "must be a mapping (object)")
			return
		}
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				w.fail(k, path, "keys must be strings")
				continue
			}
			if seen[k.Value] {
				w.fail(k, join(path, k.Value), "the key %q is repeated", k.Value)
				continue
			}
			seen[k.Value] = true
			w.walk(v, t.Elem(), join(path, k.Value))
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			w.fail(n, path, "must be a list")
			return
		}
		for i, item := range n.Content {
			w.walk(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.String:
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
			w.fail(n, path, "must be a string (quote it)")
		}
	case reflect.Bool:
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!bool" {
			w.fail(n, path, "must be true or false")
		}
	case reflect.Int, reflect.Int64:
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" {
			w.fail(n, path, "must be an integer")
		} else if _, err := strconv.ParseInt(strings.ReplaceAll(n.Value, "_", ""), 0, 32); err != nil {
			w.fail(n, path, "the integer %s is out of range", n.Value)
		}
	case reflect.Float64:
		if !isNumber(n) {
			w.fail(n, path, "must be a number")
		}
	default:
		w.fail(n, path, "unsupported value")
	}
}

func isNumber(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
		return false
	}
	switch n.ShortTag() {
	case "!!int":
		return true
	case "!!float":
		f, err := strconv.ParseFloat(n.Value, 64)
		return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return false
}

// yamlFields maps the yaml keys of a struct type (inline fields included) to their types.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if strings.Contains(","+opts+",", ",inline,") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			for k, v := range yamlFields(ft) {
				out[k] = v
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

// seg is one step of a field path: a mapping key or a sequence index.
type seg struct {
	key   string
	index int // -1 for a key
}

// parsePath splits "a.b[2].c" into steps. Keys never contain '.' or '[' in policy documents
// except header names, which only appear as the last step.
func parsePath(p string) []seg {
	var out []seg
	for _, part := range strings.Split(p, ".") {
		key, rest, hasIdx := strings.Cut(part, "[")
		if key != "" {
			out = append(out, seg{key: key, index: -1})
		}
		for hasIdx {
			var idx string
			idx, rest, _ = strings.Cut(rest, "]")
			i, err := strconv.Atoi(idx)
			if err != nil {
				break
			}
			out = append(out, seg{index: i})
			_, rest, hasIdx = strings.Cut(rest, "[")
		}
	}
	return out
}

// find follows path from n. It returns the deepest node reached and whether the whole path
// exists. For a mapping key it returns the value node.
func find(n *yaml.Node, path []seg) (*yaml.Node, bool) {
	cur := n
	for _, s := range path {
		for cur != nil && cur.Kind == yaml.AliasNode && cur.Alias != nil {
			cur = cur.Alias
		}
		if cur == nil {
			return nil, false
		}
		var next *yaml.Node
		switch {
		case s.index >= 0 && cur.Kind == yaml.SequenceNode && s.index < len(cur.Content):
			next = cur.Content[s.index]
		case s.index < 0 && cur.Kind == yaml.MappingNode:
			for i := 0; i+1 < len(cur.Content); i += 2 {
				if cur.Content[i].Value == s.key {
					next = cur.Content[i+1]
					break
				}
			}
		}
		if next == nil {
			return cur, false
		}
		cur = next
	}
	for cur != nil && cur.Kind == yaml.AliasNode && cur.Alias != nil {
		cur = cur.Alias
	}
	return cur, true
}

// mappingValue returns the value node of key in a mapping node, or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	v, ok := find(n, []seg{{key: key, index: -1}})
	if !ok {
		return nil
	}
	return v
}

// yamlFromDoc renders a JSON policy document as block-style YAML, keeping the document's key
// order. The result parses back to the same document.
func yamlFromDoc(doc []byte) (string, error) {
	if !json.Valid(doc) {
		return "", errors.New("doc is not valid JSON")
	}
	var n yaml.Node
	if err := yaml.Unmarshal(doc, &n); err != nil {
		return "", err
	}
	blockStyle(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// blockStyle clears flow and quoting styles so the encoder chooses block style and plain
// scalars where they are unambiguous.
func blockStyle(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		n.Style = 0
	case yaml.ScalarNode:
		if n.Tag == "!!str" || n.ShortTag() == "!!str" {
			n.Style = 0
		}
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}
