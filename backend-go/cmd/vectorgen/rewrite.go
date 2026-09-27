package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/truthtable/backend-go/internal/decision"
)

// fileEdit lists the top-level members of a vector file to replace; nil fields stay as they are.
type fileEdit struct {
	expected    *any
	generatedBy *string
	status      *string
}

// span is the byte range of a top-level member's value.
type span struct{ start, end int }

// topLevelSpans returns the value span of every top-level member of a JSON object, and the member
// names in document order.
func topLevelSpans(src []byte) (map[string]span, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, errors.New("a vector file must be a JSON object")
	}
	spans := map[string]span{}
	var order []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		end := int(dec.InputOffset())
		spans[key] = span{start: end - len(raw), end: end}
		order = append(order, key)
	}
	return spans, order, nil
}

// rewrite applies edit to a vector file, keeping every other byte. A missing generated_by member
// is inserted after status.
func rewrite(src []byte, edit fileEdit) ([]byte, error) {
	spans, _, err := topLevelSpans(src)
	if err != nil {
		return nil, err
	}
	type change struct {
		at, end int
		text    string
	}
	var changes []change
	replace := func(key, text string) error {
		s, ok := spans[key]
		if !ok {
			return fmt.Errorf("the file has no %q member", key)
		}
		changes = append(changes, change{s.start, s.end, text})
		return nil
	}
	if edit.expected != nil {
		if err := replace("expected", format(*edit.expected, 1)); err != nil {
			return nil, err
		}
	}
	if edit.status != nil {
		if err := replace("status", quote(*edit.status)); err != nil {
			return nil, err
		}
	}
	if edit.generatedBy != nil {
		if _, ok := spans["generated_by"]; ok {
			if err := replace("generated_by", quote(*edit.generatedBy)); err != nil {
				return nil, err
			}
		} else {
			s, ok := spans["status"]
			if !ok {
				return nil, errors.New(`the file has no "status" member`)
			}
			changes = append(changes, change{s.end, s.end, ",\n" + indentUnit + `"generated_by": ` + quote(*edit.generatedBy)})
		}
	}
	// Apply from the end of the file so earlier offsets stay valid.
	slices.SortFunc(changes, func(a, b change) int { return b.at - a.at })
	out := src
	for _, c := range changes {
		out = slices.Concat(out[:c.at], []byte(c.text), out[c.end:])
	}
	if !json.Valid(out) {
		return nil, errors.New("rewrite produced invalid JSON")
	}
	return out, nil
}

// sameJSON reports whether the document raw and the value v are the same JSON value.
func sameJSON(raw json.RawMessage, v any) (bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	a, err := decision.CanonicalizeJSON(raw)
	if err != nil {
		return false, err
	}
	b, err := decision.Canonicalize(v)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}
