package decision

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// Compile limits and markers (SPEC §2.9 compile).
const (
	// MaxRequestTokens is the hard ceiling of one request: overhead + est(state) + max est(q).
	MaxRequestTokens = 28800
	// NoneProvided replaces an empty text field in the state.
	NoneProvided = "(none provided)"
	// userRequestLongTokens is the est() above which user_request keeps only its head and tail.
	userRequestLongTokens = 1000
	// userRequestHeadTokens and userRequestTailTokens size the kept head and tail (times cpt).
	userRequestHeadTokens = 600
	userRequestTailTokens = 400
	// userRequestJoin joins the head and tail of a long user_request.
	userRequestJoin = " … " // space, U+2026 horizontal ellipsis, space
	// systemInstructionsTokens sizes the kept head of system_instructions (times cpt).
	systemInstructionsTokens = 800
	// minTruncationTokens is the room an item needs to be truncated instead of skipped.
	minTruncationTokens = 200
)

// ErrRequestTooLarge is returned when a single question cannot fit in a request with the state.
var ErrRequestTooLarge = errors.New("decision: question does not fit in a request with the state")

// CompileRoute is the part of a route's settings that compile() reads.
type CompileRoute struct {
	// Evaluators are the route's evaluators in route order.
	Evaluators []RouteEvaluator `json:"evaluators"`
	// OnNoEvidence is ignore, flag or block.
	OnNoEvidence string `json:"on_no_evidence"`
	// EvidenceSources is the route's evidence.sources priority order (source_rank).
	EvidenceSources []string `json:"evidence_sources"`
	// ClientRules is overrides.client_rules: whether request trustagent.rules may set params.
	ClientRules bool `json:"client_rules"`
	// Model is the decision model requested (route decision.model or TRUTHTABLE_JEV_MODEL).
	Model string `json:"model"`
}

// RouteEvaluator is a route's reference to an evaluator (policy RouteEvaluatorRef).
type RouteEvaluator struct {
	Ref     string            `json:"ref"`
	Stage   string            `json:"stage,omitempty"`   // default active
	Enabled *bool             `json:"enabled,omitempty"` // default true
	Params  map[string]string `json:"params,omitempty"`
}

// CompileInputs are the texts of one gated exchange.
type CompileInputs struct {
	UserRequest        string `json:"user_request"`
	AssistantResponse  string `json:"assistant_response"`
	SystemInstructions string `json:"system_instructions"`
	// Evidence is priority-ordered and not yet packed.
	Evidence []EvidenceItem `json:"evidence"`
	// Rules are the request's trustagent.rules, honoured only with CompileRoute.ClientRules.
	Rules map[string]string `json:"rules"`
}

// EvidenceItem is one extracted evidence item.
type EvidenceItem struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Text   string `json:"text"`
	// Untrusted items (untrusted_tools) never enter the state.
	Untrusted bool `json:"untrusted,omitempty"`
}

// Budget sizes the compiled requests.
type Budget struct {
	Tokens            int     `json:"tokens"`
	CPT               float64 `json:"cpt"`
	MaxQuestions      int     `json:"max_questions"`
	Overhead          int     `json:"overhead"`
	MaxResponseTokens int     `json:"max_response_tokens"`
	EvidenceMaxTokens int     `json:"evidence_max_tokens"`
}

// DefaultBudget is the budget with the SPEC §2.7 defaults (TRUTHTABLE_JEV_TOKEN_BUDGET,
// _CHARS_PER_TOKEN, _MAX_QUESTIONS_PER_CALL, route max_response_tokens and evidence.max_tokens).
func DefaultBudget() Budget {
	return Budget{
		Tokens:            24000,
		CPT:               DefaultCPT,
		MaxQuestions:      16,
		Overhead:          Overhead,
		MaxResponseTokens: 6000,
		EvidenceMaxTokens: 8000,
	}
}

// Request is one decision-model request body (without transport fields such as session_id).
type Request struct {
	Model     string         `json:"model"`
	State     map[string]any `json:"state"`
	Questions map[string]any `json:"questions"`
}

// CompiledEvaluator is an evaluator compile() considered: the enabled route evaluators in route
// order, then the implicit factual_claims@1 when it was added.
type CompiledEvaluator struct {
	Ref      string `json:"ref"`
	Stage    string `json:"stage"`
	Internal bool   `json:"internal"`
}

