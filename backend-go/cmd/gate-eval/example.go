package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// evidenceSources are the EvidenceSource values an example's evidence may name.
var evidenceSources = []string{"request_system", "request_context", "tool_results", "extension", "knowledge"}

// defaultEvidenceSource is the source of evidence given as plain strings: text the user message
// carried, as the Python harness injects dataset documents (SPEC §3.2 build_golden).
const defaultEvidenceSource = "request_context"

// exampleLine is one input line (gate-eval example). query/response/context are the Python
// harness's dataset field names; user_request/answer/evidence are accepted as synonyms.
type exampleLine struct {
	ID                 string            `json:"id"`
	Query              *string           `json:"query"`
	UserRequest        *string           `json:"user_request"`
	Response           *string           `json:"response"`
	Answer             *string           `json:"answer"`
	SystemInstructions string            `json:"system_instructions"`
	Context            json.RawMessage   `json:"context"`
	Evidence           json.RawMessage   `json:"evidence"`
	Rules              map[string]string `json:"rules"`
	MaxMarker          int               `json:"max_marker"`
	Scenario           string            `json:"scenario"`
	// Passed through to the output untouched.
	Labels            json.RawMessage `json:"labels"`
	LabelHallucinated *bool           `json:"label_hallucinated"`
	LabelSource       string          `json:"label_source"`
	Domain            string          `json:"domain"`
	Meta              json.RawMessage `json:"meta"`
}

// example is a parsed input line.
type example struct {
	line exampleLine
	// Texts of the exchange.
	UserRequest, Response, SystemInstructions string
	Evidence                                  []decision.EvidenceItem
}

// parseExample decodes one JSONL line strictly: unknown members are an error, so a misspelt field
// cannot silently drop evidence.
func parseExample(b []byte) (*example, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var l exampleLine
	if err := dec.Decode(&l); err != nil {
		// Keep the id when the line has one, so the error can be joined to its example.
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(b, &id) == nil && id.ID != "" {
			return &example{line: exampleLine{ID: id.ID}}, fmt.Errorf("invalid example: %w", err)
		}
		return nil, fmt.Errorf("invalid example: %w", err)
	}
	if dec.More() {
		return nil, errors.New("invalid example: more than one JSON value on the line")
	}
	ex := &example{line: l, SystemInstructions: l.SystemInstructions}
	if strings.TrimSpace(l.ID) == "" {
		return ex, errors.New("invalid example: id is required")
	}
	var err error
	if ex.UserRequest, err = either("query", l.Query, "user_request", l.UserRequest, false); err != nil {
		return ex, err
	}
	if ex.Response, err = either("response", l.Response, "answer", l.Answer, true); err != nil {
		return ex, err
	}
	if l.Context != nil && l.Evidence != nil {
		return ex, errors.New("invalid example: give either context or evidence, not both")
	}
	raw := l.Evidence
	if raw == nil {
		raw = l.Context
	}
	if ex.Evidence, err = parseEvidence(raw); err != nil {
		return ex, err
	}
	if l.MaxMarker < 0 {
		return ex, errors.New("invalid example: max_marker must not be negative")
	}
	return ex, nil
}

// either returns the value of one of two synonymous fields.
func either(a string, av *string, b string, bv *string, required bool) (string, error) {
	switch {
	case av != nil && bv != nil:
		return "", fmt.Errorf("invalid example: give either %s or %s, not both", a, b)
	case av != nil:
		return *av, nil
	case bv != nil:
		return *bv, nil
	case required:
		return "", fmt.Errorf("invalid example: %s is required", a)
	}
	return "", nil
}

// parseEvidence reads the evidence list: strings (documents the user message carried) or items
// {id?, source?, text, untrusted?}. Ids default to doc-<n> (1-based) and sources to
// request_context.
func parseEvidence(raw json.RawMessage) ([]decision.EvidenceItem, error) {
	if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []decision.EvidenceItem{}, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("invalid example: evidence must be a list: %w", err)
	}
	out := make([]decision.EvidenceItem, 0, len(list))
	seen := map[string]bool{}
	for i, r := range list {
		item := decision.EvidenceItem{ID: "doc-" + strconv.Itoa(i+1), Source: defaultEvidenceSource}
		var text string
		if err := json.Unmarshal(r, &text); err == nil {
			item.Text = text
		} else {
			var obj struct {
				ID        *string `json:"id"`
				Source    *string `json:"source"`
				Text      *string `json:"text"`
				Untrusted bool    `json:"untrusted"`
			}
			dec := json.NewDecoder(bytes.NewReader(r))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&obj); err != nil {
				return nil, fmt.Errorf("invalid example: evidence[%d]: want a string or {id, source, text, untrusted}: %w", i, err)
			}
			if obj.Text == nil {
				return nil, fmt.Errorf("invalid example: evidence[%d]: text is required", i)
			}
			item.Text, item.Untrusted = *obj.Text, obj.Untrusted
			if obj.ID != nil && *obj.ID != "" {
				item.ID = *obj.ID
			}
			if obj.Source != nil {
				item.Source = *obj.Source
			}
		}
		if !slices.Contains(evidenceSources, item.Source) {
			return nil, fmt.Errorf("invalid example: evidence[%d]: unknown source %q (want one of %s)", i, item.Source, strings.Join(evidenceSources, ", "))
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("invalid example: evidence[%d]: duplicate id %q", i, item.ID)
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	return out, nil
}

// trusted reports whether any evidence item is trusted (the gate's evidence_present).
func (ex *example) trusted() bool {
	return slices.ContainsFunc(ex.Evidence, func(it decision.EvidenceItem) bool { return !it.Untrusted })
}
