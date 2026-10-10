package decision

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/truthtable/contracts"
)

// The Tier-0 detectors (SPEC §2.10): deterministic checks of the gated response against the text the
// request carried. Their patterns and normalizations are data in contracts/rules/detectors.v1.json
// and contracts/rules/secrets.v1.json; this file is the algorithm around them, pinned by the
// contracts/vectors/detectors vectors.

// DetectorIDs lists every detector in the order decision/v1 records them.
var DetectorIDs = []string{DetNumbers, DetDates, DetURLs, DetQuotes, DetCitation, DetSecret, DetPromptLeak}

// DefaultMinInteger is the route default of detectors.min_integer.
const DefaultMinInteger = 10

// DetectorFinding is one span of the response a detector objects to. Start and End are UTF-16 code
// unit offsets into the response, half-open; -1 means unaligned.
type DetectorFinding struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// DetectorResult is one detector's outcome (decision/v1 DetectorResult).
type DetectorResult struct {
	ID       string            `json:"id"`
	Ran      bool              `json:"ran"`
	Fired    bool              `json:"fired"`
	Findings []DetectorFinding `json:"findings"`
}

// DetectorInput is what the detectors read.
type DetectorInput struct {
	// Response is the gated response text.
	Response string
	// Evidence is the extracted, pre-packing evidence. Untrusted items are never a reference.
	Evidence []EvidenceItem
	// UserRequest, SystemInstructions and the values of Rules complete the reference text.
	UserRequest        string
	SystemInstructions string
	Rules              map[string]string
	// PreDedupItems and MaxMarker bound citation numbers: the evidence item count before dedup and
	// the highest [Document N] marker number (SPEC §2.10 citation_out_of_range).
	PreDedupItems int
	MaxMarker     int
	// MinInteger is the route detectors.min_integer (DefaultMinInteger unless the route sets it).
	MinInteger int
}

// Detect runs every detector, in DetectorIDs order.
func Detect(in *DetectorInput) ([]DetectorResult, error) {
	r, err := loadDetectorRules()
	if err != nil {
		return nil, err
	}
	d := newDetection(r, in)
	out := make([]DetectorResult, 0, len(DetectorIDs))
	for _, id := range DetectorIDs {
		out = append(out, d.run(id))
	}
	return out, nil
}

// DetectOne runs a single detector.
func DetectOne(id string, in *DetectorInput) (DetectorResult, error) {
	if !slices.Contains(DetectorIDs, id) {
		return DetectorResult{}, fmt.Errorf("detectors: unknown detector %q", id)
	}
	r, err := loadDetectorRules()
	if err != nil {
		return DetectorResult{}, err
	}
	return newDetection(r, in).run(id), nil
}

// DetectorStates is the decide() view of detector results: whether each ran and fired.
func DetectorStates(results []DetectorResult) map[string]DetectorState {
	out := make(map[string]DetectorState, len(results))
	for _, r := range results {
		out[r.ID] = DetectorState{Ran: r.Ran, Fired: r.Fired}
	}
	return out
}

// detectorRules are the compiled contents of the rules files.
type detectorRules struct {
	number, listMarker, windowToken, unit, numberSuffix *regexp.Regexp
	currency                                            []string
	windowTokens                                        int
	numberRemove                                        []string
	// numberScan and urlScan scan reference texts in runs (refscan.go, D167).
	numberScan, urlScan *refScan

	dates  []datePattern
	months map[string]int

	url          *regexp.Regexp
	urlStripTail string

	quotes          []*regexp.Regexp
	quoteMinWords   int
	quoteStripEdges string

	citations []*regexp.Regexp

	secrets []*regexp.Regexp

	quoteMap   map[string]string
	whitespace *regexp.Regexp
	// wsRun is the whitespace pattern's byte set when it is one ASCII class repeated (D167):
	// normalizeText then collapses runs in a byte loop instead of the regexp.
	wsRun     [256]bool
	wsRunFast bool

	leakMin, leakWindow, leakStride int
}

type datePattern struct {
	re                *regexp.Regexp
	year, month, day  int // capture group numbers; day is 0 for month_year
	monthIsName, full bool
	window            int // the most bytes a match spans; 0 scans whole texts (datescan.go)
}

