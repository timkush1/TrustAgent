# TrustAgent × Jev: integration design (researched 2026-09-27, read-only)

**How to read this.**
- **[V]** means I re-checked the fact at its URL today.
- A fact with no marker comes from the three input reports, and its URL is given.
- **UNVERIFIED** means no source confirms it.
- **ESTIMATE** means my own arithmetic from the assumptions stated next to it.
- All code below is a design sketch. None of it has been compiled or run.
- No repo files were changed.

---

## 0. Summary

1. **Tier-1 gate in Go.**
   - Two new packages: `internal/jev` (the Jev adapter) and `internal/gate` (the policy logic).
   - Each gated response gets **one** Jev call with **4 questions**: 2 yes/no questions (`noul`), 1 `choice`, and 1 `score` that is shown on the dashboard but never used for policy.
   - Each route has a policy mode: `observe`, `flag` or `block`.
   - Every call has a hard deadline, a circuit breaker and a request-rate limiter. If Jev fails, the gate lets the response through by default ("fail-open").
2. **Groundedness is only judged when there is context.** Without source text in `state`, Jev's ranking inverts: it scored fabricated legal citations higher than honest ones (3.21 vs 2.79, r = +0.02). With the sources present it reached AUC 0.9962 [V][haqq]. So with no context, the gate records `not_assessed` and leaves the judgment to the async audit.
3. **Tier-2 cascade in Python** (`verifier_mode=cascade`).
   - Jev answers a **4-option** Choice per claim.
   - The Choice has 4 options: `supported`, `partially_supported`, `contradicted` and `not_addressed`. UNKNOWN is not one of them. The repo reserves it for parse failures (`verifier.py:68-70`), and a generic "unknown" option tends to attract answers (reported 95% selection on KoBBQ [awesome]).
   - The LLM judge (Haiku, or Ollama as fallback) only sees claims that are low-confidence, contain numbers or dates, are partial, or are contradicted and need an explanation.
4. **KB (knowledge base) uses.** Jev replaces the LLM in three places: the Gate-1 entailment check at ingest, pairwise contradiction checks (one call for all candidate pairs instead of `top_k` LLM calls), and a new screen for prompt injection hidden inside uploaded documents.
5. **Safety of the rollout.** Everything is off by default. The Ollama/Haiku path is unchanged. CI uses deterministic mock Jev clients. The eval harness reports Jev-only, LLM-only and cascade side by side, with precision/recall/F1/AUROC/ECE, latency and $ per 1k audits.

---

## 1. Facts this design depends on

