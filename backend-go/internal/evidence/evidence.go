// Package evidence extracts what a gated answer is checked against from the request itself
// (SPEC §2.10, §7.1: pluggable evidence, no RAG by default): document blocks in system and
// developer messages, context the user pasted, tool results, and the trustagent.context extension.
// The rules are data in contracts/rules/doc-blocks.v1.json and are pinned by the
// contracts/vectors/evidence vectors.
//
// Requests of every API format arrive through llmapi.ParseRequest (OpenAI chat and completions,
// Anthropic Messages mapped onto chat messages). The knowledge add-on's documents join the
// request's evidence through Result.AddKnowledge (SPEC §7.1, P4-PLAN D65).
package evidence

import (
	"crypto/sha256"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/glob"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// Evidence sources (EvidenceSource), in the default route priority order.
const (
	SourceRequestSystem  = "request_system"
	SourceRequestContext = "request_context"
	SourceToolResults    = "tool_results"
	SourceExtension      = "extension"
	SourceKnowledge      = "knowledge"
)

// System modes (route evidence.system_mode).
const (
	SystemModeOff           = "off"
	SystemModeDocumentsOnly = "documents_only"
	SystemModeWhole         = "whole"
)

// Labels of items that carry no label of their own.
const (
	LabelSystemPrompt = "system prompt"
	// LabelUserContext marks user-supplied context: it is evidence, but nobody vouches for it.
	LabelUserContext = "user message (not verified as app-supplied)"
	LabelToolResult  = "tool result"
	LabelExtension   = "trustagent.context"
)

// DefaultSources is the route default of evidence.sources.
var DefaultSources = []string{SourceRequestSystem, SourceRequestContext, SourceToolResults, SourceExtension}

// TagPattern is a route evidence.tag_patterns entry: an extra RE2 block pattern with flags from
// "ims".
type TagPattern struct {
	Pattern string `json:"pattern" yaml:"pattern"`
	Flags   string `json:"flags,omitempty" yaml:"flags,omitempty"`
}

// Compile compiles the pattern the way the rules files prescribe ("(?flags)" + pattern).
func (p TagPattern) Compile() (*regexp.Regexp, error) {
	src := p.Pattern
	if p.Flags != "" {
		src = "(?" + p.Flags + ")" + src
	}
	return regexp.Compile(src)
}

// Settings are the route evidence settings extraction reads.
type Settings struct {
	// Sources is the route evidence.sources priority order.
	Sources        []string
	SystemMode     string
	LineHeaders    bool
	TagPatterns    []TagPattern
	UntrustedTools []string
}

// DefaultSettings are the route defaults (SPEC §2.7).
func DefaultSettings() Settings {
	return Settings{Sources: slices.Clone(DefaultSources), SystemMode: SystemModeDocumentsOnly, LineHeaders: true}
}

// Item is one evidence item (api.ts EvidenceItem).
type Item struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Label  string `json:"label"`
	Text   string `json:"text"`
	// Truncated is set by the gate when packing cut the item short.
	Truncated bool `json:"truncated"`
	// Untrusted items (untrusted_tools) are kept for display but never packed and never a detector
	// reference.
	Untrusted bool `json:"untrusted,omitempty"`
}

// Decision returns the item as the decision core sees it.
func (it Item) Decision() decision.EvidenceItem {
	return decision.EvidenceItem{ID: it.ID, Source: it.Source, Text: it.Text, Untrusted: it.Untrusted}
}

// Result is the outcome of extraction.
type Result struct {
	// Items are the deduplicated items in route source order, then message order.
	Items []Item
	// SystemInstructions is the system text without its document blocks.
	SystemInstructions string
	// UserRequest is the final text part of the last user message without blocks.
	UserRequest string
	// PreDedupItems counts the items of configured sources before dedup (untrusted included).
	PreDedupItems int
	// MaxMarker is the highest [Document N] marker number among the items of configured sources,
	// counted before dedup like PreDedupItems.
	MaxMarker int
	// Knowledge is the state of the knowledge retrieval (a Knowledge* state; EvidenceSummary
	// knowledge): "" when none ran, which a route that reads knowledge reports as disabled.
	Knowledge string
}

// DecisionItems returns the items as the decision core sees them.
func (r *Result) DecisionItems() []decision.EvidenceItem {
	out := make([]decision.EvidenceItem, len(r.Items))
	for i, it := range r.Items {
		out[i] = it.Decision()
	}
	return out
}