// rulePattern is a pattern with separately passed flags, as the rules files write them.
type rulePattern struct {
	Pattern string `json:"pattern"`
	Flags   string `json:"flags"`
}

func (p rulePattern) compile() (*regexp.Regexp, error) {
	src := p.Pattern
	if p.Flags != "" {
		src = "(?" + p.Flags + ")" + src
	}
	return regexp.Compile(src)
}

var loadDetectorRules = sync.OnceValues(func() (*detectorRules, error) {
	det, err := contracts.FS.ReadFile("rules/detectors.v1.json")
	if err != nil {
		return nil, err
	}
	sec, err := contracts.FS.ReadFile("rules/secrets.v1.json")
	if err != nil {
		return nil, err
	}
	return parseDetectorRules(det, sec)
})

func parseDetectorRules(detJSON, secJSON []byte) (*detectorRules, error) {
	var f struct {
		TextNormalization struct {
			QuoteMap   map[string]string `json:"quote_map"`
			Whitespace rulePattern       `json:"whitespace"`
		} `json:"text_normalization"`
		Detectors struct {
			Numbers struct {
				Number    rulePattern `json:"number"`
				Normalize struct {
					RemoveChars []string    `json:"remove_chars"`
					StripSuffix rulePattern `json:"strip_suffix"`
				} `json:"normalize"`
				SkipListMarkers rulePattern `json:"skip_list_markers"`
				SmallIntegers   struct {
					CurrencyPrefix []string    `json:"currency_prefix"`
					WindowTokens   int         `json:"window_tokens"`
					WindowToken    rulePattern `json:"window_token"`
					Unit           rulePattern `json:"unit"`
				} `json:"small_integers"`
			} `json:"numbers_not_in_evidence"`
			Dates struct {
				Patterns []struct {
					rulePattern
					ID     string         `json:"id"`
					Groups map[string]int `json:"groups"`
				} `json:"patterns"`
				Months map[string]int `json:"months"`
			} `json:"dates_not_in_evidence"`
			URLs struct {
				URL       rulePattern `json:"url"`
				Normalize struct {
					StripTrailing string `json:"strip_trailing"`
				} `json:"normalize"`
			} `json:"urls_not_in_evidence"`
			Quotes struct {
				Quotes     []rulePattern `json:"quotes"`
				MinWords   int           `json:"min_words"`
				StripEdges string        `json:"strip_edges"`
			} `json:"quotes_not_in_evidence"`
			Citation struct {
				Citations []rulePattern `json:"citations"`
			} `json:"citation_out_of_range"`
			Leak struct {
				MinCodePoints    int `json:"min_code_points"`
				WindowCodePoints int `json:"window_code_points"`
				StrideCodePoints int `json:"stride_code_points"`
			} `json:"system_prompt_leak"`
		} `json:"detectors"`
	}
	if err := json.Unmarshal(detJSON, &f); err != nil {
		return nil, fmt.Errorf("detectors.v1.json: %w", err)
	}
	var s struct {
		Patterns []rulePattern `json:"patterns"`
	}
	if err := json.Unmarshal(secJSON, &s); err != nil {
		return nil, fmt.Errorf("secrets.v1.json: %w", err)
	}

	r := &detectorRules{}
	var errs []error
	must := func(p rulePattern) *regexp.Regexp {
		re, err := p.compile()
		if err != nil {
			errs = append(errs, err)
		}
		return re
	}
	d := f.Detectors
	r.number = must(d.Numbers.Number)
	r.numberRemove = d.Numbers.Normalize.RemoveChars
	r.numberSuffix = must(d.Numbers.Normalize.StripSuffix)
	r.listMarker = must(d.Numbers.SkipListMarkers)
	r.currency = d.Numbers.SmallIntegers.CurrencyPrefix
	r.windowTokens = d.Numbers.SmallIntegers.WindowTokens
	r.windowToken = must(d.Numbers.SmallIntegers.WindowToken)
	r.unit = must(d.Numbers.SmallIntegers.Unit)
	for _, p := range d.Dates.Patterns {
		dp := datePattern{year: p.Groups["year"], month: p.Groups["month"], day: p.Groups["day"]}
		var err error
		if dp.re, dp.window, err = compileDatePattern(p.rulePattern, dp.year); err != nil {
			errs = append(errs, err)
		}
		dp.full = dp.day > 0
		dp.monthIsName = p.ID != "iso"
		r.dates = append(r.dates, dp)
	}
	r.months = d.Dates.Months
	r.url = must(d.URLs.URL)
	if r.number != nil && r.url != nil {
		r.numberScan, r.urlScan = newRefScan(r.number), newRefScan(r.url)
	}
	r.urlStripTail = d.URLs.Normalize.StripTrailing
	for _, q := range d.Quotes.Quotes {
		r.quotes = append(r.quotes, must(q))
	}
	r.quoteMinWords = d.Quotes.MinWords
	r.quoteStripEdges = d.Quotes.StripEdges
	for _, c := range d.Citation.Citations {
		r.citations = append(r.citations, must(c))
	}
	for _, p := range s.Patterns {
		r.secrets = append(r.secrets, must(p))
	}
	r.quoteMap = f.TextNormalization.QuoteMap
	r.whitespace = must(f.TextNormalization.Whitespace)
	if r.whitespace != nil {
		r.wsRun, r.wsRunFast = asciiClassRun(r.whitespace)
	}
	r.leakMin = d.Leak.MinCodePoints
	r.leakWindow = d.Leak.WindowCodePoints
	r.leakStride = d.Leak.StrideCodePoints
	if len(errs) > 0 {
		return nil, fmt.Errorf("detector rules: %v", errs)
	}
	if r.leakWindow <= 0 || r.leakStride <= 0 || r.quoteMinWords <= 0 || r.windowTokens < 0 {
		return nil, fmt.Errorf("detector rules: invalid sizes")
	}
	return r, nil
}

