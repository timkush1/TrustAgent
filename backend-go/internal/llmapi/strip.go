package llmapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// errNotObject is returned by the member editors for a body that is not a JSON object.
var errNotObject = errors.New("llmapi: body is not a JSON object")

// StripMembers removes the named members from the top level of the JSON object body.
//
// The body is not re-encoded: every member that stays is copied byte for byte, in its original
// order, so what the upstream receives is exactly what the client sent minus the removed members
// (SPEC §2.12 asks for the other fields to stay byte-equivalent; re-marshalling a
// map[string]json.RawMessage would also reorder keys and re-escape <, > and &). Whitespace between
// top-level members is normalized to none. A body with none of the names is returned unchanged,
// and every occurrence of a duplicated name is removed.
func StripMembers(body []byte, names ...string) ([]byte, error) {
	obj, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	if !obj.del(names...) {
		return body, nil
	}
	return obj.bytes(), nil
}

// member is one top-level member of a JSON object: src is `"key": value` exactly as in the
// source (or as built), from the key's opening quote to the end of the value.
type member struct {
	key   string
	value json.RawMessage
	src   []byte
}

// object is a JSON object edited member by member: members that are not touched keep their bytes
// and their order.
type object struct {
	members []member
}

// parseObject splits a JSON object into its members.
func parseObject(body []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errNotObject
	}
	o := &object{}
	for dec.More() {
		keyStart := skipToKey(body, int(dec.InputOffset()))
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		end := int(dec.InputOffset())
		o.members = append(o.members, member{key: key, value: value, src: body[keyStart:end]})
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	return o, nil
}

// get returns the value of key; with duplicates the last one wins, as encoding/json decides.
func (o *object) get(key string) (json.RawMessage, bool) {
	for i := len(o.members) - 1; i >= 0; i-- {
		if o.members[i].key == key {
			return o.members[i].value, true
		}
	}
	return nil, false
}

// set gives key the (valid JSON) value: in place of its first occurrence, removing any duplicate,
// or as a new last member.
func (o *object) set(key string, value []byte) {
	src := append(append(marshal(key), ':'), value...)
	m := member{key: key, value: value, src: src}
	for i := range o.members {
		if o.members[i].key == key {
			o.members[i] = m
			o.members = o.dropAfter(i, key)
			return
		}
	}
	o.members = append(o.members, m)
}

// dropAfter returns the members without the occurrences of key after position i (in place: the
// write position never passes the read position).
func (o *object) dropAfter(i int, key string) []member {
	out := o.members[:i+1]
	for _, m := range o.members[i+1:] {
		if m.key != key {
			out = append(out, m)
		}
	}
	return out
}

// del removes every occurrence of the names and reports whether any was present.
func (o *object) del(names ...string) bool {
	n := len(o.members)
	o.members = slices.DeleteFunc(o.members, func(m member) bool { return slices.Contains(names, m.key) })
	return len(o.members) != n
}

// bytes encodes the object: the members' bytes joined by commas.
func (o *object) bytes() []byte {
	size := 2
	for _, m := range o.members {
		size += len(m.src) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, m := range o.members {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, m.src...)
	}
	return append(out, '}')
}

// joinArray encodes a JSON array of already encoded elements.
func joinArray(elems [][]byte) []byte {
	return append(append([]byte{'['}, bytes.Join(elems, []byte{','})...), ']')
}

// skipToKey returns the offset of the opening quote of the next member key at or after off,
// skipping whitespace and the comma that separates members.
func skipToKey(body []byte, off int) int {
	for off < len(body) {
		switch body[off] {
		case ' ', '\t', '\n', '\r', ',':
			off++
		default:
			return off
		}
	}
	return off
}
