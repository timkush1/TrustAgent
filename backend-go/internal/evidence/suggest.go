package evidence

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/llmapi"
)

// Evidence suggestions (SPEC §7.1 "Coverage and suggestions", P4-PLAN D64): a free scan of recent
// answers that were checked against no evidence, looking for document shapes the doc-blocks
// rules do not know (an unknown <tag>, a markdown heading, a "Title:" header line). Each shape
// becomes a candidate route evidence.tag_patterns entry, and would_gain counts the scanned
// requests that would have carried evidence with it, by running the real extraction. The
// trustagent.context extension is always offered too: it is how an app that keeps its documents
// outside the prompt sends them.

// Suggestion kinds (api.ts EvidenceSuggestion.kind).
const (
	SuggestTagPattern = "tag_pattern"
	SuggestLineHeader = "line_header"
	SuggestExtension  = "extension"
)

// Suggestion limits (D64).
const (
	// MaxSuggestScan is the most bodies one scan reads.
	MaxSuggestScan = 500
	// MaxSuggestions is the most suggestions returned, the extension included.
	MaxSuggestions = 10
	// MaxSuggestPattern is the longest pattern suggested, in characters.
	MaxSuggestPattern = 256
	// suggestFlags are the flags a suggested pattern may use.
	suggestFlags = "ims"
	// maxCandidates bounds how many shapes are tried against every body.
	maxCandidates = 20
	// minBlockChars is the least text a block needs to count as a document.
	minBlockChars = 40
)

// ScanMessage is one stored request message (gate.ChatMessage: trace_bodies.messages).
type ScanMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ScanBody is one answer the scan reads: its trace, route and request messages.
type ScanBody struct {
	TraceID  string
	Route    string
	Messages []ScanMessage
}

// SuggestedPattern is api.ts EvidenceSuggestion.pattern.
type SuggestedPattern struct {
	Pattern string `json:"pattern"`
	Flags   string `json:"flags"`
}

// Suggestion is api.ts EvidenceSuggestion.
type Suggestion struct {
	Kind    string            `json:"kind"`
	Pattern *SuggestedPattern `json:"pattern"`
	Label   string            `json:"label"`
	// WouldGain counts the scanned requests that would have carried evidence with the pattern.
	WouldGain int `json:"would_gain"`
	// Sample is how many requests were scanned.
	Sample         int     `json:"sample"`
	ExampleTraceID *string `json:"example_trace_id"`
}

// LabelExtensionSuggestion is the label of the extension suggestion.
const LabelExtensionSuggestion = "Send documents in the trustagent.context extension"

// Shapes the scan looks for.
var (
	tagOpenShape    = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9_.:-]{0,47})(?:[\t\n\f\r ][^<>]*)?>`)
	headingShape    = regexp.MustCompile(`(?m)^#{1,6}[\t\f\r ]+([^\n#][^\n]{0,58}?)[\t\f\r ]*$`)
	colonShape      = regexp.MustCompile(`(?m)^([A-Z][A-Za-z0-9 '&/-]{2,40}):[\t\f\r ]*$`)
	instructionLike = map[string]bool{
		"instruction": true, "instructions": true, "rule": true, "rules": true, "guideline": true, "guidelines": true,
		"persona": true, "role": true, "tone": true, "style": true, "format": true, "output_format": true,
		"output format": true, "output": true, "example": true, "examples": true, "task": true, "constraint": true,
		"constraints": true, "system": true, "thinking": true, "scratchpad": true, "answer": true, "response": true,
		"question": true, "query": true, "user": true, "assistant": true, "goal": true, "goals": true,
	}
)

// shape is one document shape found in the bodies, as a candidate pattern.
type shape struct {
	kind    string
	pattern SuggestedPattern
	label   string
	// bodies counts the bodies the shape appears in; inSystem those where it is in a system or
	// developer message.
	bodies, inSystem int
}

