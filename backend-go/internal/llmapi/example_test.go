package llmapi_test

import (
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/truthtable/backend-go/internal/llmapi"
)

// upstreamStream is a two-token OpenAI chat stream.
const upstreamStream = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Refunds take "},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"60 days."},"finish_reason":"stop"}]}

data: [DONE]

`

// Passthrough streaming (route streaming: passthrough): every event reaches the client as it
// arrives, the terminal event waits for the decision, and the decision goes out as a comment and
// as trailers.
func ExampleRelay() {
	format := llmapi.FormatForPath("/v1/chat/completions")
	w := httptest.NewRecorder() // the client's http.ResponseWriter
	sw := llmapi.NewStreamWriter(w)
	sw.DeclareTrailers("X-TrustAgent-Action", "X-TrustAgent-Mode")

	asm, _ := llmapi.NewStreamAssembler(format)
	res := llmapi.Relay(llmapi.NewReader(strings.NewReader(upstreamStream)), asm, sw)

	gated, _ := asm.Response().Gated()
	fmt.Printf("gate %q (finished %v, held %v)\n", gated.Text, asm.Finished(), res.Held)

	// ... decide, with the route deadline ...
	_ = sw.Finish(&llmapi.Annotation{TraceID: "t1", Action: "flag", WouldAction: "flag", Status: "ok", Reasons: []string{"groundedness"}},
		map[string]string{"X-TrustAgent-Action": "flag", "X-TrustAgent-Mode": "flag"})
	body := w.Body.String()
	fmt.Println(body[strings.LastIndex(body, "\n\n: ")+2:])
	// Output:
	// gate "Refunds take 60 days." (finished true, held true)
	// : trustagent {"trace_id":"t1","action":"flag","would_action":"flag","status":"ok","verdict":null,"max_risk":null,"reasons":["groundedness"]}
	//
	// data: [DONE]
}

// Buffer streaming (route streaming: buffer) that ends in a block: the upstream answer is read
// whole, then a block stream replaces it.
func ExampleCollect() {
	format := llmapi.FormatOpenAIChat
	w := httptest.NewRecorder()
	sw := llmapi.NewStreamWriter(w)

	asm, _ := llmapi.NewStreamAssembler(format)
	res := llmapi.Collect(llmapi.NewReader(strings.NewReader(upstreamStream)), asm, llmapi.CollectOptions{})
	fmt.Println("collected", len(res.Events), "events, terminated:", res.Terminated)

	// ... the decision is block ...
	events := llmapi.BlockStream(format, llmapi.Block{Message: "I can't confirm that."},
		llmapi.BlockTarget{TraceID: "t1", Response: asm.Response()})
	_ = sw.WriteEvents(format, events, &llmapi.Annotation{TraceID: "t1", Action: "block", WouldAction: "block", Status: "ok"})

	check, _ := llmapi.NewStreamAssembler(format)
	for r := llmapi.NewReader(w.Body); ; {
		ev, err := r.Next()
		if err != nil {
			break
		}
		check.Add(ev)
	}
	c, _ := check.Response().Gated()
	fmt.Printf("client sees %q, finish_reason %s\n", c.Text, c.FinishReason)
	// Output:
	// collected 3 events, terminated: true
	// client sees "I can't confirm that.", finish_reason content_filter
}

// The fallback request is built from the original body; stream is forced off.
func ExampleBuildFallbackRequest() {
	original := []byte(`{"model":"claude-sonnet-4-5","max_tokens":512,"system":"Be brief.","stream":true,
		"messages":[{"role":"user","content":"Refund after 60 days?"}]}`)
	req, _ := llmapi.BuildFallbackRequest(llmapi.FormatAnthropicMessages, original, llmapi.Fallback{
		SystemPrompt: "Answer only from CONTEXT.",
		Evidence:     []llmapi.EvidenceLine{{ID: "refunds", Text: "Refunds within 30 days."}},
	})
	fmt.Println(string(req.Body))
	// Output:
	// {"model":"claude-sonnet-4-5","max_tokens":512,"system":"Answer only from CONTEXT.\n\nCONTEXT:\n<context>\n[refunds] Refunds within 30 days.\n</context>\n\nBe brief.","stream":false,"messages":[{"role":"user","content":"Refund after 60 days?"}]}
}