// detection is one run of the detectors over one input.
type detection struct {
	r        *detectorRules
	in       *DetectorInput
	resp     string
	u16      []int    // UTF-16 offset of every byte offset of resp
	refParts []string // the reference text, one part per source text
	evidence bool     // trusted evidence is present
}

func newDetection(r *detectorRules, in *DetectorInput) *detection {
	d := &detection{r: r, in: in, resp: in.Response, u16: utf16Index(in.Response)}
	for _, it := range in.Evidence {
		if it.Untrusted {
			continue
		}
		d.evidence = true
		d.refParts = append(d.refParts, it.Text)
	}
	d.refParts = append(d.refParts, in.UserRequest, in.SystemInstructions)
	keys := make([]string, 0, len(in.Rules))
	for k := range in.Rules {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		d.refParts = append(d.refParts, in.Rules[k])
	}
	return d
}

func (d *detection) run(id string) DetectorResult {
	res := DetectorResult{ID: id, Findings: []DetectorFinding{}}
	switch id {
	case DetNumbers:
		res.Ran = d.evidence || len(d.in.Rules) > 0
		if res.Ran {
			res.Findings = d.numbers()
		}
	case DetDates:
		res.Ran = d.evidence || len(d.in.Rules) > 0
		if res.Ran {
			res.Findings = d.dates()
		}
	case DetURLs:
		res.Ran = d.evidence
		if res.Ran {
			res.Findings = d.urls()
		}
	case DetQuotes:
		res.Ran = d.evidence
		if res.Ran {
			res.Findings = d.quotes()
		}
	case DetCitation:
		res.Ran = d.evidence
		if res.Ran {
			res.Findings = d.citations()
		}
	case DetSecret:
		res.Ran = true
		res.Findings = d.secrets()
	case DetPromptLeak:
		var leaked string
		res.Ran, leaked = d.promptLeak()
		if leaked != "" {
			res.Findings = []DetectorFinding{{Text: leaked, Start: -1, End: -1}}
		}
	}
	res.Fired = len(res.Findings) > 0
	return res
}

func (d *detection) finding(text string, start, end int) DetectorFinding {
	return DetectorFinding{Text: text, Start: d.u16[start], End: d.u16[end]}
}

// numbers_not_in_evidence

