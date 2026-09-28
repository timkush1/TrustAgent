package evidence

import (
	"slices"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// Knowledge states (EvidenceSummary.knowledge).
const (
	KnowledgeNotRequested = "not_requested"
	KnowledgeDisabled     = "disabled"
)

// Summary is what a decision was checked against (contracts/schema/evidence-summary.v1.schema.json).
type Summary struct {
	// Configured is the route evidence.sources, in priority order.
	Configured []string      `json:"configured"`
	Used       []SourceCount `json:"used"`
	// Empty lists the configured sources that yielded nothing.
	Empty     []string `json:"empty"`
	Knowledge string   `json:"knowledge"`
	Truncated bool     `json:"truncated"`
	// Tokens is the estimated fast-check tokens of the used evidence (SPEC §2.9 est()).
	Tokens int `json:"tokens"`
}

// SourceCount is the evidence one source contributed.
type SourceCount struct {
	Source string   `json:"source"`
	Items  int      `json:"items"`
	Labels []string `json:"labels"`
	Tokens int      `json:"tokens"`
}

// Summarize describes the trusted items of r per configured source. truncated says whether packing
// cut or skipped an item; cpt is the characters-per-token ratio of the token estimates. The
// knowledge add-on is not available in S0, so a configured knowledge source reports disabled.
func Summarize(r *Result, configured []string, truncated bool, cpt float64) Summary {
	s := Summary{
		Configured: slices.Clone(configured),
		Used:       []SourceCount{},
		Empty:      []string{},
		Knowledge:  KnowledgeNotRequested,
		Truncated:  truncated,
	}
	if s.Configured == nil {
		s.Configured = []string{}
	}
	for _, src := range configured {
		if src == SourceKnowledge {
			s.Knowledge = KnowledgeDisabled
		}
		c := SourceCount{Source: src, Labels: []string{}}
		for _, it := range r.Items {
			if it.Source != src || it.Untrusted {
				continue
			}
			c.Items++
			if !slices.Contains(c.Labels, it.Label) {
				c.Labels = append(c.Labels, it.Label)
			}
			if est, err := decision.Est(it.Text, cpt); err == nil {
				c.Tokens += est
			}
		}
		if c.Items == 0 {
			if !slices.Contains(s.Empty, src) {
				s.Empty = append(s.Empty, src)
			}
			continue
		}
		s.Used = append(s.Used, c)
		s.Tokens += c.Tokens
	}
	return s
}

// CheckedAgainst is the X-TrustAgent-Checked-Against value: "request_system:2,tool_results:1", or
// "none" when no evidence was used.
func (s Summary) CheckedAgainst() string {
	if len(s.Used) == 0 {
		return "none"
	}
	parts := make([]string, len(s.Used))
	for i, u := range s.Used {
		parts[i] = u.Source + ":" + strconv.Itoa(u.Items)
	}
	return strings.Join(parts, ",")
}
