package llmapi

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// DefaultFallbackSystemPrompt is the default RouteSettings.fallback.system_prompt
// (contracts/schema/policy.v1.schema.json).
const DefaultFallbackSystemPrompt = "Answer using only the information in CONTEXT. If CONTEXT does not contain the answer, " +
	"say you don't have that information. Do not add facts, numbers, dates, names or links that are not in CONTEXT."

// FallbackNoEvidenceSuffix ends the fallback prompt of a request without evidence, in place of the
// CONTEXT block (SPEC §2.12).
const FallbackNoEvidenceSuffix = " If you are not certain, say so; do not include specific numbers, dates, URLs or quotations."

// DefaultFallbackEvidenceTokens bounds the evidence packed into the fallback prompt (8k tokens).
const DefaultFallbackEvidenceTokens = 8000

// ErrFallbackRequest is returned when a request has no place for the fallback prompt: an OpenAI
// chat body without a messages array, a completions body without a string prompt, or an Anthropic
// system that is neither a string nor an array.
var ErrFallbackRequest = errors.New("llmapi: the request cannot carry a fallback prompt")

// EvidenceLine is one evidence item for the fallback prompt, written as "[id] text".
type EvidenceLine struct {
	ID   string
	Text string
}

// Fallback is what a fallback request is built from: the route's fallback settings and the
// request's evidence.
type Fallback struct {
	// Model is fallback.model; empty keeps the original model.
	Model string
	// SystemPrompt is fallback.system_prompt; empty is DefaultFallbackSystemPrompt.
	SystemPrompt string
	// Evidence is the trusted evidence in priority order (untrusted items must not be passed).
	Evidence []EvidenceLine
	// MaxTokens bounds the packed evidence lines; 0 is DefaultFallbackEvidenceTokens.
	MaxTokens int
	// CountTokens estimates the tokens of a text; nil uses ceil(narrow/4 + wide) over code
	// points, wide being U+3000 and above (the SPEC §2.9 estimate with the default cpt). The
	// gate passes the decision package's estimate with its runtime cpt.
	CountTokens func(string) int
}

// FallbackRequest is a built fallback request.
type FallbackRequest struct {
	// Body is the request to send once to the fallback upstream.
	Body []byte
	// Model is the model the request names.
	Model string
	// Prompt is the text that was prepended.
	Prompt string
	// PackedIDs are the ids of the evidence lines packed into the prompt; Truncated is set when
	// the budget cut an item short or left items out.
	PackedIDs []string
	Truncated bool
}

// BuildFallbackRequest builds the fallback request of a same-format original request
// (SPEC §2.12 Fallback, step 1):
//
//   - model = f.Model or the original model;
//   - the fallback prompt f.SystemPrompt + "\n\nCONTEXT:\n<context>\n" + "[id] text" evidence
//     lines (at most f.MaxTokens) + "\n</context>" — without evidence f.SystemPrompt +
//     FallbackNoEvidenceSuffix — is prepended: as a leading system message (openai_chat), to the
//     top-level system (anthropic_messages: before a string, or as a first text block), or to
//     the prompt (openai_completions, each string of a prompt array);
//   - stream is forced to false (stream_options goes with it: OpenAI rejects it without a
//     stream), and the gateway's own members are removed.
//
// Every other member keeps its bytes. It fails with ErrUnparseable for a body that is not a JSON
// object and ErrFallbackRequest when the prompt has nowhere to go.
func BuildFallbackRequest(format string, original []byte, f Fallback) (*FallbackRequest, error) {
	obj, err := parseObject(original)
	if err != nil {
		return nil, ErrUnparseable
	}
	prompt, packed, truncated := FallbackPrompt(f)
	out := &FallbackRequest{Prompt: prompt, PackedIDs: packed, Truncated: truncated}
	switch format {
	case FormatOpenAIChat:
		msgs, ok := arrayMember(obj, "messages")
		if !ok {
			return nil, ErrFallbackRequest
		}
		sys := marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"system", prompt})
		obj.set("messages", joinArray(append([][]byte{sys}, msgs...)))
	case FormatOpenAICompletions:
		raw, _ := obj.get("prompt")
		p, err := prefixPrompt(raw, prompt)
		if err != nil {
			return nil, err
		}
		obj.set("prompt", p)
	case FormatAnthropicMessages:
		raw, _ := obj.get("system")
		s, err := prefixSystem(raw, prompt)
		if err != nil {
			return nil, err
		}
		obj.set("system", s)
	default:
		return nil, ErrUnknownFormat
	}
	obj.del(ExtensionField, TestResponseField, "stream_options")
	if f.Model != "" {
		obj.set("model", marshal(f.Model))
		out.Model = f.Model
	} else if raw, ok := obj.get("model"); ok {
		decodeLoose(raw, &out.Model)
	}
	obj.set("stream", []byte("false"))
	out.Body = obj.bytes()
	return out, nil
}