func (d *detection) normNumber(raw string) string {
	for _, c := range d.r.numberRemove {
		raw = strings.ReplaceAll(raw, c, "")
	}
	// Every match of the suffix pattern begins with its literal prefix ("." for "\.0+$"), so a
	// number without one is left as it is without running the pattern (D167).
	if lit, _ := d.r.numberSuffix.LiteralPrefix(); lit != "" && !strings.Contains(raw, lit) {
		return raw
	}
	return d.r.numberSuffix.ReplaceAllString(raw, "")
}

func (d *detection) numbers() []DetectorFinding {
	// Only the response's numbers can fire, so the reference text is read only when one of them
	// is checked (D167), and then in runs (refscan.go).
	type checked struct {
		raw, norm  string
		start, end int
	}
	markers := d.r.listMarker.FindAllStringIndex(d.resp, -1)
	var todo []checked
	for _, m := range d.r.number.FindAllStringIndex(d.resp, -1) {
		if startsInside(markers, m[0]) {
			continue
		}
		raw := d.resp[m[0]:m[1]]
		norm := d.normNumber(raw)
		if d.smallInteger(norm) && !d.unitAdjacent(raw, m) {
			continue
		}
		todo = append(todo, checked{raw: raw, norm: norm, start: m[0], end: m[1]})
	}
	if len(todo) == 0 {
		return orEmpty(nil)
	}
	known := make(map[string]bool, len(todo)) // the checked numbers, true once the reference has one
	for _, c := range todo {
		known[c.norm] = false
	}
	for _, p := range d.refParts {
		d.r.numberScan.each(p, func(start, end int) {
			norm := d.normNumber(p[start:end])
			if seen, checked := known[norm]; checked && !seen {
				known[norm] = true
			}
		})
	}
	var out []DetectorFinding
	for _, c := range todo {
		if !known[c.norm] {
			out = append(out, d.finding(c.raw, c.start, c.end))
		}
	}
	return orEmpty(out)
}

// smallInteger reports a normalized number without a fraction whose value is <= min_integer.
func (d *detection) smallInteger(norm string) bool {
	if strings.Contains(norm, ".") {
		return false
	}
	v, err := strconv.ParseUint(norm, 10, 64)
	return err == nil && d.in.MinInteger >= 0 && v <= uint64(d.in.MinInteger)
}

// unitAdjacent reports a number that must be checked although it is small: a percentage, a number
// right after a currency sign, or one followed within window_tokens tokens by a unit.
func (d *detection) unitAdjacent(raw string, m []int) bool {
	if strings.HasSuffix(raw, "%") {
		return true
	}
	if prev, _ := utf8.DecodeLastRuneInString(d.resp[:m[0]]); m[0] > 0 && slices.Contains(d.r.currency, string(prev)) {
		return true
	}
	for _, tok := range d.r.windowToken.FindAllString(d.resp[m[1]:], d.r.windowTokens) {
		if d.r.unit.MatchString(tok) {
			return true
		}
	}
	return false
}

// dates_not_in_evidence

type foundDate struct {
	start, end int
	norm       string
}

// findDates applies the date patterns in order; a match overlapping a date already found in the
// same text is ignored, as is one with an impossible month or day.
func (d *detection) findDates(text string) []foundDate {
	var found []foundDate
	for _, p := range d.r.dates {
		for _, m := range p.findAll(text) {
			if slices.ContainsFunc(found, func(f foundDate) bool { return m[0] < f.end && f.start < m[1] }) {
				continue
			}
			norm, ok := d.normDate(text, p, m)
			if ok {
				found = append(found, foundDate{start: m[0], end: m[1], norm: norm})
			}
		}
	}
	slices.SortFunc(found, func(a, b foundDate) int { return a.start - b.start })
	return found
}

func (d *detection) normDate(text string, p datePattern, m []int) (string, bool) {
	group := func(g int) string { return text[m[2*g]:m[2*g+1]] }
	year, err := strconv.Atoi(group(p.year))
	if err != nil {
		return "", false
	}
	var month int
	if p.monthIsName {
		month = d.r.months[strings.ToLower(group(p.month))]
	} else if month, err = strconv.Atoi(group(p.month)); err != nil {
		return "", false
	}
	if month < 1 || month > 12 {
		return "", false
	}
	if !p.full {
		return fmt.Sprintf("%04d-%02d", year, month), true
	}
	day, err := strconv.Atoi(group(p.day))
	if err != nil || day < 1 || day > 31 {
		return "", false
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day), true
}