// Compiled is the output of compile().
type Compiled struct {
	Requests   []Request           `json:"requests"`
	Evaluators []CompiledEvaluator `json:"evaluators"`
	// QuestionMap maps each asked question id to its evaluator ref.
	QuestionMap map[string]string `json:"question_map"`
	// QIDRequest maps each asked question id to the index of its request.
	QIDRequest   map[string]int `json:"qid_request"`
	Excluded     []Exclusion    `json:"excluded"`
	PackedIDs    []string       `json:"packed_ids"`
	TruncatedIDs []string       `json:"truncated_ids"`
	// Truncated is true when an evidence item was truncated or skipped by packing.
	Truncated         bool     `json:"truncated"`
	CPT               float64  `json:"cpt"`
	EstTokens         []int    `json:"est_tokens"`
	RequestSHA256     []string `json:"request_sha256"`
	QuestionSetSHA256 string   `json:"question_set_sha256"`
}

// member is an evaluator taking part in compilation.
type member struct {
	ref      string
	spec     *Spec
	stage    string
	internal bool
	params   map[string]string
	excluded string // exclusion reason, "" while included
}

// Compile turns a route's evaluators and one exchange into decision-model requests
// (SPEC §2.9 compile). specs resolves every evaluator ref of the route, plus factual_claims@1 when
// the route sets on_no_evidence. An error means the route or its evaluators are unusable.
func Compile(route *CompileRoute, specs map[string]*Spec, in *CompileInputs, b Budget) (*Compiled, error) {
	if err := checkBudget(b); err != nil {
		return nil, err
	}
	if route.Model == "" {
		return nil, errors.New("compile: the decision model is not set")
	}
	switch route.OnNoEvidence {
	case OnNoEvidenceIgnore, OnNoEvidenceFlag, OnNoEvidenceBlock:
	default:
		return nil, fmt.Errorf("compile: unknown on_no_evidence %q", route.OnNoEvidence)
	}

	// Step 0: enabled evaluators; shadow ones are asked too.
	var members []*member
	seen := map[string]bool{}
	for _, re := range route.Evaluators {
		if re.Enabled != nil && !*re.Enabled {
			continue
		}
		spec := specs[re.Ref]
		if spec == nil {
			return nil, fmt.Errorf("compile: no spec for evaluator %s", re.Ref)
		}
		if seen[re.Ref] {
			return nil, fmt.Errorf("compile: evaluator %s is attached twice", re.Ref)
		}
		seen[re.Ref] = true
		stage := re.Stage
		switch stage {
		case "":
			stage = StageActive
		case StageActive, StageShadow:
		default:
			return nil, fmt.Errorf("compile: evaluator %s: unknown stage %q", re.Ref, stage)
		}
		members = append(members, &member{ref: re.Ref, spec: spec, stage: stage, params: re.Params})
	}

	out := &Compiled{
		Requests:      []Request{},
		Evaluators:    []CompiledEvaluator{},
		QuestionMap:   map[string]string{},
		QIDRequest:    map[string]int{},
		Excluded:      []Exclusion{},
		PackedIDs:     []string{},
		TruncatedIDs:  []string{},
		CPT:           b.CPT,
		EstTokens:     []int{},
		RequestSHA256: []string{},
	}

	// Step 1: a response too long to check excludes everything.
	if estString(in.AssistantResponse, b.CPT) > b.MaxResponseTokens {
		for _, m := range members {
			m.excluded = ExcludedTooLong
		}
		return finish(out, route, members, nil, nil, b)
	}

	// Step 2: the pre-pack evidence gate. Untrusted items never count as evidence.
	var items []EvidenceItem
	for _, it := range in.Evidence {
		if !it.Untrusted {
			items = append(items, it)
		}
	}
	total := 0
	for _, it := range items {
		total += utf8.RuneCountInString(it.Text)
	}
	noEvidence := false
	for _, m := range members {
		if m.spec.Evidence.Requires != EvidenceRequired {
			continue
		}
		switch {
		case len(items) == 0:
			m.excluded = ExcludedNoEvidence
			noEvidence = true
		case total < m.spec.Evidence.MinChars:
			m.excluded = ExcludedEvidenceTooShort
		}
	}

	// Step 3: the implicit factual_claims evaluator.
	if route.OnNoEvidence != OnNoEvidenceIgnore && noEvidence &&
		!slices.ContainsFunc(members, func(m *member) bool { return m.spec.ID == FactualClaimsID }) {
		spec := specs[FactualClaimsRef]
		if spec == nil {
			return nil, fmt.Errorf("compile: on_no_evidence needs the %s spec", FactualClaimsRef)
		}
		members = append(members, &member{ref: FactualClaimsRef, spec: spec, stage: StageActive, internal: true})
	}

	// Steps 4-5: questions and the state without evidence.
	_, qEst, err := buildQuestions(members, b.CPT)
	if err != nil {
		return nil, err
	}
	base := buildState(members, route, in, b.CPT)

	// Step 6: pack evidence when an included evaluator reads it.
	var packed []any
	if needsEvidence(members) {
		baseEst, err := Est(base, b.CPT)
		if err != nil {
			return nil, err
		}
		qSum := 0
		for _, e := range qEst {
			qSum += e
		}
		limit := min(b.EvidenceMaxTokens, b.Tokens-b.Overhead-qSum-baseEst)
		p := pack(items, in.AssistantResponse, route.EvidenceSources, limit, b.CPT)
		packed = p.state
		out.PackedIDs = p.packedIDs
		out.TruncatedIDs = p.truncatedIDs
		out.Truncated = p.truncated

		// Step 7: the post-pack evidence gate.
		for _, m := range members {
			if m.excluded == "" && m.spec.Evidence.Requires == EvidenceRequired && p.chars < m.spec.Evidence.MinChars {
				m.excluded = ExcludedEvidencePackedAway
			}
		}
	}
	return finish(out, route, members, packed, in, b)
}

