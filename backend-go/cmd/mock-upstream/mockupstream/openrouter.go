package mockupstream

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Decision endpoint behaviour.
const (
	// jevPricePerMTok is the reported price of decision calls (input only; output is free).
	jevPricePerMTok = 0.042
	// maxDecisionTokens is OpenRouter's combined state + questions limit.
	maxDecisionTokens = 32000
	// requestOverhead approximates the vendor's fixed per-request tokens.
	requestOverhead = 275
	// DefaultSnapshot answers typesafe/jev-1.13 and ~typesafe/jev-latest.
	DefaultSnapshot = "typesafe/jev-1.13-20260917"
)

// decisionRequest is a decision body as OpenRouter or TypeSafe receive it.
type decisionRequest struct {
	Model     string                     `json:"model"`
	SessionID string                     `json:"session_id"`
	State     json.RawMessage            `json:"state"`
	Questions map[string]json.RawMessage `json:"questions"`
	Provider  json.RawMessage            `json:"provider"`
}

// decisions serves a decision endpoint: OpenRouter's /api/alpha/decisions and /api/v1/systemone
// (openRouter: session_id, provider, usage.cost) or the TypeSafe-shaped /v1/systemone.
func (s *Server) decisions(env envelope, openRouter bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req decisionRequest
		decodeErr := json.Unmarshal(body, &req)
		if s.writeFault(w, r, s.fault(r, req.SessionID), env) {
			return
		}
		if msg := keyProblem(bearer(r)); msg.status != 0 {
			writeError(w, env, msg.status, msg.errType, msg.message, nil)
			return
		}
		if decodeErr != nil {
			s.invalid(w, env, "body", "the request body is not a JSON object: "+decodeErr.Error())
			return
		}
		if loc, msg := validateDecision(&req, openRouter); msg != "" {
			s.invalid(w, env, loc, msg)
			return
		}
		if tokens := len(body) / 4; tokens > maxDecisionTokens {
			writeError(w, env, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("Request too large: about %d tokens exceeds the %d-token context", tokens, maxDecisionTokens), nil)
			return
		}
		if !delay(r, s.cfg.JevLatency) {
			return
		}
		sc := s.scenarios[scenarioID(r)]
		answers := make(map[string]json.RawMessage, len(req.Questions))
		for qid, q := range req.Questions {
			if sc != nil && len(sc.MockAnswers[qid]) > 0 && string(sc.MockAnswers[qid]) != "null" {
				answers[qid] = sc.MockAnswers[qid]
				continue
			}
			answers[qid] = hashAnswer(qid, q, req.State)
		}
		qb, _ := json.Marshal(req.Questions)
		in := requestOverhead + (len(req.State)+len(qb))/4
		usage := map[string]any{"input_tokens": in, "output_tokens": 0}
		sum := sha256.Sum256(body)
		resp := map[string]any{"id": "dec_mock_" + hex.EncodeToString(sum[:8]), "model": s.snapshotFor(req.Model, r), "answers": answers, "usage": usage}
		if openRouter {
			usage["cost"] = float64(in) * jevPricePerMTok / 1e6
			resp["provider"] = "TypeSafe"
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

type problem struct {
	status           int
	errType, message string
}

// keyProblem is the mock's key policy: empty → 401; a key containing "invalid" → 401,
// "forbidden" → 403, "nocredits" → 402. Any other key works.
func keyProblem(key string) problem {
	switch {
	case key == "":
		return problem{http.StatusUnauthorized, "authentication_error", "No auth credentials found"}
	case strings.Contains(key, "invalid"):
		return problem{http.StatusUnauthorized, "authentication_error", "User not found."}
	case strings.Contains(key, "forbidden"):
		return problem{http.StatusForbidden, "permission_error", "This key is disabled"}
	case strings.Contains(key, "nocredits"):
		return problem{http.StatusPaymentRequired, "payment_required", "Insufficient credits. Add more using https://openrouter.ai/settings/credits"}
	}
	return problem{}
}

// invalid writes a validation error: OpenRouter 400, TypeSafe 422 with a detail list.
func (s *Server) invalid(w http.ResponseWriter, env envelope, loc, msg string) {
	if env == envTypeSafe {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []map[string]any{
			{"loc": append([]any{"body"}, toAny(strings.Split(loc, "."))...), "msg": msg, "type": "value_error"},
		}})
		return
	}
	writeError(w, env, http.StatusBadRequest, "invalid_request_error", "Invalid request: "+loc+": "+msg, nil)
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// validateDecision checks the documented request rules: a model, an object state, at least one
// question; per question a known type and instructions; noul criteria with both true and false;
// choice criteria with 1..255 options; score criteria with 1..10 levels (2..10 on TypeSafe).
func validateDecision(req *decisionRequest, openRouter bool) (loc, msg string) {
	if req.Model == "" {
		return "model", "field required"
	}
	var state map[string]any
	if len(req.State) == 0 || json.Unmarshal(req.State, &state) != nil || state == nil {
		return "state", "must be a JSON object"
	}
	if len(req.Questions) == 0 {
		return "questions", "at least one question is required"
	}
	for qid, raw := range req.Questions {
		var q struct {
			Type         string          `json:"type"`
			Instructions json.RawMessage `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
		}
		if json.Unmarshal(raw, &q) != nil {
			return "questions." + qid, "must be an object"
		}
		if len(q.Instructions) == 0 || string(q.Instructions) == "null" {
			return "questions." + qid + ".instructions", "field required"
		}
		switch q.Type {
		case "noul":
			if len(q.Criteria) > 0 && string(q.Criteria) != "null" {
				var c map[string]json.RawMessage
				if json.Unmarshal(q.Criteria, &c) != nil || c["true"] == nil || c["false"] == nil {
					return "questions." + qid + ".criteria", "a noul criteria object needs both true and false"
				}
			}
		case "choice":
			var c map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &c) != nil || len(c) == 0 || len(c) > 255 {
				return "questions." + qid + ".criteria", "a choice needs 1 to 255 options"
			}
		case "score":
			var c []json.RawMessage
			least := 2
			if openRouter {
				least = 1
			}
			if json.Unmarshal(q.Criteria, &c) != nil || len(c) < least || len(c) > 10 {
				return "questions." + qid + ".criteria", fmt.Sprintf("a score needs %d to 10 levels", least)
			}
		default:
			return "questions." + qid + ".type", "must be one of noul, choice, score"
		}
	}
	return "", ""
}

// snapshotFor is the model id a decision response reports.
func (s *Server) snapshotFor(model string, r *http.Request) string {
	if v := r.Header.Get("X-Mock-Snapshot"); v != "" {
		return v
	}
	s.mu.Lock()
	snap := s.snapshot
	s.mu.Unlock()
	if snap != "" {
		return snap
	}
	switch model {
	case "typesafe/jev-1.13", "~typesafe/jev-latest":
		return DefaultSnapshot
	case "jev-latest", "jev-1.13", "typesafe-ai/jev":
		return "jev-1.13.0"
	}
	return model
}

// hashAnswer derives a deterministic answer from the question id and the state, rounded to two
// decimals like the vendor's.
func hashAnswer(qid string, rawQ, state json.RawMessage) json.RawMessage {
	h := sha256.New()
	h.Write([]byte(qid))
	h.Write([]byte{0})
	h.Write(state)
	sum := h.Sum(nil)
	u := func(i int) float64 { return float64(binary.BigEndian.Uint64(sum[8*i:8*i+8])>>11) / (1 << 53) }
	var q struct {
		Type     string          `json:"type"`
		Criteria json.RawMessage `json:"criteria"`
	}
	_ = json.Unmarshal(rawQ, &q)
	var a map[string]any
	switch q.Type {
	case "choice":
		var c map[string]json.RawMessage
		_ = json.Unmarshal(q.Criteria, &c)
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		top := keys[int(u(1)*float64(len(keys)))%len(keys)]
		probs, pTop := spread(keys, top, u(2))
		a = map[string]any{"type": "choice", "choice": top, "confidence": round2(math.Max(pTop-0.07, 0)), "probabilities": probs}
	case "score":
		var c []json.RawMessage
		_ = json.Unmarshal(q.Criteria, &c)
		keys := make([]string, len(c))
		legend := map[string]json.RawMessage{}
		for i := range c {
			keys[i] = strconv.Itoa(i)
			legend[keys[i]] = c[i]
		}
		top := keys[int(u(1)*float64(len(keys)))%len(keys)]
		probs, pTop := spread(keys, top, u(2))
		var score float64
		for i, k := range keys {
			score += float64(i) * probs[k]
		}
		a = map[string]any{"type": "score", "score": round2(score), "confidence": round2(math.Max(pTop-0.07, 0)), "probabilities": probs, "legend": legend}
	default:
		p := round2(math.Pow(u(0), 3))
		a = map[string]any{"type": "noul", "noul": p, "confidence": round2(math.Abs(p-0.5) + 0.45)}
	}
	b, _ := json.Marshal(a)
	return b
}

// spread puts 0.55..0.95 on top and shares the rest evenly, each rounded to two decimals.
func spread(keys []string, top string, u float64) (map[string]float64, float64) {
	pTop := round2(0.55 + 0.4*u)
	if len(keys) == 1 {
		pTop = 1
	}
	rest := round2((1 - pTop) / float64(max(len(keys)-1, 1)))
	out := make(map[string]float64, len(keys))
	for _, k := range keys {
		out[k] = rest
	}
	out[top] = pTop
	return out, pTop
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// handleKey serves GET /api/v1/key.
func (s *Server) handleKey(w http.ResponseWriter, r *http.Request) {
	if s.writeFault(w, r, s.fault(r, ""), envOpenRouter) {
		return
	}
	key := bearer(r)
	if p := keyProblem(key); p.status == http.StatusUnauthorized || p.status == http.StatusForbidden {
		writeError(w, envOpenRouter, p.status, p.errType, p.message, nil)
		return
	}
	data := map[string]any{
		"label": maskKey(key), "usage": 0.0123, "limit": nil, "limit_remaining": nil, "is_free_tier": false,
		"is_provisioning_key": false, "rate_limit": map[string]any{"requests": 1000, "interval": "10s"},
	}
	if strings.Contains(key, "nocredits") {
		data["limit"], data["usage"], data["limit_remaining"] = 1.0, 1.0, 0.0
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func maskKey(k string) string {
	if len(k) <= 10 {
		return "sk-…"
	}
	return k[:10] + "…" + k[len(k)-3:]
}

// handleAuthorize is the mock of openrouter.ai/auth: it issues a code for the PKCE challenge and
// redirects to callback_url?code=….
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cb, err := url.Parse(q.Get("callback_url"))
	if err != nil || (cb.Scheme != "http" && cb.Scheme != "https") || cb.Host == "" {
		http.Error(w, "mock-upstream: callback_url must be an absolute http(s) URL", http.StatusBadRequest)
		return
	}
	challenge, method := q.Get("code_challenge"), q.Get("code_challenge_method")
	if method == "" {
		method = "plain"
	}
	if challenge == "" || (method != "S256" && method != "plain") {
		http.Error(w, "mock-upstream: code_challenge and code_challenge_method (S256|plain) are required", http.StatusBadRequest)
		return
	}
	var nonce [12]byte
	_, _ = rand.Read(nonce[:])
	code := "mockcode_" + hex.EncodeToString(nonce[:])
	s.mu.Lock()
	s.codes[code] = method + "|" + challenge
	s.mu.Unlock()
	v := cb.Query()
	v.Set("code", code)
	cb.RawQuery = v.Encode()
	http.Redirect(w, r, cb.String(), http.StatusFound)
}

// handleAuthKeys serves POST /api/v1/auth/keys: it exchanges a code issued by /auth for a key
// when the code_verifier matches the challenge. Codes are single-use.
func (s *Server) handleAuthKeys(w http.ResponseWriter, r *http.Request) {
	if s.writeFault(w, r, s.fault(r, ""), envOpenRouter) {
		return
	}
	var req struct {
		Code                string `json:"code"`
		CodeVerifier        string `json:"code_verifier"`
		CodeChallengeMethod string `json:"code_challenge_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, envOpenRouter, http.StatusBadRequest, "invalid_request_error", "code is required", nil)
		return
	}
	s.mu.Lock()
	stored, ok := s.codes[req.Code]
	delete(s.codes, req.Code)
	s.mu.Unlock()
	if !ok {
		writeError(w, envOpenRouter, http.StatusBadRequest, "invalid_request_error", "Invalid code", nil)
		return
	}
	method, challenge, _ := strings.Cut(stored, "|")
	got := req.CodeVerifier
	if method == "S256" {
		sum := sha256.Sum256([]byte(req.CodeVerifier))
		got = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	if req.CodeVerifier == "" || got != challenge || req.CodeChallengeMethod != "" && req.CodeChallengeMethod != method {
		writeError(w, envOpenRouter, http.StatusForbidden, "permission_error", "Invalid code_verifier", nil)
		return
	}
	var k [16]byte
	_, _ = rand.Read(k[:])
	writeJSON(w, http.StatusOK, map[string]any{"key": "sk-or-v1-mock-" + hex.EncodeToString(k[:]), "user_id": "user_mock"})
}