// Trusted reports whether any item may serve as evidence (untrusted items never do).
func (r *Result) Trusted() bool {
	return slices.ContainsFunc(r.Items, func(it Item) bool { return !it.Untrusted })
}

// LabelKnowledge marks a knowledge-base document that carries no label of its own.
const LabelKnowledge = "Knowledge base"

// HasRequestEvidence reports whether the request itself carries trusted evidence: an item of any
// source but knowledge. A route whose evidence.knowledge.when is fallback retrieves only without it.
func (r *Result) HasRequestEvidence() bool {
	return slices.ContainsFunc(r.Items, func(it Item) bool { return !it.Untrusted && it.Source != SourceKnowledge })
}

// KnowledgeOnly reports whether the trusted evidence came from the knowledge base alone (decision
// meta.knowledge_only): a critical grounding level is then capped to warn.
func (r *Result) KnowledgeOnly() bool {
	return r.Trusted() && !r.HasRequestEvidence()
}

// AddKnowledge joins knowledge-base documents to the evidence (SPEC §7.1, P4-PLAN D65). sources is
// the route evidence.sources: the documents go where knowledge stands in it, after the items of the
// sources before it and before those after it. A document keeps its id when that is a valid
// evidence id no other item uses, else it gets kbN; it is labelled LabelKnowledge when it has no
// label; one whose text another item already carries (white space collapsed) is dropped, so the
// request's own copy wins. Knowledge documents never count in PreDedupItems or MaxMarker: the
// upstream never saw them, so an answer cannot cite them by number. Knowledge becomes used when a
// document joined, else no_hits.
func (r *Result) AddKnowledge(docs []Item, sources []string) {
	seen := map[[sha256.Size]byte]bool{}
	ids := map[string]bool{}
	for _, it := range r.Items {
		seen[sha256.Sum256([]byte(collapseWhitespace(it.Text)))] = true
		ids[it.ID] = true
	}
	var add []Item
	n := 0
	for _, d := range docs {
		text := strings.TrimSpace(d.Text)
		if text == "" {
			continue
		}
		key := sha256.Sum256([]byte(collapseWhitespace(text)))
		if seen[key] {
			continue
		}
		seen[key] = true
		id := d.ID
		for !validID.MatchString(id) || ids[id] {
			n++
			id = idPrefix[SourceKnowledge] + strconv.Itoa(n)
		}
		ids[id] = true
		label := strings.TrimSpace(d.Label)
		if label == "" {
			label = LabelKnowledge
		}
		add = append(add, Item{ID: id, Source: SourceKnowledge, Label: label, Text: text})
	}
	if len(add) == 0 {
		r.Knowledge = KnowledgeNoHits
		return
	}
	r.Knowledge = KnowledgeUsed
	// The items are in route source order: the documents go before the first item of a source
	// that comes after knowledge.
	rank := func(src string) int {
		if i := slices.Index(sources, src); i >= 0 {
			return i
		}
		return len(sources)
	}
	k := rank(SourceKnowledge)
	at := slices.IndexFunc(r.Items, func(it Item) bool { return rank(it.Source) > k })
	if at < 0 {
		at = len(r.Items)
	}
	r.Items = slices.Insert(r.Items, at, add...)
}

var validID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// fallback id prefixes, per source.
var idPrefix = map[string]string{
	SourceRequestSystem:  "sys",
	SourceRequestContext: "ctx",
	SourceToolResults:    "tool",
	SourceExtension:      "ext",
	SourceKnowledge:      "kb",
}

// Extract reads the evidence of an OpenAI chat request. It fails only when a route tag pattern
// does not compile (the policy loader rejects those first).
func Extract(req *llmapi.ChatRequest, s *Settings) (*Result, error) {
	x, err := newExtractor(s)
	if err != nil {
		return nil, err
	}
	return x.chat(req), nil
}

// candidate is an item before ids and dedup.
type candidate struct {
	id, label, text string
	marker          int
	untrusted       bool
}