// finish rebuilds the state for the evaluators still included, splits the questions into
// requests (step 8) and computes the hashes (step 9).
func finish(out *Compiled, route *CompileRoute, members []*member, packed []any, in *CompileInputs, b Budget) (*Compiled, error) {
	for _, m := range members {
		out.Evaluators = append(out.Evaluators, CompiledEvaluator{Ref: m.ref, Stage: m.stage, Internal: m.internal})
		if m.excluded != "" {
			out.Excluded = append(out.Excluded, Exclusion{Ref: m.ref, Reason: m.excluded})
		}
	}
	questions, qEst, err := buildQuestions(members, b.CPT)
	if err != nil {
		return nil, err
	}
	qids := make([]string, 0, len(questions))
	for qid := range questions {
		qids = append(qids, qid)
	}
	SortUTF16(qids)
	pairs := make([]any, 0, len(qids))
	for _, qid := range qids {
		pairs = append(pairs, []any{qid, questions[qid]})
	}
	if out.QuestionSetSHA256, err = SHA256Hex(pairs); err != nil {
		return nil, err
	}
	if len(questions) == 0 {
		return out, nil
	}
	for _, m := range members {
		if m.excluded == "" {
			out.QuestionMap[m.spec.QuestionID()] = m.ref
		}
	}

	state := buildState(members, route, in, b.CPT)
	if needsEvidence(members) {
		if len(packed) > 0 {
			state[FieldEvidence] = packed
		} else {
			state[FieldEvidence] = NoneProvided
		}
	}
	if err := checkTextTree(state, "state"); err != nil {
		return nil, err
	}
	stateEst, err := Est(state, b.CPT)
	if err != nil {
		return nil, err
	}

	for k, bin := range split(qids, qEst, stateEst, b) {
		if b.Overhead+stateEst+qEst[bin[0]] > MaxRequestTokens {
			return nil, fmt.Errorf("%w: %s needs %d tokens", ErrRequestTooLarge, bin[0], b.Overhead+stateEst+qEst[bin[0]])
		}
		req := Request{Model: route.Model, State: state, Questions: map[string]any{}}
		for _, qid := range bin {
			req.Questions[qid] = questions[qid]
			out.QIDRequest[qid] = k
		}
		sum, err := SHA256Hex(req)
		if err != nil {
			return nil, err
		}
		est, err := Est(req, b.CPT)
		if err != nil {
			return nil, err
		}
		out.Requests = append(out.Requests, req)
		out.RequestSHA256 = append(out.RequestSHA256, sum)
		out.EstTokens = append(out.EstTokens, b.Overhead+est)
	}
	return out, nil
}

// split packs the questions into requests first-fit-decreasing by est(q) (ties by question id),
// so that each request has at most MaxQuestions questions and fits the token budget with the
// shared state. A question that fits no request opens a new one on its own. The largest question
// of every request comes first.
func split(qids []string, qEst map[string]int, stateEst int, b Budget) [][]string {
	order := slices.Clone(qids)
	slices.SortStableFunc(order, func(x, y string) int { return qEst[y] - qEst[x] })
	var bins [][]string
	var sums []int
	for _, qid := range order {
		placed := false
		for i := range bins {
			if len(bins[i]) < b.MaxQuestions && b.Overhead+stateEst+sums[i]+qEst[qid] <= b.Tokens {
				bins[i] = append(bins[i], qid)
				sums[i] += qEst[qid]
				placed = true
				break
			}
		}
		if !placed {
			bins = append(bins, []string{qid})
			sums = append(sums, qEst[qid])
		}
	}
	return bins
}

