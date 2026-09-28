package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/truthtable/contracts"
)

// The dashboard outcome of a trace (SPEC §2.9 "Outcome for the UI"): the first rule of
// contracts/ui/outcomes.v1.json whose conditions all hold. GET /api/traces?outcome= filters with
// the same rules compiled to SQL, and trafficgen checks scenario outcomes with Outcome.

// OutcomeFields are the trace fields the outcome rules read.
type OutcomeFields struct {
	GateStatus  string
	Action      string
	WouldAction string
}

type outcomeRule struct {
	When    map[string][]string `json:"when"`
	Outcome string              `json:"outcome"`
}

type outcomeRules struct {
	ids   []string
	rules []outcomeRule
	sql   string
}

// outcomeColumns maps the fields a rule may test to their traces columns.
var outcomeColumns = map[string]string{"gate_status": "t.gate_status", "action": "t.action", "would_action": "t.would_action"}

// ruleValue is the shape of an enum value that may be inlined into SQL.
var ruleValue = regexp.MustCompile(`^[a-z_]+$`)

var (
	outcomesOnce sync.Once
	outcomes     *outcomeRules
	outcomesErr  error
)

func loadOutcomes() (*outcomeRules, error) {
	outcomesOnce.Do(func() {
		b, err := contracts.FS.ReadFile("ui/outcomes.v1.json")
		if err != nil {
			outcomesErr = err
			return
		}
		var doc struct {
			Outcomes []struct {
				ID string `json:"id"`
			} `json:"outcomes"`
			Rules []outcomeRule `json:"rules"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			outcomesErr = fmt.Errorf("ui/outcomes.v1.json: %w", err)
			return
		}
		r := &outcomeRules{rules: doc.Rules}
		for _, o := range doc.Outcomes {
			r.ids = append(r.ids, o.ID)
		}
		var sql strings.Builder
		sql.WriteString("CASE")
		for _, rule := range doc.Rules {
			if !slices.Contains(r.ids, rule.Outcome) {
				outcomesErr = fmt.Errorf("ui/outcomes.v1.json: rule outcome %q is not an outcome", rule.Outcome)
				return
			}
			conds := []string{"TRUE"}
			for _, field := range sortedKeys(rule.When) {
				col, ok := outcomeColumns[field]
				if !ok {
					outcomesErr = fmt.Errorf("ui/outcomes.v1.json: rules cannot test %q", field)
					return
				}
				quoted := make([]string, 0, len(rule.When[field]))
				for _, v := range rule.When[field] {
					if !ruleValue.MatchString(v) {
						outcomesErr = fmt.Errorf("ui/outcomes.v1.json: value %q of %s", v, field)
						return
					}
					quoted = append(quoted, "'"+v+"'")
				}
				conds = append(conds, fmt.Sprintf("COALESCE(%s, '') IN (%s)", col, strings.Join(quoted, ", ")))
			}
			fmt.Fprintf(&sql, " WHEN %s THEN '%s'", strings.Join(conds, " AND "), rule.Outcome)
		}
		sql.WriteString(" END")
		r.sql = sql.String()
		outcomes = r
	})
	return outcomes, outcomesErr
}

// Outcome returns the outcome id of a trace, or "" if the contract cannot be read.
func Outcome(f OutcomeFields) string {
	r, err := loadOutcomes()
	if err != nil {
		return ""
	}
	values := map[string]string{"gate_status": f.GateStatus, "action": f.Action, "would_action": f.WouldAction}
	for _, rule := range r.rules {
		match := true
		for field, allowed := range rule.When {
			if !slices.Contains(allowed, values[field]) {
				match = false
				break
			}
		}
		if match {
			return rule.Outcome
		}
	}
	return ""
}

// OutcomeIDs returns the outcome ids of the contract.
func OutcomeIDs() []string {
	r, err := loadOutcomes()
	if err != nil {
		return nil
	}
	return slices.Clone(r.ids)
}

// outcomeSQL is the CASE expression that computes the outcome of a traces row aliased t.
func outcomeSQL() (string, error) {
	r, err := loadOutcomes()
	if err != nil {
		return "", err
	}
	return r.sql, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