// Suggest scans bodies (most recent first; at most MaxSuggestScan are read) and returns at most
// MaxSuggestions suggestions, the most useful first. settingsOf returns the evidence settings of a
// body's route. It returns no suggestion when there is nothing to scan.
func Suggest(bodies []ScanBody, settingsOf func(route string) Settings) []Suggestion {
	if len(bodies) > MaxSuggestScan {
		bodies = bodies[:MaxSuggestScan]
	}
	if len(bodies) == 0 {
		return []Suggestion{}
	}
	reqs := make([]*llmapi.ChatRequest, len(bodies))
	for i, b := range bodies {
		reqs[i] = chatOf(b.Messages)
	}

	// The shapes, by pattern, with the number of bodies each appears in.
	shapes := map[string]*shape{}
	for _, b := range bodies {
		seen := map[string]bool{}
		for _, m := range b.Messages {
			if m.Role == "assistant" {
				continue
			}
			for _, sh := range shapesOf(normalizeNewlines(m.Content)) {
				key := sh.pattern.Pattern
				if seen[key] {
					continue
				}
				seen[key] = true
				got, ok := shapes[key]
				if !ok {
					got = sh
					shapes[key] = got
				}
				got.bodies++
				if m.Role == "system" || m.Role == "developer" {
					got.inSystem++
				}
			}
		}
	}
	minBodies := min(2, len(bodies))
	ranked := make([]*shape, 0, len(shapes))
	for _, sh := range shapes {
		if sh.bodies >= minBodies {
			ranked = append(ranked, sh)
		}
	}
	slices.SortFunc(ranked, func(a, b *shape) int {
		return cmp.Or(cmp.Compare(b.bodies, a.bodies), cmp.Compare(a.pattern.Pattern, b.pattern.Pattern))
	})
	if len(ranked) > maxCandidates {
		ranked = ranked[:maxCandidates]
	}

	// What each body yields today, per route settings.
	base := map[string]*extractor{}
	extractorFor := func(cache map[string]*extractor, route string, extra *TagPattern) *extractor {
		if x, ok := cache[route]; ok {
			return x
		}
		s := settingsOf(route)
		if extra != nil {
			s.TagPatterns = append(slices.Clone(s.TagPatterns), *extra)
		}
		x, err := newExtractor(&s)
		if err != nil {
			x = nil // a route pattern that does not compile: the route cannot be scanned
		}
		cache[route] = x
		return x
	}
	had := make([]bool, len(bodies))
	for i, b := range bodies {
		if x := extractorFor(base, b.Route, nil); x != nil {
			had[i] = x.chat(reqs[i]).Trusted()
		} else {
			had[i] = true // unknown: never credit a suggestion with it
		}
	}

	out := []Suggestion{}
	for _, sh := range ranked {
		tp := TagPattern{Pattern: sh.pattern.Pattern, Flags: sh.pattern.Flags}
		if !ValidSuggestion(tp) {
			continue
		}
		cache := map[string]*extractor{}
		s := Suggestion{Kind: sh.kind, Pattern: &sh.pattern, Label: sh.label, Sample: len(bodies)}
		if sh.inSystem*2 >= sh.bodies {
			s.Label += " in the system prompt"
		} else {
			s.Label += " in user messages"
		}
		for i, b := range bodies {
			if had[i] {
				continue
			}
			x := extractorFor(cache, b.Route, &tp)
			if x == nil || !x.chat(reqs[i]).Trusted() {
				continue
			}
			s.WouldGain++
			if s.ExampleTraceID == nil {
				id := b.TraceID
				s.ExampleTraceID = &id
			}
		}
		if s.WouldGain > 0 {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b Suggestion) int { return cmp.Compare(b.WouldGain, a.WouldGain) })
	if len(out) > MaxSuggestions-1 {
		out = out[:MaxSuggestions-1]
	}
	return append(out, Suggestion{Kind: SuggestExtension, Label: LabelExtensionSuggestion, Sample: len(bodies)})
}

// ValidSuggestion reports whether p may be suggested (D64): an RE2 pattern of at most 256
// characters with flags within "ims".
func ValidSuggestion(p TagPattern) bool {
	if p.Pattern == "" || utf8.RuneCountInString(p.Pattern) > MaxSuggestPattern {
		return false
	}
	for _, f := range p.Flags {
		if !strings.ContainsRune(suggestFlags, f) {
			return false
		}
	}
	_, err := p.Compile()
	return err == nil
}

// chatOf is the chat request of stored messages (each content is one text part).
func chatOf(msgs []ScanMessage) *llmapi.ChatRequest {
	req := &llmapi.ChatRequest{Messages: make([]llmapi.Message, 0, len(msgs))}
	for _, m := range msgs {
		req.Messages = append(req.Messages, llmapi.Message{Role: m.Role, Parts: []string{m.Content}, Name: m.Name})
	}
	return req
}