func (x *extractor) chat(req *llmapi.ChatRequest) *Result {
	res := &Result{}
	perSource := map[string][]candidate{}

	// request_system: system and developer messages.
	var remainders []string
	for i := range req.Messages {
		m := &req.Messages[i]
		if m.Role != "system" && m.Role != "developer" {
			continue
		}
		text := normalizeNewlines(m.Text())
		if x.s.SystemMode == SystemModeWhole {
			perSource[SourceRequestSystem] = append(perSource[SourceRequestSystem], candidate{label: LabelSystemPrompt, text: strings.TrimSpace(text)})
			remainders = append(remainders, text)
			continue
		}
		blocks := x.blocks(text)
		if x.s.SystemMode != SystemModeOff {
			for _, b := range blocks {
				perSource[SourceRequestSystem] = append(perSource[SourceRequestSystem], b.candidate(LabelSystemPrompt))
			}
		}
		remainders = append(remainders, removeSpans(text, blocks))
	}
	res.SystemInstructions = collapseNewlines(strings.Join(remainders, "\n\n"))

	// request_context: blocks in user messages, plus the non-final text parts of the last one.
	last := -1
	for i := range req.Messages {
		if req.Messages[i].Role == "user" {
			last = i
		}
	}
	for i := range req.Messages {
		m := &req.Messages[i]
		if m.Role != "user" {
			continue
		}
		if i != last {
			for _, b := range x.blocks(normalizeNewlines(m.Text())) {
				perSource[SourceRequestContext] = append(perSource[SourceRequestContext], b.candidate(LabelUserContext))
			}
			continue
		}
		if len(m.Parts) == 0 {
			continue
		}
		for _, p := range m.Parts[:len(m.Parts)-1] {
			perSource[SourceRequestContext] = append(perSource[SourceRequestContext], candidate{label: LabelUserContext, text: strings.TrimSpace(p)})
		}
		final := normalizeNewlines(m.Parts[len(m.Parts)-1])
		blocks := x.blocks(final)
		for _, b := range blocks {
			perSource[SourceRequestContext] = append(perSource[SourceRequestContext], b.candidate(LabelUserContext))
		}
		res.UserRequest = strings.TrimSpace(removeSpans(final, blocks))
	}

	// tool_results: role "tool" (named through tool_call_id) and legacy role "function".
	toolNames := map[string]string{}
	for _, m := range req.Messages {
		for _, c := range m.ToolCalls {
			if c.ID != "" {
				toolNames[c.ID] = c.Name
			}
		}
	}
	for _, m := range req.Messages {
		var name string
		switch m.Role {
		case "tool":
			name = toolNames[m.ToolCallID]
		case "function":
			name = m.Name
		default:
			continue
		}
		label := name
		if label == "" {
			label = LabelToolResult
		}
		perSource[SourceToolResults] = append(perSource[SourceToolResults], candidate{
			label: label, text: strings.TrimSpace(m.Text()),
			untrusted: name != "" && glob.Any(x.s.UntrustedTools, name),
		})
	}

	// extension: trustagent.context.
	for _, c := range req.Extension.Context {
		label := c.Source
		if label == "" {
			label = c.ID
		}
		if label == "" {
			label = LabelExtension
		}
		perSource[SourceExtension] = append(perSource[SourceExtension], candidate{id: c.ID, label: label, text: strings.TrimSpace(c.Text)})
	}

	// Ids, then dedup across sources in route order.
	seen := map[[sha256.Size]byte]bool{}
	for _, src := range x.s.Sources {
		cands, ok := perSource[src]
		if !ok {
			continue
		}
		delete(perSource, src) // a source listed twice contributes once
		n := 0
		for _, c := range cands {
			if c.text == "" {
				continue
			}
			n++
			res.PreDedupItems++
			res.MaxMarker = max(res.MaxMarker, c.marker)
			id := c.id
			if !validID.MatchString(id) {
				id = idPrefix[src] + strconv.Itoa(n)
			}
			key := sha256.Sum256([]byte(collapseWhitespace(c.text)))
			if seen[key] {
				continue
			}
			seen[key] = true
			res.Items = append(res.Items, Item{ID: id, Source: src, Label: c.label, Text: c.text, Untrusted: c.untrusted})
		}
	}
	return res
}

var (
	newlineRuns = regexp.MustCompile(`\n{3,}`)
	whitespace  = regexp.MustCompile(`[\t\n\f\r ]+`)
)

// normalizeNewlines converts CRLF and lone CR to LF, before any pattern runs.
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// collapseNewlines collapses runs of three or more newlines to two and trims.
func collapseNewlines(s string) string {
	return strings.TrimSpace(newlineRuns.ReplaceAllString(s, "\n\n"))
}

// collapseWhitespace is the dedup key text: whitespace runs collapsed to one space, trimmed.
func collapseWhitespace(s string) string {
	return strings.TrimSpace(whitespace.ReplaceAllString(s, " "))
}
