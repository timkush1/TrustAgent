package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

// MaxDraftDescription is the longest description "Draft with AI" takes, in characters (SPEC §2.4
// row 34).
const MaxDraftDescription = 2000

// draftPrompt is the part of contracts/prompts/evaluator-drafter.v1.json the gateway reads: the
// texts the engine sends, to price the call before it is made (P4-PLAN D48).
type draftPrompt struct {
	Call struct {
		MaxTokens int `json:"max_tokens"`
	} `json:"call"`
	Variables map[string]struct {
		Empty    string `json:"empty"`
		MaxChars int    `json:"max_chars"`
	} `json:"variables"`
	System []string `json:"system"`
	User   []string `json:"user"`
}

var loadDraftPrompt = sync.OnceValues(func() (*draftPrompt, error) {
	b, err := contracts.FS.ReadFile("prompts/evaluator-drafter.v1.json")
	if err != nil {
		return nil, fmt.Errorf("drafter prompt: %w", err)
	}
	var p draftPrompt
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("drafter prompt: %w", err)
	}
	if p.Call.MaxTokens <= 0 || len(p.System) == 0 || len(p.User) == 0 {
		return nil, errors.New("drafter prompt: call.max_tokens, system and user are required")
	}
	return &p, nil
})

// DraftInput is what the drafter's user turn carries.
type DraftInput struct {
	Description, RouteName, RouteDescription string
	ExistingIDs                              []string
}

// DraftPromptChars is the length in characters of the prompt the engine sends for in: the system
// text and the user turn with its variables filled (an empty one takes its "empty" text, and each
// is cut at its max_chars).
func DraftPromptChars(in DraftInput) (int, error) {
	p, err := loadDraftPrompt()
	if err != nil {
		return 0, err
	}
	values := map[string]string{
		"description": in.Description, "route_name": in.RouteName, "route_description": in.RouteDescription,
		"existing_ids": strings.Join(in.ExistingIDs, ", "),
	}
	pairs := make([]string, 0, 2*len(values))
	for name, v := range values {
		spec := p.Variables[name]
		if v == "" {
			v = spec.Empty
		}
		if spec.MaxChars > 0 && utf8.RuneCountInString(v) > spec.MaxChars {
			v = string([]rune(v)[:spec.MaxChars])
		}
		pairs = append(pairs, "{{"+name+"}}", v)
	}
	user := strings.NewReplacer(pairs...).Replace(strings.Join(p.User, "\n"))
	return utf8.RuneCountInString(strings.Join(p.System, "\n")) + utf8.RuneCountInString(user), nil
}

// DraftReserveUSD is what a draft reserves of the LLM cap (P4-PLAN D48): the prompt's tokens
// (promptChars / cpt) at the judge's input price plus the prompt's max_tokens of output at its
// output price.
func DraftReserveUSD(promptChars int, cpt float64, price decision.PriceEntry) (float64, error) {
	p, err := loadDraftPrompt()
	if err != nil {
		return 0, err
	}
	if cpt <= 0 {
		cpt = decision.DefaultCPT
	}
	in := float64(promptChars) / cpt
	return (in*price.InputPerMTok + float64(p.Call.MaxTokens)*price.OutputPerMTok) / 1e6, nil
}