// FallbackPrompt returns the fallback prompt for f, the ids of the packed evidence and whether
// the token budget cut any evidence.
func FallbackPrompt(f Fallback) (prompt string, packed []string, truncated bool) {
	system := f.SystemPrompt
	if system == "" {
		system = DefaultFallbackSystemPrompt
	}
	lines, packed, truncated := packEvidence(f)
	if len(lines) == 0 {
		return system + FallbackNoEvidenceSuffix, nil, truncated
	}
	return system + "\n\nCONTEXT:\n<context>\n" + strings.Join(lines, "\n") + "\n</context>", packed, truncated
}

// packEvidence writes the evidence as "[id] text" lines while they fit the budget. The first
// line that does not fit is cut to the budget (on a code point boundary) and the rest are left
// out. Items without text are skipped; an item without an id is numbered by its position.
func packEvidence(f Fallback) (lines, ids []string, truncated bool) {
	count := f.CountTokens
	if count == nil {
		count = estimateTokens
	}
	budget := f.MaxTokens
	if budget <= 0 {
		budget = DefaultFallbackEvidenceTokens
	}
	for i, ev := range f.Evidence {
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			continue
		}
		id := ev.ID
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		head := "[" + id + "] "
		line := head + text
		if n := count(line); n <= budget {
			lines, ids = append(lines, line), append(ids, id)
			budget -= n
			continue
		}
		truncated = true
		if cut := fitPrefix(text, func(s string) bool { return count(head+s) <= budget }); cut != "" {
			lines, ids = append(lines, head+cut), append(ids, id)
		}
		break
	}
	return lines, ids, truncated
}

// fitPrefix returns the longest code-point prefix of s that fits (fits must be monotone).
func fitPrefix(s string, fits func(string) bool) string {
	runes := []rune(s)
	lo, hi := 0, len(runes) // the answer lies in [lo, hi]
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(string(runes[:mid])) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return strings.TrimSpace(string(runes[:lo]))
}

// estimateTokens is ceil(narrow/4 + wide) over the code points of s (wide: U+3000 and above).
func estimateTokens(s string) int {
	narrow, wide := 0, 0
	for _, r := range s {
		if r >= 0x3000 {
			wide++
		} else {
			narrow++
		}
	}
	return int(math.Ceil(float64(narrow)/4 + float64(wide)))
}

// arrayMember returns the elements of an array member, each with its bytes.
func arrayMember(obj *object, key string) ([][]byte, bool) {
	raw, ok := obj.get(key)
	if !ok {
		return nil, false
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil || elems == nil {
		return nil, false
	}
	out := make([][]byte, len(elems))
	for i, e := range elems {
		out[i] = e
	}
	return out, true
}

// prefixPrompt prepends prompt to a completions prompt: a string, or each string of an array.
func prefixPrompt(raw json.RawMessage, prompt string) ([]byte, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil && isJSONString(raw) {
		return marshal(prompt + "\n\n" + s), nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, ErrFallbackRequest
	}
	out := make([][]byte, len(items))
	for i, it := range items {
		if !isJSONString(it) || json.Unmarshal(it, &s) != nil {
			return nil, ErrFallbackRequest // token prompts cannot take text
		}
		out[i] = marshal(prompt + "\n\n" + s)
	}
	return joinArray(out), nil
}

// prefixSystem prepends prompt to an Anthropic system: absent or null becomes the prompt, a string
// gets it in front, and an array of blocks gets it as a first text block.
func prefixSystem(raw json.RawMessage, prompt string) ([]byte, error) {
	if len(raw) == 0 || isNull(raw) {
		return marshal(prompt), nil
	}
	var s string
	if isJSONString(raw) && json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return marshal(prompt), nil
		}
		return marshal(prompt + "\n\n" + s), nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || blocks == nil {
		return nil, ErrFallbackRequest
	}
	first := marshal(anthropicText{Type: blockText, Text: prompt})
	out := make([][]byte, 0, len(blocks)+1)
	out = append(out, first)
	for _, b := range blocks {
		out = append(out, b)
	}
	return joinArray(out), nil
}

func isJSONString(raw json.RawMessage) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c == '"'
	}
	return false
}