// shapesOf finds the document shapes of one message text: unknown tags wrapping at least
// minBlockChars of text, markdown headings and "Title:" header lines followed by that much text.
// Shapes named like instructions (rules, examples, persona, ...) are left out: they hold what the
// app tells the model, not documents to check answers against. Each shape is returned once.
//
// The text is request content any client sends, so the scan stays linear in its length whatever
// it holds: the closing tags and the section ends are indexed in one pass each, and every block
// test is a constant-time lookup (an earlier version searched the rest of the text per tag or
// header, which took seconds per body for text made of many distinct unclosed tags).
func shapesOf(text string) []*shape {
	var out []*shape
	seen := map[string]bool{}
	add := func(sh *shape) {
		if !seen[sh.pattern.Pattern] {
			seen[sh.pattern.Pattern] = true
			out = append(out, sh)
		}
	}
	ix := newTextIndex(text)
	closes := closingTags(text)
	for _, m := range tagOpenShape.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		key := strings.ToLower(name) // tag names are ASCII (tagOpenShape)
		if instructionLike[key] {
			continue
		}
		at := firstAtOrAfter(closes[key], m[1])
		if at < 0 || !ix.block(m[1], at) {
			continue
		}
		q := regexp.QuoteMeta(name)
		add(&shape{kind: SuggestTagPattern,
			pattern: SuggestedPattern{Pattern: "<" + q + `(?:[\t\n\f\r ][^>]*)?>(.*?)</` + q + ">", Flags: "is"},
			label:   "<" + name + "> blocks"})
	}
	headingEnds := occurrences(text, "\n#")
	for _, m := range headingShape.FindAllStringSubmatchIndex(text, -1) {
		title := strings.TrimSpace(text[m[2]:m[3]])
		if title == "" || instructionLike[strings.ToLower(title)] || !ix.block(m[1], sectionEnd(headingEnds, m[1], len(text))) {
			continue
		}
		add(&shape{kind: SuggestLineHeader,
			pattern: SuggestedPattern{Pattern: `^#{1,6}[\t\f\r ]*` + regexp.QuoteMeta(title) + `[\t\f\r ]*\n(.*?)(?:\n#{1,6}[\t\f\r ]|\z)`, Flags: "ims"},
			label:   fmt.Sprintf("%q sections", "## "+title)})
	}
	paragraphEnds := occurrences(text, "\n\n")
	for _, m := range colonShape.FindAllStringSubmatchIndex(text, -1) {
		title := text[m[2]:m[3]]
		if instructionLike[strings.ToLower(title)] || !ix.block(m[1], sectionEnd(paragraphEnds, m[1], len(text))) {
			continue
		}
		add(&shape{kind: SuggestLineHeader,
			pattern: SuggestedPattern{Pattern: `^` + regexp.QuoteMeta(title) + `:[\t\f\r ]*\n(.*?)(?:\n[\t\f\r ]*\n|\z)`, Flags: "ims"},
			label:   fmt.Sprintf("%q sections", title+":")})
	}
	return out
}

// closeTagShape is a closing tag with a name tagOpenShape accepts.
var closeTagShape = regexp.MustCompile(`</([A-Za-z][A-Za-z0-9_.:-]{0,47})>`)

// closingTags indexes the closing tags of text: per lower-cased name, the byte offsets where they
// start, ascending. Closing tags cannot overlap, so one pass finds them all.
func closingTags(text string) map[string][]int {
	out := map[string][]int{}
	for _, m := range closeTagShape.FindAllStringSubmatchIndex(text, -1) {
		key := strings.ToLower(text[m[2]:m[3]])
		out[key] = append(out[key], m[0])
	}
	return out
}

// occurrences are the byte offsets of every occurrence of sub (two bytes or more) in text,
// overlapping ones included, ascending.
func occurrences(text, sub string) []int {
	var out []int
	for from := 0; ; {
		i := strings.Index(text[from:], sub)
		if i < 0 {
			return out
		}
		out = append(out, from+i)
		from += i + 1
	}
}

// firstAtOrAfter is the first offset of sorted that is at least from, or -1.
func firstAtOrAfter(sorted []int, from int) int {
	if i, _ := slices.BinarySearch(sorted, from); i < len(sorted) {
		return sorted[i]
	}
	return -1
}

// sectionEnd is where the section that starts at offset from ends: at the first of stops after
// from (a stop at from itself belongs to the line before), else at the end of the text.
func sectionEnd(stops []int, from, end int) int {
	if at := firstAtOrAfter(stops, from+1); at >= 0 {
		return at
	}
	return end
}

// textIndex answers in constant time whether a span of a text holds a block: at least
// minBlockChars bytes once the ASCII white space around it is trimmed.
type textIndex struct {
	// next[i] is the offset of the first byte at or after i that is not ASCII white space
	// (len(text) when there is none).
	next []int32
}

func newTextIndex(text string) *textIndex {
	next := make([]int32, len(text)+1)
	next[len(text)] = int32(len(text)) // #nosec G115 -- a message is far below 2 GiB
	for i := len(text) - 1; i >= 0; i-- {
		switch text[i] {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			next[i] = next[i+1]
		default:
			next[i] = int32(i) // #nosec G115 -- a message is far below 2 GiB
		}
	}
	return &textIndex{next: next}
}

// block reports whether text[from:to] holds at least minBlockChars bytes once trimmed: its first
// non-space byte f is before to, and so is a non-space byte at f+minBlockChars-1 or later.
func (ix *textIndex) block(from, to int) bool {
	f := int(ix.next[from])
	last := f + minBlockChars - 1
	return f < to && last < to && int(ix.next[last]) < to
}