// buildQuestions returns the question bodies of the included members keyed by question id, and
// est() of each body.
func buildQuestions(members []*member, cpt float64) (map[string]any, map[string]int, error) {
	questions := map[string]any{}
	qEst := map[string]int{}
	for _, m := range members {
		if m.excluded != "" {
			continue
		}
		qid := m.spec.QuestionID()
		if _, dup := questions[qid]; dup {
			return nil, nil, fmt.Errorf("compile: question id %s is asked twice", qid)
		}
		body := m.spec.QuestionBody()
		if err := checkTextTree(body, "question "+qid); err != nil {
			return nil, nil, err
		}
		e, err := Est(body, cpt)
		if err != nil {
			return nil, nil, err
		}
		questions[qid] = body
		qEst[qid] = e
	}
	return questions, qEst, nil
}

// needsEvidence reports whether an included member lists the evidence state field.
func needsEvidence(members []*member) bool {
	return slices.ContainsFunc(members, func(m *member) bool {
		return m.excluded == "" && m.spec.HasStateField(FieldEvidence)
	})
}

// buildState builds the state of the included members without evidence (step 4). A nil in
// yields an empty state.
func buildState(members []*member, route *CompileRoute, in *CompileInputs, cpt float64) map[string]any {
	state := map[string]any{}
	if in == nil {
		return state
	}
	fields := map[string]bool{}
	for _, m := range members {
		if m.excluded == "" {
			for _, f := range m.spec.StateFields {
				fields[f] = true
			}
		}
	}
	if fields[FieldUserRequest] {
		state[FieldUserRequest] = orNone(shortenUserRequest(in.UserRequest, cpt))
	}
	if fields[FieldAssistantResponse] {
		state[FieldAssistantResponse] = orNone(in.AssistantResponse)
	}
	if fields[FieldSystemInstructions] {
		state[FieldSystemInstructions] = orNone(headRunes(in.SystemInstructions, int(math.Floor(systemInstructionsTokens*cpt))))
	}
	if fields[FieldRules] {
		rules := map[string]any{}
		for _, m := range members {
			if m.excluded != "" || !m.spec.HasStateField(FieldRules) {
				continue
			}
			for name, p := range m.spec.Params {
				v := p.Default
				if rv, ok := m.params[name]; ok {
					v = rv
				}
				if cv, ok := in.Rules[name]; ok && route.ClientRules {
					v = cv
				}
				rules[name] = orNone(v)
			}
		}
		if len(rules) > 0 {
			state[FieldRules] = rules
		} else {
			state[FieldRules] = NoneProvided
		}
	}
	return state
}

// shortenUserRequest keeps the head and tail of a user_request longer than 1,000 tokens.
func shortenUserRequest(s string, cpt float64) string {
	if estString(s, cpt) <= userRequestLongTokens {
		return s
	}
	head := int(math.Floor(userRequestHeadTokens * cpt))
	tail := int(math.Floor(userRequestTailTokens * cpt))
	r := []rune(s)
	if len(r) <= head+tail {
		return s
	}
	return string(r[:head]) + userRequestJoin + string(r[len(r)-tail:])
}

