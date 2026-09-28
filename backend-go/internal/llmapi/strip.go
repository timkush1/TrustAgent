package llmapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// StripMembers removes the named members from the top level of the JSON object body.
//
// The body is not re-encoded: every member that stays is copied byte for byte, in its original
// order, so what the upstream receives is exactly what the client sent minus the removed members
// (SPEC §2.12 asks for the other fields to stay byte-equivalent; re-marshalling a
// map[string]json.RawMessage would also reorder keys and re-escape <, > and &). Whitespace between
// top-level members is normalized to none. A body with none of the names is returned unchanged,
// and every occurrence of a duplicated name is removed.
func StripMembers(body []byte, names ...string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("llmapi: body is not a JSON object")
	}
	type member struct{ start, end int }
	var kept []member
	removed := false
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
		if slices.Contains(names, key) {
			removed = true
			continue
		}
		kept = append(kept, member{keyStart, end})
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if !removed {
		return body, nil
	}
	out := make([]byte, 0, len(body))
	out = append(out, '{')
	for i, m := range kept {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, body[m.start:m.end]...)
	}
	return append(out, '}'), nil
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
