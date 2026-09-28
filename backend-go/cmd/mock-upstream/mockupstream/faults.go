package mockupstream

import (
	"io"
	"net/http"
	"time"
)

// envelope selects the error body shape of an endpoint family.
type envelope int

const (
	envOpenRouter envelope = iota // {"error":{"code","message","metadata"}}
	envTypeSafe                   // {"detail":{"error_type","message"}} / 422 {"detail":[…]}
	envOpenAI                     // {"error":{"message","type","param","code"}}
	envAnthropic                  // {"type":"error","error":{"type","message"}}
)

// htmlPage is what a site's catch-all route returns for a wrong API path.
const htmlPage = `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>OpenRouter</title></head>` +
	`<body><div id="__next">A unified interface for LLMs</div></body></html>`

type faultSpec struct {
	status   int
	errType  string
	message  string
	metadata map[string]any
	header   map[string]string
}

var faultTable = map[string]faultSpec{
	"402_inflight": {status: 402, errType: "payment_required", message: "Request would exceed this key's in-flight budget; retry shortly",
		metadata: map[string]any{"limit_source": "openrouter_in_flight_budget"}, header: map[string]string{"Retry-After": "1"}},
	"402_credits": {status: 402, errType: "payment_required", message: "Insufficient credits. Add more using https://openrouter.ai/settings/credits"},
	"429":         {status: 429, errType: "rate_limit_error", message: "Rate limit exceeded: retry shortly", header: map[string]string{"Retry-After": "1"}},
	"529":         {status: 529, errType: "overloaded_error", message: "The provider is overloaded"},
	"413":         {status: 413, errType: "request_too_large", message: "Request too large: it exceeds the 32000-token context of this model"},
	"400":         {status: 400, errType: "invalid_request_error", message: "Invalid request: questions.q1.type must be one of noul, choice, score"},
	"400_context": {status: 400, errType: "invalid_request_error", message: "This request exceeds the model's maximum context length of 32000 tokens"},
	"401":         {status: 401, errType: "authentication_error", message: "No auth credentials found"},
	"403":         {status: 403, errType: "permission_error", message: "This key is disabled"},
	"404":         {status: 404, errType: "not_found_error", message: "Not Found"},
	"404_data_policy": {status: 404, errType: "not_found_error",
		message: "No endpoints found matching your data policy (Zero data retention). Configure: https://openrouter.ai/settings/privacy"},
	"500": {status: 500, errType: "api_error", message: "Internal server error"},
	"502": {status: 502, errType: "api_error", message: "Bad gateway: the provider returned an invalid response"},
	"503": {status: 503, errType: "api_error", message: "Service unavailable"},
	"524": {status: 524, errType: "api_error", message: "A timeout occurred at the provider"},
}

// writeFault answers r with fault name, in the endpoint's envelope. It returns false for faults
// that only delay the normal answer (slow), and when the client left during a hang.
func (s *Server) writeFault(w http.ResponseWriter, r *http.Request, name string, env envelope) bool {
	switch name {
	case "":
		return false
	case "hang":
		if sleepCtx(r, s.cfg.HangMax) {
			http.Error(w, "mock-upstream: hang ended", http.StatusGatewayTimeout)
		}
		return true
	case "slow":
		sleepCtx(r, 2*time.Second)
		return false
	case "html200":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, htmlPage)
		return true
	case "bad_json":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"broken","answers":{"q":`)
		return true
	case "no_answers":
		writeJSON(w, http.StatusOK, map[string]any{"id": "dec_mock_empty", "model": "typesafe/jev-1.13-20260917"})
		return true
	case "422":
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []map[string]any{
			{"loc": []any{"body", "questions", "q1", "criteria"}, "msg": "field required", "type": "value_error.missing"},
		}})
		return true
	}
	f, ok := faultTable[name]
	if !ok {
		return false
	}
	for k, v := range f.header {
		w.Header().Set(k, v)
	}
	writeError(w, env, f.status, f.errType, f.message, f.metadata)
	return true
}

// writeError writes an error body in the envelope of env.
func writeError(w http.ResponseWriter, env envelope, status int, errType, message string, metadata map[string]any) {
	switch env {
	case envTypeSafe:
		writeJSON(w, status, map[string]any{"detail": map[string]any{"error_type": errType, "message": message}})
	case envOpenAI:
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": errType, "param": nil, "code": errType}})
	case envAnthropic:
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": errType, "message": message}})
	default:
		e := map[string]any{"code": status, "message": message}
		if metadata != nil {
			e["metadata"] = metadata
		}
		writeJSON(w, status, map[string]any{"error": e})
	}
}