// headRunes returns the first n code points of s.
func headRunes(s string, n int) string {
	i := 0
	for count := 0; i < len(s) && count < n; count++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func orNone(s string) string {
	if s == "" {
		return NoneProvided
	}
	return s
}

// packResult is the outcome of evidence packing.
type packResult struct {
	state        []any // [{id, source, text}] in (source_rank, index) order
	packedIDs    []string
	truncatedIDs []string
	truncated    bool
	chars        int // code points of the packed text
}

// pack fits the evidence items into limit tokens (SPEC §2.9 compile step 6).
func pack(items []EvidenceItem, response string, sources []string, limit int, cpt float64) packResult {
	type ranked struct {
		index   int
		rank    int
		overlap float64
		text    string
		taken   bool
		cut     bool
	}
	respTokens := overlapTokens(response)
	rs := make([]*ranked, len(items))
	for i, it := range items {
		rank := slices.Index(sources, it.Source)
		if rank < 0 {
			rank = len(sources)
		}
		common := 0
		for t := range overlapTokens(it.Text) {
			if respTokens[t] {
				common++
			}
		}
		rs[i] = &ranked{
			index:   i,
			rank:    rank,
			overlap: float64(common) / math.Sqrt(math.Max(1, float64(estString(it.Text, cpt)))),
			text:    it.Text,
		}
	}
	order := slices.Clone(rs)
	slices.SortStableFunc(order, func(a, b *ranked) int {
		switch {
		case a.rank != b.rank:
			return a.rank - b.rank
		case a.overlap > b.overlap:
			return -1
		case a.overlap < b.overlap:
			return 1
		}
		return a.index - b.index
	})

	itemEst := func(i int, text string) int {
		e, _ := Est(map[string]any{"id": items[i].ID, "source": items[i].Source, "text": text}, cpt)
		return e
	}
	var res packResult
	used := 0
	for _, r := range order {
		full := itemEst(r.index, r.text)
		if used+full <= limit {
			r.taken = true
			used += full
			continue
		}
		if limit-used >= minTruncationTokens {
			n := int(math.Floor(float64(limit-used-itemEst(r.index, "")) * cpt))
			if n > 0 {
				r.text = headRunes(r.text, n)
				r.taken = true
				r.cut = r.text != items[r.index].Text
				used += itemEst(r.index, r.text)
				continue
			}
		}
		res.truncated = true // skipped
	}

	slices.SortStableFunc(order, func(a, b *ranked) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return a.index - b.index
	})
	res.state = []any{}
	res.packedIDs = []string{}
	res.truncatedIDs = []string{}
	for _, r := range order {
		if !r.taken {
			continue
		}
		it := items[r.index]
		res.state = append(res.state, map[string]any{"id": it.ID, "source": it.Source, "text": r.text})
		res.packedIDs = append(res.packedIDs, it.ID)
		res.chars += utf8.RuneCountInString(r.text)
		if r.cut {
			res.truncatedIDs = append(res.truncatedIDs, it.ID)
			res.truncated = true
		}
	}
	return res
}

// overlapTokens is T(text): the set of [a-z0-9]{4,} runs of the ASCII-lowercased text.
func overlapTokens(text string) map[string]bool {
	set := map[string]bool{}
	start := -1
	flush := func(end int) {
		if start >= 0 && end-start >= 4 {
			set[strings.ToLower(text[start:end])] = true
		}
		start = -1
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(text))
	return set
}

// checkTextTree rejects JSON numbers and booleans, and object keys that are not printable ASCII,
// in a state or question tree (SPEC §2.1 Jev payloads).
func checkTextTree(v any, where string) error {
	switch x := v.(type) {
	case nil, string:
		return nil
	case []any:
		for _, e := range x {
			if err := checkTextTree(e, where); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range x {
			if !printableASCII(k) {
				return fmt.Errorf("compile: %s: key %q is not printable ASCII", where, k)
			}
			if err := checkTextTree(e, where); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("compile: %s contains %v; only strings, arrays and objects are allowed", where, v)
	}
	return nil
}

func printableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func checkBudget(b Budget) error {
	if err := validCPT(b.CPT); err != nil {
		return err
	}
	if b.Tokens <= 0 || b.MaxQuestions < 1 || b.Overhead < 0 || b.MaxResponseTokens <= 0 || b.EvidenceMaxTokens < 0 {
		return fmt.Errorf("compile: invalid budget %+v", b)
	}
	return nil
}

// DecideEvaluators returns the decide() evaluators for this compile result: every evaluator compile
// considered, in order (the implicit factual_claims last), with its spec and the route's threshold
// overrides from thresholds (keyed by ref; a missing entry means raw space and spec thresholds).
func (c *Compiled) DecideEvaluators(specs map[string]*Spec, thresholds map[string]Thresholds) ([]EvaluatorInput, error) {
	out := make([]EvaluatorInput, 0, len(c.Evaluators))
	for _, e := range c.Evaluators {
		spec := specs[e.Ref]
		if spec == nil {
			return nil, fmt.Errorf("decide evaluators: no spec for %s", e.Ref)
		}
		th := thresholds[e.Ref]
		if th.Space == "" {
			th.Space = SpaceRaw
		}
		out = append(out, EvaluatorInput{Ref: e.Ref, Spec: spec, Stage: e.Stage, Internal: e.Internal, Thresholds: th})
	}
	return out, nil
}
