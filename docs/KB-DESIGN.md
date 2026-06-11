# Knowledge-Base Design: VERITAS-lite

> Most RAG stores index unverified text chunks. TrustAgent's knowledge base
> only admits atomic claims that pass an entailment gate against their own
> source, and detects contradictions between sources at ingest time.

## Research lineage

This design is a deliberately scoped-down implementation of the core ideas
from the VERITAS architecture research
([docs/research/VERITAS-claim-graph-research.md](research/VERITAS-claim-graph-research.md)):
a verified, claim-level knowledge substrate, motivated by the documented
failure mode of chunk-based stores — purpose-built legal-RAG tools
hallucinating 17–33% of the time *with citations*, because "cited" and
"verified" are conflated.

What was adopted, and what was deliberately cut:

| VERITAS concept | Status here |
|---|---|
| Atomic claims (not chunks) as the unit of storage | ✅ implemented |
| Gate-1: source-entailment verification at ingest | ✅ implemented |
| Contradiction detection between sources | ✅ implemented (CONTRADICTS pairs, no typed-edge graph) |
| Hybrid retrieval (BM25 + dense, RRF-fused) | ✅ implemented |
| Provenance (source doc id + excerpt per claim) | ✅ implemented |
| Gate-2: answer-entailment verification | ✅ already existed — it's the audit pipeline itself |
| Bitemporal validity (valid-time / tx-time) | ❌ cut — wrong complexity/value for this scale |
| Typed SUPERSEDES/REFINES edges | ❌ cut — needs bitemporality to be meaningful |
| Permission-aware retrieval (ACLs) | ❌ cut — single-tenant system |
| Crypto-shredding / GDPR erasure | ❌ cut — no personal data in scope |
| Calibrated trust score from corroboration/recency/authority | ⏸ deferred — requires claim-provenance plumbing through the audit graph; tracked in docs/PLAN.md |

## The dual-gate model

```
                       INGEST (Gate-1)                        AUDIT (Gate-2)
 document ──decompose──> claims ──entailed by source?──┐   LLM response
                                   │yes          │no   │        │decompose
                                   ▼             ▼     │        ▼
                               ACCEPTED     QUARANTINED│     claims ──verified against──> verdict
                                   │        (stored,   │                ACCEPTED claims
                                   ▼         visible,  │
                          contradiction      never     │
                          check vs. KB     retrieved)  │
```

- **Gate-1** (`kb/ingestion.py`) keeps the *store* honest: a claim enters
  retrieval only if the verifier confirms its own source entails it
  (SUPPORTED with confidence ≥ 0.7; PARTIALLY_SUPPORTED counts at half
  confidence). Decomposition artifacts and unsupported assertions are
  quarantined — stored and auditable in the dashboard, but never retrievable.
- **Gate-2** is the pre-existing audit pipeline: answers are verified against
  the (now Gate-1-vetted) knowledge. The two gates decouple *factuality of
  the store* from *faithfulness of the answer* — each measurable on its own.

## Contradiction detection (`kb/contradiction.py`)

On acceptance, a claim is compared against its nearest accepted neighbors
(vector similarity ≥ 0.5, top 5) using the LLM as an NLI judge
(CONTRADICTS/CONSISTENT, strict JSON schema, confidence ≥ 0.7 to record).
Detected pairs are written to both claims' payloads (`conflicts_with`) and
surfaced via `GET /api/kb/conflicts` and the dashboard's Knowledge Base tab —
silent knowledge-base inconsistency becomes a visible review queue.

The judge prompt uses the same injection defenses as the audit pipeline:
delimited untrusted data, hidden-character sanitization, strict output
validation (garbage output = no conflict recorded, never a false verdict).

## Hybrid retrieval (`kb/hybrid.py`)

Dense embeddings handle paraphrase; BM25 handles exact terms (names, dates,
model numbers). Both rankings are fused with Reciprocal Rank Fusion
(k=60, Cormack et al. 2009):

```
rrf(d) = Σ_r 1 / (k + rank_r(d))
```

BM25 is implemented directly (~40 lines of standard Okapi) over claim texts —
the corpus is small by construction, and the index is rebuilt lazily whenever
ingestion invalidates it. Quarantined claims are excluded from both paths
(dense via payload filter, sparse by indexing accepted claims only).

## Storage layout

Claims live in the existing Qdrant collection as points with payload:

```json
{
  "text": "<the atomic claim>",
  "kind": "claim",
  "kb_status": "accepted" | "quarantined",
  "source_doc_id": "...",
  "source_excerpt": "<first 300 chars of the source>",
  "entailment_score": 0.93,
  "conflicts_with": ["<claim_id>", ...],
  "ingested_at_ms": 1760000000000
}
```

Legacy seeded chunks (no `kind` field) remain retrievable by the dense-only
path; claim-level KB queries filter on `kind = "claim"`.

## API surface

| Endpoint | Purpose |
|---|---|
| `POST /api/upload` | Ingest documents → returns per-claim accept/quarantine results + conflicts |
| `GET /api/kb/claims?status=&limit=&offset=` | Page through stored claims |
| `GET /api/kb/conflicts` | Contradiction pairs |
| `GET /api/kb/stats` | Counters (total/accepted/quarantined/conflict pairs) |

All proxied by Go to the Python engine's new gRPC RPCs
(`ListKBClaims` / `ListConflicts` / `GetKBStats`, plus extended
`IngestResponse`) — contract in [proto/evaluator.proto](../proto/evaluator.proto).

## Failure-mode honesty

- A *consistently wrong* source still poisons the KB: Gate-1 checks that the
  claim matches its source, not that the source matches reality. The defense
  is contradiction detection (a poisoned doc conflicting with good docs gets
  flagged) plus upload auth/rate limits. Cross-source authority weighting is
  the natural next step (the deferred trust score).
- Contradiction recall is bounded by vector similarity: contradictions phrased
  very differently may not surface as neighbors. top_k and the similarity
  threshold are tunable per deployment.
- The LLM-as-NLI-judge inherits the judge model's quality; the eval framework
  (Tier 2) is the place to measure this per model.
