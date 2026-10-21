package evidence

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/truthtable/contracts"
)

// docRules are the compiled contents of contracts/rules/doc-blocks.v1.json.
type docRules struct {
	tags            []*regexp.Regexp
	attribute       *regexp.Regexp
	labelAttributes []string
	marker          *regexp.Regexp
	mdSection       *regexp.Regexp
	mdHeading       *regexp.Regexp
	lineStart       *regexp.Regexp
	lineEnd         *regexp.Regexp
	lineHeading     *regexp.Regexp
}

var loadDocRules = sync.OnceValues(func() (*docRules, error) {
	b, err := contracts.FS.ReadFile("rules/doc-blocks.v1.json")
	if err != nil {
		return nil, err
	}
	return parseDocRules(b)
})

func parseDocRules(b []byte) (*docRules, error) {
	var f struct {
		TagBlocks struct {
			Patterns        []TagPattern `json:"patterns"`
			Attribute       TagPattern   `json:"attribute"`
			LabelAttributes []string     `json:"label_attributes"`
		} `json:"tag_blocks"`
		MarkerBlocks     TagPattern `json:"marker_blocks"`
		MarkdownSections struct {
			TagPattern
			Heading TagPattern `json:"heading"`
		} `json:"markdown_sections"`
		LineHeaders struct {
			Start   TagPattern `json:"start"`
			End     TagPattern `json:"end"`
			Heading TagPattern `json:"heading"`
		} `json:"line_headers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("doc-blocks.v1.json: %w", err)
	}
	r := &docRules{labelAttributes: f.TagBlocks.LabelAttributes}
	var errs []error
	must := func(p TagPattern) *regexp.Regexp {
		re, err := p.Compile()
		if err != nil {
			errs = append(errs, err)
		}
		return re
	}
	for _, p := range f.TagBlocks.Patterns {
		r.tags = append(r.tags, must(p))
	}
	r.attribute = must(f.TagBlocks.Attribute)
	r.marker = must(f.MarkerBlocks)
	r.mdSection = must(f.MarkdownSections.TagPattern)
	r.mdHeading = must(f.MarkdownSections.Heading)
	r.lineStart = must(f.LineHeaders.Start)
	r.lineEnd = must(f.LineHeaders.End)
	r.lineHeading = must(f.LineHeaders.Heading)
	if len(errs) > 0 {
		return nil, fmt.Errorf("doc-blocks rules: %v", errs)
	}
	return r, nil
}

// extractor applies the rules with one route's settings.
type extractor struct {
	r     *docRules
	s     *Settings
	extra []*regexp.Regexp // route tag_patterns
}

func newExtractor(s *Settings) (*extractor, error) {
	r, err := loadDocRules()
	if err != nil {
		return nil, err
	}
	x := &extractor{r: r, s: s}
	for _, p := range s.TagPatterns {
		re, err := p.Compile()
		if err != nil {
			return nil, fmt.Errorf("evidence tag_patterns %q: %w", p.Pattern, err)
		}
		x.extra = append(x.extra, re)
	}
	return x, nil
}

// block is one document block of a message text. [start, end) is the span removed from the text
// (tag, marker, heading or header line included); text is the block content.
type block struct {
	start, end int
	text       string
	id, label  string
	marker     int
}

func (b block) candidate(defaultLabel string) candidate {
	label := b.label
	if label == "" {
		label = defaultLabel
	}
	return candidate{id: b.id, label: label, text: b.text, marker: b.marker}
}

// blocks finds the document blocks of one message text: built-in tag blocks, route tag patterns,
// marker blocks, markdown sections, then line headers. A block overlapping one already found is
// ignored; the result is in text order.
func (x *extractor) blocks(text string) []block {
	var found []block
	add := func(b block) {
		if slices.ContainsFunc(found, func(f block) bool { return b.start < f.end && f.start < b.end }) {
			return
		}
		found = append(found, b)
	}
	for _, re := range x.r.tags {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			b := block{start: m[0], end: m[1], text: strings.TrimSpace(text[m[4]:m[5]])}
			if m[2] >= 0 {
				b.id, b.label = x.attributes(text[m[2]:m[3]])
			}
			add(b)
		}
	}
	for _, re := range x.extra {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			content := text[m[0]:m[1]]
			for g := len(m)/2 - 1; g >= 1; g-- { // the last group that took part in the match
				if m[2*g] >= 0 {
					content = text[m[2*g]:m[2*g+1]]
					break
				}
			}
			add(block{start: m[0], end: m[1], text: strings.TrimSpace(content)})
		}
	}
	markers := x.r.marker.FindAllStringSubmatchIndex(text, -1)
	for i, m := range markers {
		end := len(text)
		if i+1 < len(markers) {
			end = markers[i+1][0]
		}
		n, _ := strconv.Atoi(text[m[2]:m[3]])
		label := strings.Trim(text[m[0]:m[1]], "[]")
		add(block{start: m[0], end: end, text: strings.TrimSpace(text[m[1]:end]), label: label, marker: n})
	}
	headings := x.r.mdHeading.FindAllStringSubmatchIndex(text, -1)
	for _, m := range x.r.mdSection.FindAllStringSubmatchIndex(text, -1) {
		level := m[3] - m[2]
		end := len(text)
		for _, h := range headings {
			if h[0] > m[0] && h[3]-h[2] <= level {
				end = h[0]
				break
			}
		}
		add(block{start: m[0], end: end, text: strings.TrimSpace(afterLine(text, m[1], end)), label: text[m[4]:m[5]]})
	}
	if x.s.LineHeaders {
		ends := x.r.lineEnd.FindAllStringIndex(text, -1)
		ends = append(ends, x.r.lineHeading.FindAllStringIndex(text, -1)...)
		for _, m := range x.r.lineStart.FindAllStringSubmatchIndex(text, -1) {
			end := len(text)
			for _, e := range ends {
				if e[0] > m[0] && e[0] < end {
					end = e[0]
				}
			}
			add(block{start: m[0], end: end, text: strings.TrimSpace(afterLine(text, m[1], end)), label: text[m[2]:m[3]]})
		}
	}
	slices.SortFunc(found, func(a, b block) int { return a.start - b.start })
	return found
}

// attributes reads a tag's attributes: the item id (a valid id attribute) and the label (the first
// present of the label attributes).
func (x *extractor) attributes(attrs string) (id, label string) {
	values := map[string]string{}
	for _, m := range x.r.attribute.FindAllStringSubmatch(attrs, -1) {
		name := strings.ToLower(m[1])
		if _, dup := values[name]; dup {
			continue
		}
		v := m[2]
		if v == "" {
			v = m[3]
		}
		values[name] = v
	}
	for _, name := range x.r.labelAttributes {
		if v, ok := values[name]; ok && v != "" {
			label = v
			break
		}
	}
	if v := values["id"]; validID.MatchString(v) {
		id = v
	}
	return id, label
}

// afterLine returns text[from:end] without the rest of the line at from (the heading or header
// line the match ended in).
func afterLine(text string, from, end int) string {
	if from > end {
		return ""
	}
	seg := text[from:end]
	if i := strings.IndexByte(seg, '\n'); i >= 0 {
		return seg[i+1:]
	}
	return ""
}

// removeSpans returns text without the blocks' spans.
func removeSpans(text string, blocks []block) string {
	if len(blocks) == 0 {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, bl := range blocks {
		b.WriteString(text[prev:bl.start])
		prev = bl.end
	}
	b.WriteString(text[prev:])
	return b.String()
}
