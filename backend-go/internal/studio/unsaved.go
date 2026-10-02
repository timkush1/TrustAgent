package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/truthtable/backend-go/internal/decision"
)

// DraftRefMarker separates an unsaved spec's id from its hash in studio_results.evaluator_ref.
const DraftRefMarker = "@draft:"

// Unsaved is an unsaved Studio spec as a test or a dry-run runs it (D58).
type Unsaved struct {
	// Spec is the spec compiled as <id>@<latest+1>: the version a save would create.
	Spec *decision.Spec
	// ResultRef is what studio_results.evaluator_ref records: "<id>@draft:<spec_sha256[:12]>",
	// the hash being that of Spec. Studio never writes evaluator_versions.
	ResultRef string
}

// CompileUnsaved prepares an unsaved spec document (already validated) for a run: its version
// becomes latest+1, where latest is the newest stored version of its id (0 for a new id). Only
// the top-level version member changes; every other member keeps its bytes and order.
func CompileUnsaved(doc []byte, latest int) (*Unsaved, error) {
	if latest < 0 {
		return nil, fmt.Errorf("studio: latest version %d is negative", latest)
	}
	next, err := withMember(doc, "version", []byte(strconv.Itoa(latest+1)))
	if err != nil {
		return nil, err
	}
	spec, err := decision.ParseSpec(next)
	if err != nil {
		return nil, err
	}
	h, err := spec.Hashes()
	if err != nil {
		return nil, err
	}
	return &Unsaved{Spec: spec, ResultRef: spec.ID + DraftRefMarker + h.SpecSHA256[:12]}, nil
}

// withMember returns the JSON object doc with its top-level member key set to value (appended
// when absent), keeping every other member's bytes and order.
func withMember(doc []byte, key string, value json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("studio: the spec is not a JSON object")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	found := false
	write := func(k string, v []byte) {
		if out.Len() > 1 {
			out.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(v)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("studio: the spec is not valid JSON: %w", err)
		}
		k, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("studio: the spec is not valid JSON: %w", err)
		}
		if k == key {
			if found {
				continue // a repeated member: the last one would win, so keep one, set
			}
			raw, found = value, true
		}
		write(k, raw)
	}
	if !found {
		write(key, value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