| Fact | Source |
|---|---|
| OpenRouter endpoints: `POST https://openrouter.ai/api/alpha/decisions` (Decisions) and `POST https://openrouter.ai/api/v1/systemone` (TypeSafe-compatible). Model ids `typesafe/jev-1.13` and `~typesafe/jev-latest`. Context "32,000 tokens. That's the `state` you send plus the questions." Output tokens are free. | [V][orhub] |
| TypeSafe direct: "64k tokens per request; 32k tokens for `state` plus the longest question". Rate limits "250,000 tokens per second / 1,200 requests per minute", adjusted dynamically. Price $0.042 per million input tokens. Guidance: pin a version if you tuned thresholds on it. | [V][models] |
| Question types can be mixed in one call. Questions are "evaluated in parallel and in isolation". "Adding questions barely changes the response time." | [V][intro] |
| Batching 13 questions over a ~54k-character document: "12.2x cheaper, 10.0x faster" than 13 separate calls, with mostly identical answers (std dev 0.0). | [V][par] |
| Choice allows at most 255 options. Score needs "at least two levels; the API accepts up to 10". Question ids are "not sent to the underlying model". Documented errors: 401, 422, 429, 529. | [V][api] |
| OpenRouter schema differences: if a noul `criteria` is sent it must have both `true` and `false`; Score `minItems: 1`; `confidence`, `probabilities` and `legend` are **optional** in answers; extra error codes 400/402/403/404/413/429/500/502/503/524/529. | [V][orapi] |
| Known jev-1.13 weaknesses: does not treat state as hostile by default (#6); accuracy falls as unrelated content grows (#5); "not a calculator" (#2); reads dates as text (#3); no guaranteed structural invariants between questions (#8). | [V][jag] |
| Citation-check cookbook: a 3-way Choice (`supports`, `contradicts`, `says_nothing`) with `AUTO_ACCEPT = 0.8`. | [V][cite] |
| Arize on RAGTruth: accuracy 87% at a tuned 0.80 cutoff vs 76% at 0.5; "34% of perfectly grounded responses score above 0.5"; AUC 0.94; ~141 ms; $0.05 vs $14.30 per 1k judgments; possible training contamination. | [V][arize] |
| Vercel AI Gateway has confidence-based evaluation fallbacks (`when: {question, confidenceBelow}`). Its error shape is `{"message","error_type"}`. | [V][vercel] |
| Probabilities are rounded to 2 decimals; key order changes between calls. | [aisdk][orblog] |
| Documented (probabilities → confidence) pairs: {0.85, 0.15, 0} → 0.78; {0.88, 0.12, 0} → 0.81; {0.61, 0.35, 0.04} → 0.42; {0.40, 0.34, 0.24, 0.02} → 0.20. The formula is not published. | [qs][api][choice][conf] |
| OpenRouter model page: P50 latency 0.23 s. Independent measurements: 292 ms median, p90 374 ms [V][haqq]; 0.8–0.9 s median from a laptop, with 10–35 s tails at 100 requests in flight [kumar]; "~15% of calls hang" on the alpha endpoint (one anecdote) [pydai]. | [ormodel] |
| Independent calibration on out-of-distribution data (900 items): ECE 0.107. Choice is over-confident, noul/Boolean under-confident, Score has 44.7% accuracy and ECE 0.325. | [ood] |

---

## 2. Where it plugs into the repo

| Location | What exists today | Jev hook |
|---|---|---|
| `backend-go/internal/proxy/handler.go:220-252` | Non-streaming path reads the whole upstream body, then calls `c.Data(...)` at :251 | Run the gate before `c.Data`, then set headers or apply a block |
| `handler.go:168-217` | Streaming passes 1024-byte chunks straight through; the audit is submitted at EOF (:188-209) | The gate runs at EOF. `flag` mode uses trailers or an SSE comment. `block` mode requires buffering |
| `handler.go:128,135-139` | Forwards the raw request body and **all** client headers upstream | Strip `X-TrustAgent-*` headers and the `trustagent` body extension before forwarding |
| `internal/grpc/client.go:57-61` | `Evaluate` sends only `RequestId`, `Query` and `Response`; `AuditRequest.context` (proto field 4) is never filled | Pass the gate's context documents plus the gate decision |
| `graphs/nodes/retriever.py:102-115` | If context is supplied by the caller, retrieval is skipped | The deep audit reuses exactly what the gate saw |
| `graphs/nodes/verifier.py:179-222` | LLM judge per claim, concurrency 5 | Becomes the escalation path of `CascadeVerifierNode` |
| `graphs/nodes/scorer.py:59-60, 99-103` | Final score is weighted by confidence; hallucination if CONTRADICTED/UNSUPPORTED with confidence > 0.7 | **Jev's `confidence` is a different statistic** (see 4b) |
| `kb/ingestion.py:148-160` (threshold 0.7 at :86) and `kb/contradiction.py:86-131` (top_k=5, one LLM call per candidate) | LLM-based Gate-1 and pair checks | Jev Gate-1 and one batched contradiction call |
| `internal/worker/pool.go:69-76` | Silently drops jobs when the queue is full | Two priority lanes driven by the gate |
| `evals/run_eval.py:78-134`, `evals/metrics.py` | Already computes AUROC and ECE | Add `--verifier llm\|jev\|cascade` and cost/latency reporting |
| `proto/evaluator.proto:40-49, 96-101, 196-202` | No UNKNOWN value (maps to UNSPECIFIED) | New fields (§10.3); edit the proto only, then run `make proto` |

---

## 3. Architecture

```
client ──► Go proxy ──► upstream LLM
             │  (response captured)
             ├─► [tier-1] gate.Evaluate ──HTTP──► Jev (OpenRouter /api/alpha/decisions)
             │        │  deadline · hedge · breaker · RPS limiter · fail-open
             │        ├─ policy(route): observe | flag (headers/trailers) | block (fallback)
             │        └─ WS "gate_decision" ─► dashboard   (+ Postgres gate_decisions)
             └─► worker pool (priority from gate) ──gRPC AuditRequest{context, gate}──► Python
                    decompose(LLM) → retrieve(skip if context) → verify[cascade: Jev → escalate → LLM] → score
                    └─► WS "audit_result" (+cascade stats) ─► dashboard
KB upload ──► Python ingest: decompose(LLM) → Jev injection screen → Jev Gate-1 → Jev contradiction batch
```

---

## 4a. Tier-1 gateway gate (Go)

### When the gate runs, by mode

| Mode | Non-streaming | Streaming |
|---|---|---|
| `observe` | Evaluated asynchronously after the response is sent. **Adds no latency.** | Same |
| `flag` | Synchronous, bounded by `deadline`. Writes `X-TrustAgent-*` headers. On timeout: `X-TrustAgent-Gate: pending`, and the decision still reaches the dashboard. | Runs at upstream EOF. Result goes out as HTTP trailers declared up front, and as an SSE comment line `: trustagent {"gate":"flag","risk":0.87}` written **before** `data: [DONE]`. SSE parsers ignore comments. This needs a line-aware SSE writer in place of the current raw chunk copy. Only the end of the stream is delayed. How many clients read trailers is **UNVERIFIED**. |
| `block` | Synchronous. On block, apply `on_block`: `fallback_message` (an OpenAI-shaped 200 with the configured text), `retry_upstream` (one retry with a grounding system message or an alternate model, gated again), or `error` (HTTP 422, `{"error":{"type":"trustagent_blocked",...}}`) | Requires `stream_policy: "buffer"`: collect the whole SSE stream, gate it, then replay or substitute. Time-to-first-token becomes full generation time plus the gate, so this is opt-in. Without `buffer`, the gate downgrades to `flag`. |

Response headers:
- `X-TrustAgent-Gate: pass|flag|block|fallback|not_assessed|pending|skipped|error`
- `X-TrustAgent-Risk`
- `X-TrustAgent-Contradiction-Risk`
- `X-TrustAgent-Category`
- `X-TrustAgent-Gate-Model`
- `X-TrustAgent-Gate-Latency-Ms`
- `X-TrustAgent-Audit-Id` (links the response to its deep audit)

### Where context comes from, per route: `context_from`
1. `extension`: a `trustagent.context` array in the request body. It is **stripped** before the request is forwarded, so the upstream never sees it. Whether upstreams would tolerate the unknown field is **UNVERIFIED**, which is why it is stripped.
2. `tool_messages`: `role:"tool"` messages in the conversation.
3. `system`: off by default, because system prompts are instructions, not evidence.
4. `kb`: a new gRPC `RetrieveContext` call to the Python engine, which already has the embedding model and hybrid retrieval. It has its own 150 ms deadline. Its latency on your hardware is **UNVERIFIED**.

If no context is found, the gate asks only the context-free questions and records `not_assessed` for groundedness.

### State and questions: one call, 4 questions (OpenRouter Decisions body)

```json
{
  "model": "typesafe/jev-1.13",
  "session_id": "<request_id>",
  "state": {
    "user_request": "What is the refund window for annual plans?",
    "system_instructions": "You are Acme support. Answer only from the policy excerpts.",
    "reference_context": [
      {"id": "kb-112", "text": "Annual plans can be refunded within 30 days of purchase."},
      {"id": "kb-087", "text": "Monthly plans are non-refundable."}
    ],
    "assistant_response": "Annual plans can be refunded within 60 days, and monthly plans get a prorated refund."
  },
  "questions": {
    "unsupported_content": {
      "type": "noul",
      "instructions": "Does `assistant_response` contain any factual statement that is not stated in or directly implied by `reference_context`?",
      "criteria": {
        "true": "At least one factual statement in `assistant_response` is missing from `reference_context` or disagrees with it",
        "false": "Every factual statement in `assistant_response` is stated in or directly implied by `reference_context`"
      }
    },
    "contradicts_context": {
      "type": "noul",
      "instructions": "Does `reference_context` say that something stated in `assistant_response` is false?",
      "criteria": {
        "true": "`reference_context` states the opposite of at least one statement in `assistant_response`",
        "false": "Nothing in `assistant_response` is contradicted by `reference_context`"
      }
    },
    "risk_category": {
      "type": "choice",
      "instructions": "Which description best fits `assistant_response` as an answer to `user_request`, judged only against `reference_context`?",
      "criteria": {
        "grounded":           {"what": "Answers `user_request`; its facts come from `reference_context`", "not_for": "Answers that add facts the context lacks"},
        "unsupported_claims": {"what": "Adds facts that `reference_context` does not contain, without contradicting it", "not_for": "Facts the context says are false"},
        "contradiction":      {"what": "States something that `reference_context` says is false", "not_for": "Facts merely missing from the context"},
        "off_topic":          {"what": "Does not address what `user_request` asks", "not_for": "Refusals"},
        "refusal":            {"what": "Declines or says it cannot answer", "not_for": "Answers that address the request"},
        "unsafe":             {"what": "Gives harmful content or clearly violates `system_instructions`", "not_for": "Safe answers that have factual errors"},
        "no_factual_content": {"what": "Greeting, small talk or a clarifying question with no factual statements", "not_for": "Answers with factual statements"}
      }
    },
    "faithfulness_display": {
      "type": "score",
      "instructions": "Which situation describes how `assistant_response` relates to `reference_context`?",
      "criteria": [
        "A statement in `assistant_response` is contradicted by `reference_context`",
        "Most statements in `assistant_response` do not appear in `reference_context`",
        "Some statements appear in `reference_context` and some do not",
        "Every statement appears in `reference_context` except a minor detail",
        "Every statement is stated in or directly implied by `reference_context`"
      ]
    }
  }
}
```

**Why the questions look like this**
- Each noul is phrased so that a high value means "yes, there is a risk" [noul]. Its criteria give both `true` and `false`, as OpenRouter requires.
- Option keys are descriptive words, because renaming options has been reported to flip answers [arxiv758].
- The options carry `what` / `not_for` descriptions, and the Choice includes an escape option (`no_factual_content`) [choice].
- The Score levels describe situations rather than degrees [score]. It is kept for **display only**: Score was the worst-calibrated type in the OOD test [ood], and the docs warn against reading numbers back out of a Score [jag]. The API returns levels 0..4, so display = level + 1, i.e. "1–5".
- The noul and the Choice overlap on purpose. Code never assumes they agree (jaggedness #8); when they disagree, that is treated as an uncertainty signal.

**Context-free variant** (no context available):
- `answers_request` (noul): "Does `assistant_response` address what `user_request` asks?"
- `is_refusal` (noul)
- `risk_category` restricted to {`answers`, `off_topic`, `refusal`, `unsafe`, `no_factual_content`}
- Groundedness is recorded as `not_assessed`.

### Token and latency budget
- **Budget: at most 24,000 estimated tokens per call.** That is 75% of OpenRouter's 32k combined limit.
- **Fixed overhead:** about 275 tokens. This is inferred from a one-sentence state with one noul costing 275 input tokens [orsdk].
- **Caps per part:**
  - `user_request`: 1.5k
  - `system_instructions`: 0.8k
  - questions: about 0.6k (**ESTIMATE**)
  - `assistant_response`: 6k. Longer responses get `not_assessed:too_long` and are left to the deep audit.
  - `reference_context`: packs the rest, ordered by retrieval score, then by lexical overlap with the response. This also follows jaggedness #5: filter first.
- **Estimating tokens:** chars/4 (**UNVERIFIED**; the tokenizer is "Other" and there is no counting endpoint). The ratio self-corrects from `usage.input_tokens` using an EWMA. On 413, or a 400 about size, halve the context once and retry.
- **Cost:** typically 4–6k tokens, about $0.00017–0.00025 per gated response, or about **$0.2 per 1k requests**. Worst case 24k tokens ≈ $0.001. (**ESTIMATE**, at $4.2e-8 per token [orep].)
- **Latency:** P50 0.23 s [ormodel]. p90 is about 0.37 s [haqq]. From a laptop it can be 0.8–0.9 s with long tails [kumar].
  - `flag` deadline defaults to 600 ms.
  - `block` deadline defaults to 1500 ms, with a **hedged duplicate request after 400 ms**. The duplicate costs about $0.0002, which is negligible.
  - Re-derive both from the p95 measured from your own deployment region. TypeSafe says its service is on the US West Coast [blog].

### From calibrated probabilities to policy (all decided in code)
- `risk = noul(unsupported_content)`
- `contradiction_risk = noul(contradicts_context)`
- `category = choice`, with its `confidence` and `probabilities`

| Knob | Default | Basis |
|---|---|---|
| `flag_risk` | 0.80 | Arize's tuned RAGTruth cutoff (0.5 was badly wrong) [V][arize]. Their question wording differs, so re-tune. |
| `flag_contradiction` | 0.70 | RAG-passage cookbook `contradicts > 0.70` [rag] |
| block rule | Block only when **both** `contradiction_risk ≥ 0.95` **and** `category == contradiction` with confidence ≥ 0.90. If the route enables it, also `category == unsafe` with confidence ≥ 0.90. | "> 0.9 for high-stakes action" [conf]. Two agreeing signals are required because answers are not guaranteed consistent across questions [V][jag]. |
| disagreement | `risk ≥ 0.8` while `category == grounded` with confidence ≥ 0.7, or the reverse | Leads to `flag` plus a high-priority deep audit |

**Calibration practice**
- Thresholds are operating points chosen from the eval harness's precision-recall sweep on **our own** labels.
- They are keyed by the snapshot id returned in the response (`typesafe/jev-1.13-20260917` [ortut]).
- The threshold grid is 0.01, because probabilities are rounded to 2 decimals.
- Never reuse a noul threshold for a Choice [jag].
- Optionally, fit an isotonic recalibration offline. It is stored as a 101-point lookup table so Go and Python apply the same one.
- Vendor calibration is a claim about groups of predictions, not individual answers [s1]. Independent ECE is 0.107 [ood]. So every threshold should come with a reliability diagram (§12).

```go
func decide(p *RoutePolicy, d *Decision) Action {
	if d.Risk == nil { return ActionNotAssessed }
	flag := *d.Risk >= p.FlagRisk || gte(d.ContradictionRisk, p.FlagContradiction) || d.Disagreement
	block := p.Mode == ModeBlock &&
		((gte(d.ContradictionRisk, p.BlockContradiction) && d.Category == "contradiction" && gte(d.CategoryConfidence, p.BlockMinConfidence)) ||
		 (p.BlockUnsafe && d.Category == "unsafe" && gte(d.CategoryConfidence, p.BlockMinConfidence)))
	switch {
	case block:                     return ActionBlock
	case flag && p.Mode != ModeObserve: return ActionFlag
	default:                        return ActionPass
	}
}
```

**Operational guards**
- Token-bucket limiter at `TRUTHTABLE_JEV_MAX_RPS=18`, to stay under TypeSafe's published 1,200 RPM [V][models]. OpenRouter publishes no limit for this model.
- Observe-mode routes are sampled.
- Circuit breaker opens on 5 consecutive failures or timeouts, and on any 401/403 or non-transient 402.

---

## 4b. Tier-2 claim-level cascade (Python)

**New pieces**
- `CascadeVerifierNode(jev, llm, cfg)` replaces `VerifierNode` when `verifier_mode=cascade`.
- `build_audit_graph(..., verifier_mode, jev_client)`.
- `jev_verify_ms` and `llm_escalate_ms` added to `step_timings`.

**Default request layout: one call per claim** (the cookbook's verified layout [V][cite])

```json
{"model":"typesafe/jev-1.13","session_id":"<request_id>:c3",
 "state":{"claim":"The Eiffel Tower was completed in 1889.","section":"[Document 1] ... [Document 2] ..."},
 "questions":{"relation":{"type":"choice","instructions":"How does the section relate to the claim?",
  "criteria":{
   "supported":{"what":"The section states the claim or directly implies that it is true","not_for":"Claims where only some parts are confirmed"},
   "partially_supported":{"what":"The section confirms some parts of the claim and does not mention the others","not_for":"Claims where any part is stated to be false"},
   "contradicted":{"what":"The section states the opposite of the claim, or of a part of it, or implies it is false","not_for":"Claims the section simply does not mention"},
   "not_addressed":{"what":"The section does not address what the claim asserts, either way","not_for":"Claims the section confirms or denies"}}}}}
```

**Batched layout** (one call per audit; claims become questions). Behind `jev_batch_mode=batched`:

```json
{"model":"typesafe/jev-1.13","state":{"user_query":"...","context":["[Document 1] ...","[Document 2] ..."]},
 "questions":{
  "c0":{"type":"choice","instructions":{"claim":"The Eiffel Tower was completed in 1889.","question":"How does `context` relate to `claim`?"},"criteria":{ "...same 4 options, with `context` in place of 'the section'..." }},
  "c1":{"type":"choice","instructions":{"claim":"It is 330 metres tall.","question":"How does `context` relate to `claim`?"},"criteria":{ "..." }}}}
```

- Each building block is documented: data held in instructions and referred to by sibling name in backticks, and state fields referred to by backticked path [api][prim]. **Using both in one question is UNVERIFIED**, so A/B test it in the eval before making it the default.
- Batching is allowed: the only documented bound is the token budget [models]. It sends 1 request per audit instead of N, which matters against a 1,200 RPM ceiling.
- `jev_max_questions_per_call` defaults to 16, split further on 400/413.
- **Drawback:** a shared context is a distractor for the claims it doesn't concern (jaggedness #5). That is the same union context the LLM verifier gets today (`verifier.py:26-31`).

**Mapping.** `supported`→SUPPORTED, `partially_supported`→PARTIALLY_SUPPORTED, `contradicted`→CONTRADICTED, `not_addressed`→UNSUPPORTED. UNKNOWN is produced only by code, after a Jev error and a failed LLM escalation. The "any contradicted part means contradicted" rule must be copied into `VERIFIER_SYSTEM_PROMPT`, or the two judges will disagree by definition.

**When a claim escalates to the LLM judge**

```python
NUMERIC = re.compile(r"\d|\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\b|\b(percent|million|billion|twice|half)\b", re.I)
def escalation_reason(claim: str, a: Answer | None, cfg) -> str | None:
    if a is None or a.choice not in VERDICT_MAP:           return "jev_error"
    if cfg.escalate_numeric and NUMERIC.search(claim):     return "numeric_or_date"   # jaggedness #2/#3
    conf = a.confidence if a.confidence is not None else max(a.probabilities.values(), default=0.0)  # confidence optional on OpenRouter
    if conf < cfg.accept_confidence.get(a.choice, 1.01):   return "low_confidence"    # default 0.8 (cookbook AUTO_ACCEPT)
    if cfg.explain == "all" or (cfg.explain == "contradicted" and a.choice == "contradicted"): return "explain"
    return None                                            # partially_supported threshold 1.01 => always escalates
```

- With no context, **skip Jev** (the result inverts) and keep today's LLM behaviour.
- Claims already destined for the LLM (numeric or date) start **in parallel** with the Jev call, so they don't also wait for it.
- `jev_shadow_rate=0.05`: a sample of Jev-accepted claims also goes to the LLM without affecting the result. This measures agreement and drift without labels.

**Confidence is not the same number in both judges.**
- Every documented pair has Jev `confidence` **below** the top probability (0.88 → 0.81, 0.61 → 0.42). The LLM's confidence is self-reported.
- For Jev-resolved claims, set `ClaimVerification.confidence = P(final verdict)` and store Jev's own `confidence` separately as `jev_confidence`, used only for the accept rule.
- The scorer's `> 0.7` hallucination rule (`scorer.py:99-103`) and its confidence weighting must be re-baselined in the eval. The golden baseline will move.

**Effect on cost and latency** (**ESTIMATE**)
- Assumptions: 8 claims per response; 3k-token context; ~450-token verifier system prompt; ~120 output tokens per claim.
- Prices: Haiku 4.5 at $1 / $5 per million input/output tokens (Anthropic model table in the claude-api reference, cached 2026-06-24; not re-fetched). Jev at $4.2e-8 per input token.

| Per 1k audits | LLM-only | Jev per-claim | Jev batched | Cascade at 30% escalation (per-claim Jev) |
|---|---|---|---|---|
| Verify step | ≈ $33 | ≈ $1.1 | ≈ $0.19 | ≈ $1.1 + $9.9 ≈ **$11** |
| + decompose (still LLM) | ≈ $2.5 | — | — | ≈ $2.5 |
| **Total** | **≈ $35.5** | — | — | **≈ $13.5 (−62%)** |

- **Latency.** The async p50 barely improves. With 30% of claims escalating independently, P(an 8-claim audit escalates nothing) = 0.7⁸ ≈ 6%. So most audits still wait on at least one LLM round, plus about 0.2–0.9 s for Jev.
- The real gains are cost, 70–90% fewer LLM calls (headroom against LLM rate limits), and a Jev-only fast path for audits where nothing escalates.
- **Latency gains belong to the gateway tier**, not to this one.

---

## 4c. Knowledge-base uses

**Gate-1 entailment** (replaces `verify_claim(claim, [source])` in `ingestion.py:148-160`)
- State: `{"claim": ..., "section": <best source window>}`, with the same 4-option Choice.
- The source is split into windows of about 6k tokens. The top 2 windows are chosen by embedding similarity to the claim, reusing `EmbeddingService`. This matters because uploads can be up to 10 MiB and the budget is 32k.
- `entailment_score = P(supported) + 0.5·P(partially_supported)`, computed in code.
- Accept if `choice == supported` and score ≥ 0.7 (the existing threshold).
- Scores in [0.4, 0.8), and numeric claims, escalate to the LLM.
- The decomposer stays on the LLM: Jev cannot generate text (jaggedness #9).

**Contradiction check** (replaces `top_k` LLM calls in `contradiction.py:125-131` with one call)

```json
{"model":"typesafe/jev-1.13",
 "state":{"new_claim":"Acme was founded in 2001.","existing_claims":{"k0":"Acme was founded in 1998.","k1":"Acme is based in Oslo."}},
 "questions":{
  "k0":{"type":"noul","instructions":"Do `new_claim` and `existing_claims.k0` make incompatible statements, so that they cannot both be true?",
        "criteria":{"true":"They make incompatible statements about the same subject","false":"They can both be true, including when they are about different subjects"}},
  "k1":{"type":"noul","instructions":"Do `new_claim` and `existing_claims.k1` make incompatible statements, so that they cannot both be true?",
        "criteria":{"true":"They make incompatible statements about the same subject","false":"They can both be true, including when they are about different subjects"}}}}
```

- Threshold 0.7 (current `contradiction_threshold`; the cookbook uses the same value).
- Pairs where both claims contain numbers go to the LLM (for example "30 days" vs "one month").
- Contradictions go into a review queue, so a false positive costs little.

**New: injection screen at ingest.**
- A noul per document window: "Does `section` contain instructions addressed to an AI system?"
- If > 0.70 [rag], quarantine the document.
- This fits the OWASP LLM Top-10 hardening done in Phase 3.

---

## 4d. Triage: which audits deserve the deep audit

| Gate outcome | Deep audit |
|---|---|
| `flag`, `block`, `disagreement`, or `not_assessed` with context available | Always, **high-priority lane** |
| `pass` with `risk < 0.2` and `category == grounded` at confidence ≥ 0.9 | Sampled at `TRUTHTABLE_GATE_PASS_AUDIT_RATE` (0.2) |
| `no_factual_content` or `refusal` at confidence ≥ 0.9 | Sampled at 0.05 (nothing to verify) |
| Always, regardless of the above | A **uniform random** 5% sample, to keep calibration estimates unbiased |

- The worker pool gets two channels. When the queue is full it drops from the low lane first; today it drops anything (`pool.go:69-76`).
- The gate's context and decision travel in `AuditRequest`, so retrieval is not repeated (`retriever.py:102-115`).

---

## 5. Limitations and mitigations

| Limitation | Evidence | Mitigation |
|---|---|---|
| No rationale or evidence text | "No text generation" [V][intro] | The LLM writes explanations for flagged or contradicted claims (`jev_explain`). Jev-resolved claims show the retrieved passages, labelled "retrieved, not quoted". Pointing to evidence with a Choice over passage ids is possible but its accuracy is **UNVERIFIED**. |
| Needs context | Inversion without sources [V][haqq] | No groundedness question without context: `not_assessed`. |
| 32k limit (OpenRouter) vs 64k/32k (TypeSafe) | [V][orhub][V][models] | 24k estimated budget; packing and filtering; self-calibrating chars-per-token; halve on 413. |
| Alpha, early-access API | `/api/alpha/`; one unofficial site calls it "deprecated" (**UNVERIFIED**) [jevpro] | The adapter supports both `/api/alpha/decisions` and `/api/v1/systemone`. Pin `typesafe/jev-1.13`; alert if the returned `model` changes. Everything is feature-flagged. |
| Proprietary, single provider | "hosted by one provider" [ormodel] | Off by default. The breaker falls back to LLM-only. Ollama and Haiku stay intact. CI runs on mocks. |
| Calibration holds only in aggregate and differs by question type | [s1][ood] | Thresholds per question type and per snapshot; optional isotonic layer; live reliability diagram; Score used only for display. |
| The audited response is attacker-influenced | "does not treat it as hostile by default" [V][jag] | Named fields; precise criteria; two agreeing signals before a block; the deep audit as backstop; injection cases added to the eval, next to `tests/unit/test_prompt_injection.py`. |
| Numbers and dates | [V][jag] | Numeric/date claims escalate. At the gateway, number-heavy responses are flagged, never blocked. |
| Wording and option-name sensitivity | [arxiv758] | Descriptive keys; question sets frozen and versioned (`gate-v1`, `claim-v1`); every wording change is re-evaluated. |
| Cannot abstain without an option for it | [langfuse] | `not_addressed` and `no_factual_content` options; no generic "unknown" option. |
| English first | [V][models]; Russian XNLI ran about 11 points lower [devto] | Non-English routes are treated as `not_assessed`, or escalate. |
| Latency tails and hangs | [kumar][pydai] | Hard deadline, hedged request, breaker, fail-open. |
| Rate limits | 1,200 RPM [V][models]; OpenRouter unpublished | RPS limiter, batched mode, sampling of observe routes. |
| Privacy (third-party data flow) | OpenRouter page data shows `training:false, retainsPrompts:false`; ZDR (zero data retention) is enterprise-only at TypeSafe [models][legal] | Per-route opt-out; optional PII redaction; `provider:{zdr:true}` flag (whether it is honoured is **UNVERIFIED**); keys stay server-side and never reach the dashboard [ortut]. |
| Proxy labels are not ground truth | — | Human-labelled subset plus public datasets in the eval; LLM agreement shown as "agreement", never as "accuracy". |

---

## 6. Configuration

**Go** (`internal/config/config.go`, existing `getEnv*` helpers):

```
TRUTHTABLE_GATE_ENABLED=false            TRUTHTABLE_GATE_POLICY_FILE=/etc/trustagent/gate-policy.json
TRUTHTABLE_GATE_DEFAULT_MODE=observe     TRUTHTABLE_GATE_FAIL_MODE=open
TRUTHTABLE_GATE_DEADLINE=600ms           TRUTHTABLE_GATE_BLOCK_DEADLINE=1500ms
TRUTHTABLE_GATE_SAMPLE_RATE=1.0          TRUTHTABLE_GATE_PASS_AUDIT_RATE=0.2
TRUTHTABLE_JEV_PROVIDER=openrouter       # openrouter | typesafe | vercel | mock
TRUTHTABLE_JEV_ENDPOINT=https://openrouter.ai/api/alpha/decisions
TRUTHTABLE_JEV_MODEL=typesafe/jev-1.13   # pinned; never ~typesafe/jev-latest when thresholds are tuned
OPENROUTER_API_KEY=                      # or TYPESAFE_API_KEY for provider=typesafe
TRUTHTABLE_JEV_MAX_RETRIES=1             TRUTHTABLE_JEV_HEDGE_AFTER=0     # e.g. 400ms on block routes
TRUTHTABLE_JEV_MAX_RPS=18                TRUTHTABLE_JEV_MAX_INFLIGHT=32
TRUTHTABLE_JEV_MAX_STATE_TOKENS=24000    TRUTHTABLE_JEV_CHARS_PER_TOKEN=4
TRUTHTABLE_JEV_BREAKER_FAILURES=5        TRUTHTABLE_JEV_BREAKER_COOLDOWN=30s
TRUTHTABLE_JEV_APP_URL=https://github.com/timkush1/TrustAgent   # HTTP-Referer (OpenRouter attribution)
TRUTHTABLE_JEV_APP_TITLE=TrustAgent                              # X-OpenRouter-Title
TRUTHTABLE_JEV_ZDR=false
```

**Policy file.** It is JSON so no YAML dependency is added to `go.mod`. It is hot-reloaded and can be edited from the dashboard (§12).

```json
{"defaults":{"mode":"observe","fail":"open","context_from":["extension","tool_messages"]},
 "routes":[
  {"name":"support-bot","match":{"header":{"X-TrustAgent-Route":"support-bot"}},"mode":"flag",
   "thresholds":{"flag_risk":0.80,"flag_contradiction":0.70}},
  {"name":"payments-faq","match":{"header":{"X-TrustAgent-Route":"payments-faq"}},"mode":"block",
   "stream_policy":"buffer","deadline_ms":1500,"hedge_after_ms":400,"context_from":["extension","kb"],
   "thresholds":{"flag_risk":0.80,"flag_contradiction":0.70,"block_contradiction":0.95,"block_min_confidence":0.90,"block_unsafe":false},
   "on_block":{"action":"fallback_message","message":"I can't confirm that from our policy documents. A human agent will follow up."}}]}
```

**Python** (`truthtable/config.py`; each field accepts both `TRUTHTABLE_JEV_*` and the bare name via `AliasChoices`, so one `.env` serves both services):

```python
jev_enabled: bool = False
jev_provider: Literal["openrouter", "typesafe", "mock"] = "openrouter"
jev_endpoint: str | None = None                  # default per provider
jev_model: str = "typesafe/jev-1.13"
openrouter_api_key: str | None = None
typesafe_api_key: str | None = None
jev_timeout_s: float = 5.0
jev_max_retries: int = 2
verifier_mode: Literal["llm", "jev", "cascade"] = "llm"
jev_accept_confidence: dict[str, float] = {"supported": .8, "contradicted": .8, "not_addressed": .8, "partially_supported": 1.01}
jev_batch_mode: Literal["per_claim", "batched"] = "per_claim"
jev_max_questions_per_call: int = 16
jev_max_state_tokens: int = 24000
jev_escalate_numeric: bool = True
jev_explain: Literal["none", "contradicted", "all"] = "contradicted"
jev_shadow_rate: float = 0.05
jev_kb_gate: bool = False
jev_kb_contradiction: bool = False
jev_kb_injection_screen: bool = False
```

`docker-compose.yml:156-157` passes keys explicitly, so `OPENROUTER_API_KEY=${OPENROUTER_API_KEY:-}` has to be added to both the engine and the proxy. Add the new variables to `.env.example`.

---

## 7. Interfaces

### Go: `internal/jev`

```go
type Client interface {
	Decide(ctx context.Context, req *Request) (*Response, error)
	Name() string // "openrouter" | "typesafe" | "vercel" | "mock"
}
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
	SessionID string              `json:"session_id,omitempty"` // OpenRouter only, ≤256 chars
	Provider  map[string]any      `json:"provider,omitempty"`   // e.g. {"zdr":true}
}
type Question struct {
	Type         string `json:"type"`               // noul|choice|score
	Instructions any    `json:"instructions"`       // always sent (OpenRouter requires it)
	Criteria     any    `json:"criteria,omitempty"` // noul: both true/false or omitted
}
type Answer struct {
	Type          string                     `json:"type"`
	Noul          *float64                   `json:"noul,omitempty"`
	Choice        string                     `json:"choice,omitempty"`
	Score         *float64                   `json:"score,omitempty"`
	Confidence    *float64                   `json:"confidence,omitempty"`    // optional on OpenRouter
	Probabilities map[string]float64         `json:"probabilities,omitempty"` // map; order unstable
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`        // keys "0","1",...
}
type Response struct {
	ID, Model, Provider string
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  int      `json:"input_tokens"`
		OutputTokens int      `json:"output_tokens"`
		Cost         *float64 `json:"cost,omitempty"`
	} `json:"usage"`
	Latency time.Duration `json:"-"`
}
type APIError struct {
	Status     int
	Kind       string // auth|credits|rate_limit|overloaded|invalid|too_large|timeout|upstream|bad_content_type
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}
```

- **One HTTP adapter covers OpenRouter, TypeSafe and Vercel.**
- **Transport:** plain `net/http`. The OpenRouter Go SDK requires `go 1.25.10` vs this repo's `1.25.0` [orgo].
- **Reject non-JSON responses:** a wrong path such as `/v1/systemone` without `/api` returns `200 text/html` from the site's catch-all page.
- **Normalise four error envelopes:**
  - OpenRouter `{"error":{code,message,metadata}}`
  - TypeSafe `{"detail":[...]}` (422)
  - TypeSafe `{"detail":{error_type,message}}` (a probe returned 403)
  - Vercel `{"message","error_type"}` [V][vercel]
- **Retry rules:**
  - Retry: 429, 5xx, 524, 529, and a 402 whose `metadata.limit_source == "openrouter_in_flight_budget"` (honour `Retry-After`) [orlimits].
  - Open the breaker: 401, 403, and any other 402.
  - Never retry: 400, 422.
  - 413: shrink the request and try again.
- **Health check:** `GET https://openrouter.ai/api/v1/models/typesafe/jev-1.13/endpoints` (public, includes `uptime_last_30m`). TypeSafe's `models.list` fails against OpenRouter [orsdk].
- **Wrappers:** `WithBreaker`, `WithHedge`, `WithRateLimit`, `WithCache`. The cache is keyed by a hash of the canonical request, so a retried request is not decided twice.

**`internal/gate`:**

```go
type Input struct {
	RequestID, UserRequest, SystemInstructions, Response string
	Context []ContextDoc
}
type Decision struct {
	RequestID, Route, Category, Model, QuestionSet, Reason string
	Mode   Mode
	Action Action
	Risk, ContradictionRisk, CategoryConfidence *float64
	CategoryProbs, FaithfulnessLevels map[string]float64
	Disagreement bool
	LatencyMs    int64
	CostUSD      float64
}
type Gate interface {
	Evaluate(ctx context.Context, in Input, p *RoutePolicy) Decision
}
```

**Deterministic Go mock (`jev.MockClient`)**
- Rules are `{state_contains: [...], question: "id", answer: {...}}`, matched by substring, mirroring `MockLLMProvider`.
- The default answer is derived from an FNV hash of the question id and canonical state, so it is stable across runs.
- `Strict: true` returns an error when no rule matches.

### Python: `truthtable/jev/`

```python
JSON = str | int | float | bool | None | list["JSON"] | dict[str, "JSON"]

class NoulQ(BaseModel):
    type: Literal["noul"] = "noul"
    instructions: JSON
    criteria: dict[Literal["true", "false"], JSON] | None = None

class ChoiceQ(BaseModel):
    type: Literal["choice"] = "choice"
    instructions: JSON
    criteria: dict[str, JSON | None]   # validated len ≤ 255

class ScoreQ(BaseModel):
    type: Literal["score"] = "score"
    instructions: JSON
    criteria: list[JSON]               # validated 2..10

class Answer(BaseModel):
    type: Literal["noul", "choice", "score"]
    noul: float | None = None
    choice: str | None = None
    score: float | None = None
    confidence: float | None = None
    probabilities: dict[str, float] = {}
    legend: dict[str, Any] = {}

class Decision(BaseModel):
    model: str
    answers: dict[str, Answer]
    input_tokens: int
    output_tokens: int
    cost_usd: float | None
    latency_ms: int
    request_id: str | None

class JevClient(Protocol):
    name: str
    async def decide(self, state: JSON, questions: Mapping[str, NoulQ | ChoiceQ | ScoreQ], *,
                     session_id: str | None = None, timeout: float | None = None) -> Decision: ...
    async def health_check(self) -> bool: ...
```

- **Implementations:** `HttpJevClient` on `httpx.AsyncClient` (the repo pins `httpx ^0.26`), plus `MockJevClient(fixtures_path=...)`, which substring-matches like `MockLLMProvider` and raises `FixtureNotFoundError`.
- **Do not adopt `typesafe-sdk`:** it depends on `httpx2` [orsdk], and whether that can coexist with the pinned `httpx` is **UNVERIFIED**.
- Jev is **not** an `LLMProvider` (it has no `complete()`), so it gets its own small registry.
- **Probabilities:** normalise them to sum to 1, since the docs only promise "approximately 1".

### Contract changes (`proto/evaluator.proto`, then `make proto`)

```proto
message GateDecision { string action=1; string mode=2; string route=3; float risk=4; float contradiction_risk=5;
  string category=6; float category_confidence=7; map<string,float> category_probabilities=8;
  string model=9; int64 latency_ms=10; double cost_usd=11; string reason=12; string question_set=13; }
message CascadeStats { int32 claims_total=1; int32 resolved_by_jev=2; int32 escalated=3;
  map<string,int32> escalation_reasons=4; double jev_cost_usd=5; int64 jev_latency_ms=6; double llm_cost_usd=7; }
// AuditRequest:      GateDecision gate = 9;   (and actually populate context = 4)
// ClaimVerification: string judge = 5;        // "jev" | "llm" | "jev>llm"
//                    string jev_choice = 6; float jev_confidence = 7;
//                    map<string,float> jev_probabilities = 8; string escalation_reason = 9;
// AuditResult:       CascadeStats cascade = 12;
// ClaimIngestResult: string judge = 7;
```

Matching changes elsewhere:
- WebSocket: a new `gate_decision` message, broadcast as soon as the gate decides and merged later by `request_id`.
- `types/audit.ts`: `WSMessage.type` gains `'gate_decision'`; `AuditResult` gains `gate?` and `cascade?`; `ClaimVerification` gains `judge?` and `jev?`.
- Postgres migration: a `gate_decisions` table (request_id, route, mode, action, risk, contradiction_risk, category, probabilities jsonb, model, latency_ms, cost_usd, created_at, deep_audit_hallucinated nullable, human_label nullable).

---

## 8. Measurement

**Changes to `evals/run_eval.py`**
- New flags: `--verifier {llm,jev,cascade}`, `--jev {mock,openrouter,typesafe}`, `--jev-model`, `--batch-mode`.
- `--sweep 0.50:0.95:0.05`: sweeps the accept threshold and prints the cost vs F1 curve.
- `--gate`: evaluates the tier-1 noul `unsupported_content` alone as P(hallucinated), response-level.
- `--max-cost-usd`: aborts a live run once it reaches that spend.

**Reported per mode**
- Precision, recall, F1, AUROC and ECE, using the existing `compute_metrics`. For Jev-only and gate runs the score is `noul`; for the cascade it is `1 − faithfulness`.
- p50/p95 latency per audit.
- $ per 1k audits: Jev from `usage.cost`, or `input_tokens × 4.2e-8`. The LLM from its token usage times a configurable price table (Haiku default $1 / $5 per MTok).
- Escalation rate by reason.
- A 4×4 Jev-vs-LLM verdict confusion matrix.
- A repeat-variance run: the same input 5 times, since one tester saw probabilities swing by 0.5 [kumar].

**Datasets**
- `golden_v1` (CI, mock).
- HaluEval (already present).
- Add **RAGTruth**, which is already deferred in `docs/PLAN.md`, so results can be compared with Arize's published 87% / AUC 0.94. Note the contamination caveat [V][arize].
- Add an adversarial injection set.
- Add a numeric/date set.

**CI**
- `golden_v1_jev_fixtures.json` and the baseline `baselines/golden_v1_cascade.json` stay deterministic.
- Live runs are manual or nightly, with the cost cap.

**Prometheus** (unified `trustagent_*` namespace)
- `trustagent_gate_decisions_total{route,mode,action,category}`
- `trustagent_gate_latency_seconds{route}`
- `trustagent_gate_risk`
- `trustagent_jev_requests_total{component,status}`
- `trustagent_jev_cost_usd_total{component}`
- `trustagent_jev_breaker_open`
- `trustagent_cascade_claims_total{resolved_by,verdict}`
- `trustagent_jev_escalations_total{reason}`
- `trustagent_jev_llm_agreement_total{jev_verdict,llm_verdict}`

---

## 9. Dashboard

1. **Gate column** in `AuditRow`: a badge such as `FLAG · 0.87 · unsupported_claims · 231 ms`. It appears instantly from `gate_decision`, with "deep audit pending" until `audit_result` arrives. The detail view shows "Jev said X / deep audit said Y" side by side.
2. **Probability bars** (new `components/gate/ProbabilityBars.tsx`):
   - The Choice distribution for `risk_category`, and for each claim's verdict in `ClaimBreakdown`.
   - Each noul as one bar, with the route's flag and block thresholds drawn on it.
   - Faithfulness shown as a distribution over the 5 levels, never as "3.4/5".
   - A `judge` chip on each claim: Jev, LLM, or Jev→LLM with the reason.
3. **Calibration panel** (`CalibrationPanel.tsx`):
   - Reliability diagram with 10 bins: predicted P against the observed rate, with the diagonal for reference and a count histogram underneath.
   - ECE value; filters for question type, model snapshot and label source (human, public dataset, or deep-audit proxy, each clearly labelled).
4. **Cascade funnel** (`CascadeFunnel.tsx`):
   - Claims → resolved by Jev (by verdict) → escalated (by reason) → LLM agreed or overturned.
   - "$ this period vs the LLM-only counterfactual", and latency p50/p95 per stage. `PipelineView` gains `gate`, `jev_verify` and `llm_escalate` steps.
5. **Policy editor** (`PolicyEditor.tsx` with a Zustand `gateStore.ts`, backed by `GET/PUT /api/gate/policies` behind auth and with an audit log):
   - Per-route mode, threshold sliders, deadline, fail-open/closed and `on_block`.
   - A **what-if replay** over the last N stored decisions, e.g. "would flag 4.2%, block 0.3%, 12 of those overturned by the deep audit".
6. **Grafana:** gate and cascade panels added to `config/grafana/dashboards/trustagent.json`.

---

## 10. Questions only a live API call can answer, and how to stay robust meanwhile

| # | Open question | Robust default |
|---|---|---|
| 1 | Maximum questions per call (none documented) | Chunks of 16; split in half on 400/413; optional startup probe (~275 tokens ≈ $0.00001) |
| 2 | Is OpenRouter really 32k combined, or 64k/32k as on TypeSafe direct? | Budget 24k estimated; probe with a synthetic large state in staging |
| 3 | Tokens per character (no tokenizer) | Self-fitted from `usage.input_tokens` |
| 4 | Does OpenRouter always return `confidence`/`probabilities`? (optional in the schema) | Fall back to max probability; if both are missing, escalate or return `not_assessed` |
| 5 | Is the batched layout (sibling data in instructions plus a state reference) as accurate as per-claim? | Per-claim is the default until an eval A/B says otherwise |
| 6 | Exact OpenRouter 400 validation-error body | Log the raw body; classify as `invalid`; no retry |
| 7 | Are Score levels outside 2..10 rejected? | Validate on the client side; the design uses 5 levels |
| 8 | Latency distribution from our region, and how often the alpha endpoint hangs | Deadline and hedge thresholds taken from the measured p95 |
| 9 | OpenRouter rate limit for this model | RPS limiter, 429 metric, breaker |
| 10 | Determinism of repeated identical requests | Idempotency cache; repeat-variance run in the eval |
| 11 | Is ZDR honoured? Does BYOK work with a TypeSafe key? | Config flags; the adapter also supports TypeSafe direct |
| 12 | How long will `jev-1.13` and `/api/alpha` be served? | Pinned model; alert when the returned `model` changes; endpoint switchable to `/api/v1/systemone` |
| 13 | Behaviour with an empty `reference_context` array | Never send one: omit context and use the context-free question set |
| 14 | Do probabilities sum to exactly 1? | Normalise in code |
| 15 | Is the confidence formula stable across versions? | Keep thresholds keyed per snapshot |

---

## 11. Rollout order

1. **P0.** Adapters, mocks, proto, and the eval harness in LLM, Jev and cascade modes (offline).
2. **P1.** Gate in `observe` mode on all routes, plus the dashboard gate column, calibration panel and `gate_decisions` table.
3. **P2.** `verifier_mode=cascade` behind a flag, with shadow sampling.
4. **P3.** `flag` mode on chosen routes, using thresholds from P1 and P2 data.
5. **P4.** `block` mode on explicitly opted-in routes, with buffered streaming.
6. **P5.** KB gate, contradiction check and injection screen.

Following CLAUDE.md: add this as Phase 8 in `docs/PLAN.md`, change the proto via `make proto` only, and bump `VERSION` and its 4 mirror files together.

---

## 12. Access and credentials

- **Git:** no token was needed. This was read-only work with no push or pull. The remote is `https://github.com/timkush1/TrustAgent.git`, the credential helper is `store` (the token is kept in plain text in `~/.git-credentials`), and `gh` is not installed.
  - If a later push fails with 401/403, create a fine-grained token at https://github.com/settings/personal-access-tokens/new.
  - Give it access to `timkush1/TrustAgent` only, with Contents: Read and write (add Pull requests: Read and write if you want PRs opened).
  - Then replace the old token in the `https://timkush1:<TOKEN>@github.com` line of `~/.git-credentials`, or delete that line and paste the new token as the password on the next `git push`.
- **Jev through OpenRouter:** create a key at https://openrouter.ai/settings/keys and add prepaid credits (otherwise requests get 402).
  - Put `OPENROUTER_API_KEY=...` in the repo-root `.env`, which is gitignored at `.gitignore:15`.
  - Pass it into both services in `docker-compose.yml`.
  - Never put it in the React app [ortut].

**Repo files read (all under `/home/timkushmar/projects/TrustAgent`):**
- `backend-go/internal/proxy/handler.go`
- `backend-go/internal/config/config.go`
- `backend-go/internal/worker/pool.go`
- `backend-go/internal/grpc/client.go`
- `backend-go/internal/websocket/hub.go`
- `backend-go/cmd/proxy/main.go`
- `proto/evaluator.proto`
- `backend-python/src/truthtable/graphs/nodes/{verifier,scorer,retriever}.py`
- `backend-python/src/truthtable/graphs/{audit_graph,state}.py`
- `backend-python/src/truthtable/providers/{base,mock,registry}.py`
- `backend-python/src/truthtable/{config,main}.py`
- `backend-python/src/truthtable/kb/{ingestion,contradiction}.py`
- `backend-python/evals/{run_eval,metrics}.py`
- `frontend-react/src/types/audit.ts`

[intro]: https://docs.typesafe.ai/introduction
[qs]: https://docs.typesafe.ai/introduction/quickstart
[api]: https://docs.typesafe.ai/api
[models]: https://docs.typesafe.ai/models
[s1]: https://docs.typesafe.ai/concepts/system-one
[prim]: https://docs.typesafe.ai/primitives
[choice]: https://docs.typesafe.ai/primitives/choice
[score]: https://docs.typesafe.ai/primitives/score
[noul]: https://docs.typesafe.ai/primitives/noul
[conf]: https://docs.typesafe.ai/confidence
[jag]: https://docs.typesafe.ai/model-jaggedness/jev-1.13
[cite]: https://docs.typesafe.ai/cookbooks/citation_check
[rag]: https://docs.typesafe.ai/cookbooks/classifying_rag_passages
[par]: https://docs.typesafe.ai/cookbooks/parallel_questions
[legal]: https://docs.typesafe.ai/legal
[blog]: https://typesafe.ai/blog/introducing-system-one-models-and-jev
[orhub]: https://openrouter.ai/docs/guides/community/jev
[ortut]: https://openrouter.ai/docs/guides/community/jev-tutorial
[orsdk]: https://openrouter.ai/docs/guides/community/typesafe-sdk.md
[orapi]: https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request.md
[orep]: https://openrouter.ai/api/v1/models/typesafe/jev-1.13/endpoints
[ormodel]: https://openrouter.ai/typesafe/jev-1.13
[orlimits]: https://openrouter.ai/docs/api_reference/limits.md
[orblog]: https://openrouter.ai/blog/insights/what-is-jev/
[orgo]: https://proxy.golang.org/github.com/!open!router!team/go-sdk/@v/v0.8.32.mod
[aisdk]: https://github.com/OpenRouterTeam/ai-sdk-provider
[vercel]: https://vercel.com/docs/ai-gateway/sdks-and-apis/typesafe
[arize]: https://arize.com/blog/jev-as-a-judge/
[haqq]: https://www.haqq.ai/blog/jev-legal-ai-hallucination-detection
[ood]: https://github.com/scienthoon/jev-ood-calibration
[kumar]: https://amankumar.ai/blogs/jev-measured
[pydai]: https://github.com/pydantic/pydantic-ai/issues/8552
[arxiv758]: https://arxiv.org/abs/2609.26758
[awesome]: https://github.com/AbdelStark/awesome-typesafe-jev
[langfuse]: https://langfuse.com/blog/2026-09-18-using-typesafes-jev-for-evals
[devto]: https://dev.to/gde/jev-after-eight-days-of-independent-tests-level-with-mid-price-llms-behind-the-frontier-1kln
[jevpro]: https://jev.pro/access/jev-on-openrouter/