func (d *detection) dates() []DetectorFinding {
	// Only the response's dates can fire, so a response without one never reads the reference
	// text (the costly part on long evidence, D161).
	found := d.findDates(d.resp)
	if len(found) == 0 {
		return orEmpty(nil)
	}
	var known []string
	for _, p := range d.refParts {
		for _, f := range d.findDates(p) {
			known = append(known, f.norm)
		}
	}
	var out []DetectorFinding
	for _, f := range found {
		ok := slices.ContainsFunc(known, func(k string) bool {
			return k == f.norm || (len(f.norm) == len("YYYY-MM") && strings.HasPrefix(k, f.norm+"-"))
		})
		if !ok {
			out = append(out, d.finding(d.resp[f.start:f.end], f.start, f.end))
		}
	}
	return orEmpty(out)
}

// urls_not_in_evidence

// stripURL removes trailing punctuation; normURL also lowercases the scheme and the authority.
func (d *detection) stripURL(u string) string {
	return strings.TrimRight(u, d.r.urlStripTail)
}

func normURL(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	rest := u[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	return strings.ToLower(u[:i+3+end]) + rest[end:]
}

func (d *detection) urls() []DetectorFinding {
	// The reference text is read only when the response holds a URL (D167).
	found := d.r.url.FindAllStringIndex(d.resp, -1)
	if len(found) == 0 {
		return orEmpty(nil)
	}
	known := map[string]bool{}
	for _, p := range d.refParts {
		d.r.urlScan.each(p, func(start, end int) { known[normURL(d.stripURL(p[start:end]))] = true })
	}
	var out []DetectorFinding
	for _, m := range found {
		u := d.stripURL(d.resp[m[0]:m[1]])
		if u == "" || known[normURL(u)] {
			continue
		}
		out = append(out, d.finding(u, m[0], m[0]+len(u)))
	}
	return orEmpty(out)
}

// quotes_not_in_evidence

// normalizeText is the rules' text_normalization: curly quotes to straight ones, ASCII A-Z
// lowercased, every whitespace run collapsed to one space, trimmed.
func (d *detection) normalizeText(s string) string {
	for from, to := range d.r.quoteMap {
		s = strings.ReplaceAll(s, from, to)
	}
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	if d.r.wsRunFast {
		s = collapseRuns(b, &d.r.wsRun)
	} else {
		s = d.r.whitespace.ReplaceAllString(string(b), " ")
	}
	return strings.Trim(s, " ")
}

func (d *detection) quotes() []DetectorFinding {
	type quote struct {
		raw, norm  string
		start, end int
	}
	var todo []quote
	for _, re := range d.r.quotes {
		for _, m := range re.FindAllStringSubmatchIndex(d.resp, -1) {
			raw := d.resp[m[2]:m[3]]
			q := strings.Trim(d.normalizeText(raw), d.r.quoteStripEdges)
			words := 0
			for _, w := range strings.Split(q, " ") {
				if w != "" {
					words++
				}
			}
			if words >= d.r.quoteMinWords {
				todo = append(todo, quote{raw: raw, norm: q, start: m[2], end: m[3]})
			}
		}
	}
	// The reference text is normalized only when the response quotes something long enough to
	// check (D167).
	if len(todo) == 0 {
		return orEmpty(nil)
	}
	parts := make([]string, len(d.refParts))
	for i, p := range d.refParts {
		parts[i] = d.normalizeText(p)
	}
	reference := strings.Join(parts, "\n")
	var out []DetectorFinding
	for _, q := range todo {
		if !strings.Contains(reference, q.norm) {
			out = append(out, d.finding(q.raw, q.start, q.end))
		}
	}
	slices.SortStableFunc(out, func(a, b DetectorFinding) int { return a.Start - b.Start })
	return orEmpty(out)
}

// citation_out_of_range

func (d *detection) citations() []DetectorFinding {
	limit := max(d.in.MaxMarker, d.in.PreDedupItems)
	var out []DetectorFinding
	for _, re := range d.r.citations {
		for _, m := range re.FindAllStringSubmatchIndex(d.resp, -1) {
			n, err := strconv.Atoi(d.resp[m[2]:m[3]])
			if err != nil || (n != 0 && n <= limit) {
				continue
			}
			out = append(out, d.finding(d.resp[m[0]:m[1]], m[0], m[1]))
		}
	}
	slices.SortStableFunc(out, func(a, b DetectorFinding) int { return a.Start - b.Start })
	return orEmpty(out)
}

// secret_pattern

func (d *detection) secrets() []DetectorFinding {
	var seen [][2]int
	var out []DetectorFinding
	for _, re := range d.r.secrets {
		for _, m := range re.FindAllStringIndex(d.resp, -1) {
			if slices.ContainsFunc(seen, func(s [2]int) bool { return m[0] < s[1] && s[0] < m[1] }) {
				continue
			}
			seen = append(seen, [2]int{m[0], m[1]})
			if match := d.resp[m[0]:m[1]]; !strings.Contains(d.in.UserRequest, match) {
				out = append(out, d.finding(MaskSecret(match), m[0], m[1]))
			}
		}
	}
	slices.SortStableFunc(out, func(a, b DetectorFinding) int { return a.Start - b.Start })
	return orEmpty(out)
}

// MaskSecret hides a matched credential: the first and last four code points stay, joined by an
// ellipsis; a short match is replaced entirely. Findings never carry an unmasked secret.
func MaskSecret(s string) string {
	r := []rune(s)
	if len(r) <= 12 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// system_prompt_leak

// promptLeak reports whether the detector ran (normalized system instructions of at least
// leakMin code points) and, when it fired, the first window of them found in the response.
func (d *detection) promptLeak() (bool, string) {
	s := []rune(d.normalizeText(d.in.SystemInstructions))
	if len(s) < d.r.leakMin || len(s) < d.r.leakWindow {
		return len(s) >= d.r.leakMin, ""
	}
	r := []rune(d.normalizeText(d.resp))
	w := d.r.leakWindow
	if len(r) < w {
		return true, ""
	}
	// Rabin-Karp: hash every w-code-point window of the response once, then look up each window of
	// the system instructions and confirm a hash hit by comparing the text.
	const base = 1000003
	pow := uint64(1)
	for i := 0; i < w-1; i++ {
		pow *= base
	}
	windows := make(map[uint64][]int, len(r)-w+1)
	var h uint64
	for i, c := range r {
		if i >= w {
			h -= uint64(r[i-w]) * pow
		}
		h = h*base + uint64(c)
		if i >= w-1 {
			windows[h] = append(windows[h], i-w+1)
		}
	}
	starts := []int{}
	for i := 0; i+w <= len(s); i += d.r.leakStride {
		starts = append(starts, i)
	}
	if last := len(s) - w; starts[len(starts)-1] != last {
		starts = append(starts, last)
	}
	for _, i := range starts {
		win := s[i : i+w]
		var wh uint64
		for _, c := range win {
			wh = wh*base + uint64(c) // #nosec G115 -- a rune from a string is a code point in [0, 0x10FFFF]
		}
		for _, j := range windows[wh] {
			if slices.Equal(r[j:j+w], win) {
				return true, string(win)
			}
		}
	}
	return true, ""
}

// helpers

// utf16Index maps every byte offset of s (0..len(s)) to its UTF-16 code unit offset.
func utf16Index(s string) []int {
	idx := make([]int, len(s)+1)
	u := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		units := 1
		if r >= 0x10000 {
			units = 2
		}
		for k := 0; k < size; k++ {
			idx[i+k] = u
		}
		u += units
		i += size
	}
	idx[len(s)] = u
	return idx
}

func startsInside(spans [][]int, pos int) bool {
	for _, s := range spans {
		if s[0] <= pos && pos < s[1] {
			return true
		}
	}
	return false
}

func orEmpty(f []DetectorFinding) []DetectorFinding {
	if f == nil {
		return []DetectorFinding{}
	}
	return f
}
