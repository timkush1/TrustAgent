package decision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
)

// jsObject is a JSON object whose members keep the order JavaScript gives them, so Go reports the
// lints of a spec in the same order and with the same paths as lint.ts. Object.keys and
// Object.entries list array-index keys ("0", "1", ...) first in ascending numeric order, then the
// other keys in insertion order. JSON.parse keeps a repeated key at its first position with its
// last value.
type jsObject struct {
	keys []string // insertion order of first occurrence
	vals map[string]any
}

func (o *jsObject) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *jsObject) has(key string) bool {
	_, ok := o.vals[key]
	return ok
}

// entries returns the keys in JavaScript property order.
func (o *jsObject) entries() []string {
	var index, other []string
	for _, k := range o.keys {
		if isArrayIndex(k) {
			index = append(index, k)
		} else {
			other = append(other, k)
		}
	}
	slices.SortFunc(index, func(a, b string) int {
		x, _ := strconv.ParseUint(a, 10, 32)
		y, _ := strconv.ParseUint(b, 10, 32)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	return append(index, other...)
}

// isArrayIndex reports whether k is an ECMAScript array index: the canonical decimal form of an
// integer in [0, 2^32 - 2].
func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 1<<32-1
}

// decodeOrdered decodes a JSON document into strings, json.Numbers, bools, nil, []any and
// *jsObject values.
func decodeOrdered(doc []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	return readOrdered(dec)
}

func readOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := readOrdered(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token() // ']'
		return arr, err
	case '{':
		obj := &jsObject{vals: map[string]any{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, ok := kt.(string)
			if !ok {
				return nil, fmt.Errorf("json: object key %v is not a string", kt)
			}
			v, err := readOrdered(dec)
			if err != nil {
				return nil, err
			}
			if _, seen := obj.vals[k]; !seen {
				obj.keys = append(obj.keys, k)
			}
			obj.vals[k] = v
		}
		_, err := dec.Token() // '}'
		return obj, err
	}
	return nil, fmt.Errorf("json: unexpected delimiter %v", d)
}
