package evidence_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// supportDocs is about tokens tokens (4 characters each) of support documentation split into
// <document> blocks of about 2k tokens, as a RAG app puts them in its system prompt.
func supportDocs(tokens int) string {
	var b strings.Builder
	doc := 0
	for i := 1; b.Len() < tokens*4; i++ {
		if (i-1)%40 == 0 {
			if doc > 0 {
				b.WriteString("</document>\n")
			}
			doc++
			fmt.Fprintf(&b, "<document id=\"policy-%d.md\">", doc)
		}
		fmt.Fprintf(&b, "Section %d. Annual plans can be refunded within %d days of purchase; the form is at "+
			"https://help.acme.example/refunds/%d. Priority tickets are answered within %d hours. ", i, 10+i%50, i, 2+i%10)
	}
	b.WriteString("</document>")
	return b.String()
}

// BenchmarkExtract is evidence extraction (SPEC §2.10) from a chat request whose system prompt
// carries 8k or 32k tokens of documents, a tool result and a user message, with the default
// sources.
func BenchmarkExtract(b *testing.B) {
	for _, size := range []struct {
		name   string
		tokens int
	}{{"8k", 8000}, {"32k", 32000}} {
		body, err := json.Marshal(map[string]any{
			"model": "gpt-4o-mini",
			"messages": []map[string]any{
				{"role": "system", "content": "You are the Acme support assistant. Answer only from the documents below.\n\n" + supportDocs(size.tokens)},
				{"role": "user", "content": "Where is my order A-1043, and can I still return it?"},
				{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call_order", "type": "function",
					"function": map[string]any{"name": "get_order", "arguments": `{"order_id":"A-1043"}`}}}},
				{"role": "tool", "tool_call_id": "call_order", "content": "Order A-1043: shipped on 2026-09-24, delivered on 2026-09-26."},
			},
		})
		if err != nil {
			b.Fatal(err)
		}
		req, err := llmapi.ParseChatRequest(body)
		if err != nil {
			b.Fatal(err)
		}
		settings := evidence.DefaultSettings()
		b.Run("evidence="+size.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				r, err := evidence.Extract(req, &settings)
				if err != nil {
					b.Fatal(err)
				}
				if len(r.Items) < 2 {
					b.Fatalf("%d evidence items", len(r.Items))
				}
			}
		})
	}
}
