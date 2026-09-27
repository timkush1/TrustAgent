# VERITAS: A Verified, Bitemporal Claim-Graph Architecture for Enterprise "Living Memory"
## With a concrete instantiation for an insurance underwriting + claims knowledge compiler

---

## TL;DR
- **The core flaw in every shipping enterprise-knowledge system today — Glean, Hebbia, Harvey, Microsoft 365 Copilot, GraphRAG, HippoRAG, RAPTOR, Graphiti/Zep — is that the *atomic unit of memory is a chunk of text, not a verified claim*.** Chunk-level storage forces every downstream guarantee (faithfulness, supersession, permissions, deletion, multi-hop reasoning) to be reconstructed at query time, which is why purpose-built legal-RAG tools "hallucinate between 17% and 33% of the time" (Magesh et al., *Journal of Empirical Legal Studies*, 2025, on Lexis+ AI, Westlaw AI-Assisted Research, and Ask Practical Law AI), why LongMemEval reports "commercial chat assistants and long-context LLMs showing a 30% accuracy drop" on sustained interactions (Wu et al., arXiv:2410.10813), and why Gartner's *How to Secure and Govern Microsoft 365 Copilot at Scale* (Goss, Litan, Wilson, January 23, 2025) found "57% of respondents limited their M365 Copilot rollout to low-risk/trusted users and 40% delayed their rollout by three months or more."
- **VERITAS** (Verified Entailment over a Retrievable, Immutable, Typed-edge Atomic-claim Store) proposes the atomic unit be a **typed, provenance-bound, bitemporal claim**, admitted to the graph only after **Gate-1 source-entailment verification**, and consumable only after **Gate-2 answer-entailment verification** — combined with hybrid + graph + temporal retrieval and a calibrated trust score computed from entailment + corroboration + recency + adversarial-review signals rather than a model's self-reported confidence (which is known to be poorly calibrated).
- **For the "Living Memory" insurance product, this means**: every underwriting guideline, policy wording clause, endorsement, claims-file note, regulator circular, and Slack/email decision is decomposed into atomic verifiable claims that are *typed* (`COVERS`, `EXCLUDES`, `SUPERSEDES`, `REFINES`, `CONTRADICTS`, `REFERENCED_BY`), *bitemporally stamped* (valid-time = policy effective dates; transaction-time = ingestion), *cited to source spans*, *permission-gated by ACL labels propagated from source systems*, and *queryable point-in-time* ("What did our cyber appetite require on 2024-03-15?") with traceable, span-level citations and per-answer entailment scores.

---

## Key Findings

1. **No production system today provides all four of: claim-level granularity, bitemporal supersession, entailment-gated answers, and permission-aware retrieval.** Graphiti/Zep (Rasmussen et al., arXiv:2501.13956) has bitemporal edges but stores extracted entities/relations, not entailment-verified claims, and has no answer-time entailment gate. GraphRAG (Edge et al., Microsoft Research, arXiv:2404.16130) has community summaries but no incremental update, no bitemporality, and no per-claim verification — its own GitHub maintainers concede that Microsoft's implementation doesn't natively support incremental updates and that entity resolution uses exact string matching ("our analysis uses exact string matching for entity matching"). HippoRAG 2 (Gutiérrez et al., arXiv:2502.14802) optimizes retrieval via Personalized PageRank but is not designed for supersession, ACLs, or answer-time verification.
2. **Faithfulness-vs-factuality is the central trust gap.** RAGAS/HHEM/Lynx/Galileo all measure whether an answer is grounded in the *retrieved* context, not whether the underlying knowledge base itself is correct. Vectara's HHEM-2.1 outperforms GPT-4-as-judge on AggreFact/RAGTruth, but only addresses faithfulness; *"FaithBench highlighted that even the best current models achieve near 50% accuracy"* on long-form hallucinations (Vectara, arXiv:2505.04847; confirmed by the original FaithBench paper arXiv:2410.13210). Verbalized confidence from LLMs is poorly calibrated, so a dual-gate entailment architecture is needed.
3. **Commercial enterprise-AI tools systematically conflate "cited" with "verified."** V7 Go's "AI Citations," Glean's source links, Copilot's footnotes — all are *visual* attestations that an answer was retrieved alongside a document; none enforce that the answer sentence is entailed by the cited span. The Stanford RegLab study found that "the AI research tools made by LexisNexis (Lexis+ AI) and Thomson Reuters (Westlaw AI-Assisted Research and Ask Practical Law AI) each hallucinate between 17% and 33% of the time."
4. **Microsoft 365 Copilot's primary production failure mode is permission-bypass via the RAG layer.** Gartner director analyst Dennis Xu identified SharePoint oversharing as the top risk at the March 2026 Gartner Sydney summit. Per Gartner's *2025 Microsoft 365 and Copilot Survey* (cited by analyst Dan Wilson), "of those that had finished pilots, five percent said their organisations were moving to larger deployment in 2025." Permission-aware retrieval is therefore non-negotiable in any "Living Memory" design.
5. **Insurance-specific vendors (Sixfold, Federato, Cytora, Indico, Roots, Shift Technology) each cover a vertical slice but none provide a verified bitemporal knowledge substrate.** Sixfold's own AI Accuracy Validator publicly documents a Cyber summary that scored 89% because "it failed to include information about the company's backup retention period" (Insurtech Insights, 2025). Shift Technology reports a 69% alert-acceptance rate on fraud (31% go uninvestigated). Cytora is single-cloud-locked on Google Cloud Vertex AI. None solve "the underwriter looked at the wrong version of the endorsement."
6. **Crypto-shredding is the right primitive for "right to be forgotten" in an append-only knowledge graph.** NIST SP 800-88 Rev.1 recognizes Cryptographic Erase at the Purge level; the EDPB *Guidelines 02/2025 on processing of personal data through blockchain technologies* (adopted April 2025) state that "it might be technically impracticable to grant the request for actual deletion made by a data subject when personal data is stored directly on a blockchain" and explicitly allow cryptographic-commitment + off-chain personal-data storage as a permissible mitigation. Per-claim envelope encryption with per-subject keys lets us tombstone-and-shred without breaking provenance chains.

---

## Details

### 1. Problem framing and why current approaches fail

An enterprise knowledge base must simultaneously satisfy nine properties; no current system satisfies more than four:

| Property | Vanilla RAG | RAPTOR | GraphRAG | HippoRAG 2 | Graphiti/Zep | Glean | Hebbia | Harvey | Copilot | **VERITAS** |
|---|---|---|---|---|---|---|---|---|---|---|
| Atomic verifiable units | ✗ (chunks) | ✗ (chunks+summaries) | ~ (entities) | ~ (KG triples) | ~ (entities/edges) | ✗ | ✗ | ✗ | ✗ | **✓ (typed claims)** |
| Bitemporal validity (valid-time & tx-time) | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | **✓** |
| Incremental update without rebuild | ✓ | ✗ | ✗ (full rebuild) | ~ | ✓ | ~ | ✓ | ✓ | ✓ | **✓** |
| Entity canonicalization | ✗ | ✗ | ~ (string match) | ~ | ✓ | ~ | ~ | ~ | ~ | **✓ (typed + bitemporal)** |
| Supersession / contradiction edges | ✗ | ✗ | ✗ | ✗ | ~ (invalidation only) | ✗ | ✗ | ✗ | ✗ | **✓ (supports/contradicts/supersedes/refines)** |
| Ingest-time entailment gate | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | **✓ (Gate-1)** |
| Answer-time entailment gate | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | **✓ (Gate-2)** |
| Permission-aware retrieval (ACL-propagated) | ✗ | ✗ | ✗ | ✗ | ~ | ~ (source ACL inherited) | ~ | ~ | ✗ (oversharing documented) | **✓ (row-level + claim-level)** |
| Cryptographic forgettability | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | **✓ (per-claim crypto-shred)** |

The reason the legacy column is mostly "✗" is structural: when the atomic unit is a chunk of text, every downstream property — supersession, contradiction, deletion, citation — must be reconstructed by an LLM at query time over a noisy, redundant, semantically-overlapping store. Microsoft 365 Copilot demonstrates the consequences in the wild: per Gartner's *How to Secure and Govern Microsoft 365 Copilot at Scale* (Goss/Litan/Wilson, Jan 23 2025), "57% of respondents limited their M365 Copilot rollout to low-risk/trusted users and 40% delayed their rollout by three months or more"; per Gartner's *2025 Microsoft 365 and Copilot Survey*, only 5% of completed pilots progress to broader deployment.

### 2. State of the art across the four dimensions + privacy + evaluation

#### 2.1 Representation / perception
- **Vanilla RAG** (chunk + dense vector) loses context across chunks and cannot do multi-hop. The retrieval-unit choice "significantly impacts the performance of both retrieval and downstream tasks" (Chen et al., "Dense X Retrieval: What Retrieval Granularity Should We Use?", arXiv:2312.06648, EMNLP 2024).
- **Dense X Retrieval / Propositions** (Chen et al.) shows propositions ("atomic expressions … encapsulating a distinct factoid … concise, self-contained natural language format") outperform passages or sentences. This is the key empirical justification for claim-level indexing.
- **RAPTOR** (Sarthi et al., Stanford, ICLR 2024, arXiv:2401.18059) recursively embeds, clusters, and summarizes chunks bottom-up — strong on QuALITY (+20% over baseline) but the tree is static and lossy.
- **GraphRAG** (Edge et al., Microsoft Research, arXiv:2404.16130) extracts entities/relations, partitions via Leiden community detection, and writes LLM-generated community summaries. Global "map-reduce" answers over community summaries dominate for sense-making but the index requires expensive rebuilds and entity-resolution uses exact string matching ("our analysis uses exact string matching for entity matching" — Edge et al., §). HippoRAG 2's authors note GraphRAG, RAPTOR, and LightRAG use significantly more resources for offline indexing than HippoRAG.
- **HippoRAG / HippoRAG 2** (Gutiérrez et al., NeurIPS 2024 + arXiv:2502.14802) uses PPR over an open-IE knowledge graph keyed on entities, mimicking hippocampal indexing; outperforms RAPTOR/GraphRAG on multi-hop while being cheaper online and offline; "achieves a 7% improvement in associative memory tasks over leading embedding models."
- **LightRAG / LongRAG / Self-RAG** (Asai et al., arXiv:2310.11511) add adaptive retrieval and critique tokens — Self-RAG generates reflection tokens (`Retrieve`, `ISREL`, `ISSUP`, `ISUSE`) to gate retrieval and critique its own generations.
- **Graphiti / Zep** (Rasmussen et al., arXiv:2501.13956) is the closest precedent for bitemporality: every edge has `t_valid` / `t_invalid`; subgraphs split into episodic/semantic/community. P95 retrieval ~300ms by avoiding LLM calls in the retrieval path. Beats MemGPT on DMR (94.8% vs 93.4%).
- **A-MEM** (Xu et al., arXiv:2502.12110) borrows the Zettelkasten method — atomic notes with flexible linking — and lets memories *evolve* as new ones arrive.
- **MemGPT/Letta** (Packer et al.) provides an OS-like tiered context (core/recall/archival) for agents; conflates memory and orchestration.
- **ColBERT/ColBERTv2** late interaction and **SPLADE** learned sparse retrieval improve retrieval precision; **SPLATE** (arXiv:2404.13950) bridges them: their pipeline "achieves the same effectiveness as the PLAID ColBERTv2 engine by re-ranking 50 documents that can be retrieved under 10ms." Hybrid (BM25 + dense) commonly outperforms either alone for high-recall enterprise workloads.

#### 2.2 Update / ingestion
The dominant production failure mode is **stale-but-confident retrieval**. GraphRAG requires periodic full rebuilds; vanilla vector stores have no native supersession. Graphiti's bitemporal model is the right primitive but it invalidates edges without typing the *reason* (refinement vs. contradiction vs. correction). Entity resolution is universally weak — most systems collapse on name match, missing the canonical-entity problem ("ACME Corp." vs "ACME Corporation Pty Ltd" vs "ACME (now Beta Industries)").

#### 2.3 Retrieval / query + citations
Best practice today is **hybrid retrieval (BM25 + dense, RRF-fused) → cross-encoder rerank → optional graph expansion**. Self-RAG demonstrates the value of *adaptive* retrieval. Attribution research — Attributed QA (Rashkin et al., arXiv:2212.08037), the AIS evaluation framework, the AutoAIS metric (T5-XXL NLI mixture) — provides the basis for citation evaluation. CRAG (Yang et al., arXiv:2406.04744) shows that "most advanced LLMs achieve ≤34% accuracy on CRAG, adding RAG in a straightforward manner improves the accuracy only to 44%. State-of-the-art industry RAG solutions only answer 63% of questions without any hallucination."

#### 2.4 Verification / trust / precision
- **FActScore** (Min et al., 2023) decomposes long-form output into atomic facts and scores precision against a knowledge source.
- **SAFE** (Wei et al., 2024) extends to open web evidence; assumes every claim is verifiable.
- **VeriScore** (Song et al., arXiv:2406.19276) only extracts *verifiable* claims and validates via Google Search; addresses unfair penalization of hypotheticals/opinions.
- **VeriFastScore** (arXiv:2505.16973) fine-tunes Llama-3.1-8B for joint extract+verify; ~6.6×–10× faster than VeriScore.
- **CoVe / Chain-of-Verification** (Dhuliawala et al., ACL Findings 2024, arXiv:2309.11495): draft → plan verification questions → answer them independently → produce verified final response.
- **RARR** (Gao et al., ACL 2023, arXiv:2210.08726): post-hoc question-generation, search, agreement-gate edit, then revise. Operationalized at SIGIR 2025.
- **FOLK** (Wang & Shu, arXiv:2310.05253): first-order-logic-guided claim decomposition + symbolic reasoning.
- **Hallucination detection (production-grade)**: Patronus Lynx (Llama-3-based; per Patronus's PubMedQA results, Lynx-70B "scored 8.3% higher than GPT-4o" on medical-inaccuracy); Vectara HHEM-2.1/2.3 (NLI-style premise-hypothesis scoring; HHEM-2.1-Open ships sub-1.5s on Intel Xeon, 4096-token windows). RAGAS faithfulness. Both are *faithfulness* signals (grounded vs. context) rather than *factuality* (grounded vs. reality).
- **Calibration**: LLMs' verbalized confidence is poorly calibrated; a system trust score must be derived from independent signals (entailment score, independent corroboration count, source recency/authority, contradiction-edge count, prior reviewer disposition).

#### 2.5 Privacy & security
- **Permission-aware RAG / ACL-hydration**: DataRobot's ACL Hydration, Microsoft's Agent Loop Security Filters, RheinInsights' two-index pattern (document index + principal index), Supabase Postgres RLS on `document_sections` — the prevailing pattern is to enforce ACLs *in the index*, not in the application layer, because (per TianPan.co, May 2026) "The model has already processed the document. If the application layer catches the retrieval only after generation, the damage is done."
- **Prompt injection** in RAG (Greshake et al., 2023) is now a documented production exploit — *EchoLeak* (CVE-2025-32711, zero-click against Microsoft 365 Copilot via crafted emails); CVE-2026-26133 cross-prompt injection in Copilot email summarization patched March 2026.
- **Bitemporal + GDPR**: EDPB Guidelines 02/2025 (April 2025) state that "it might be technically impracticable to grant the request for actual deletion made by a data subject when personal data is stored directly on a blockchain" and explicitly allow cryptographic-commitment + off-chain personal-data storage as a mitigation. NIST SP 800-88 Rev.1 recognizes Cryptographic Erase (CE) at the Purge sanitization level. **Caveat (Robinson, ThoughtWorks-radar-referenced commentary)**: "If a decryption key is exposed it becomes impossible to confidently apply crypto shredding. If the key exists somewhere, the encrypted personal data is recoverable and therefore has not met the requirements of the right to be forgotten."

#### 2.6 Measurement / evaluation
Best-of-breed benchmarks:
- **Retrieval**: KILT, BEIR, MS MARCO, MuSiQue, HotpotQA, 2WikiMultiHopQA, NQ, PopQA, NarrativeQA, LV-Eval.
- **Faithfulness/hallucination**: RAGTruth, AggreFact-SOTA, FaithBench, TofuEval, FACTS Grounding, Galileo Hallucination Index, Vectara Hallucination Leaderboard.
- **Factuality**: FActScore biographies, VeriScore long-form, SAFE.
- **Attribution**: Attributed-QA / AIS, CAQA (KG-derived fine-grained categories).
- **Freshness/temporal**: FreshQA (never-/slow-/fast-changing + false-premise).
- **End-to-end RAG**: CRAG (5 domains × 8 categories, mock web + KG APIs).
- **Long-term memory**: LongMemEval — five core abilities (information extraction, multi-session reasoning, temporal reasoning, knowledge updates, abstention); commercial chat assistants showed "a 30% accuracy drop on memorizing information across sustained interactions" and long-context LLMs dropped 30–60% on LongMemEvalS; DMR (MemGPT/Zep).

---

### 3. The novel architecture — **VERITAS** (a.k.a. the Compiled Claim Graph / "Living Memory" for insurance)

#### 3.1 Core data structures

```ebnf
Claim          ::= {
  claim_id            : ULID
  predicate           : { subject_ref: EntityRef,
                          relation: Symbol,
                          object_ref: ValueOrEntityRef,
                          quantifiers: [Quantifier],
                          modality: { polarity, certainty, scope } }
  natural_form        : str    -- decontextualized proposition (Dense X-style)
  source_span         : SpanRef -- (doc_id, byte_start, byte_end, page, bbox)
  provenance          : Provenance
  valid_time          : Interval(t_valid_from, t_valid_to | OPEN)
  tx_time             : Interval(t_ingested, t_invalidated | OPEN)
  entail_score        : Float[0,1]   -- Gate-1 NLI entailment of natural_form from source_span
  corroboration       : { sources: [SourceId], independents: int }
  trust_score         : Float[0,1]   -- f(entail_score, corroboration, recency, authority, review)
  acl                 : { allow_principals, deny_principals, classification, residency }
  pii_tags            : [PIITag]
  ciphertext          : Bytes        -- envelope-encrypted; per-subject key reference
  key_ref             : KeyId        -- for crypto-shred
}

Entity         ::= { entity_id, canonical_name, type, aliases, valid_time, embeddings, acl }
Edge           ::= { edge_id, src: ClaimId, dst: ClaimId,
                     type: { SUPPORTS, CONTRADICTS, SUPERSEDES, REFINES, REFERENCES, DERIVED_FROM },
                     weight: Float, tx_time, evidence: [SourceRef] }
Community      ::= { community_id, level, members: [ClaimId|EntityId],
                     summary_text, summary_entail_proof: [ClaimId], valid_time }
```

Key design choices:
1. **Atomic unit is a `Claim`, not a chunk.** Combines Dense X-style decontextualized natural-language form with a structured predicate (for graph traversal and exact-match operators).
2. **Bitemporal by construction.** `valid_time` (when the claim is true in the world, e.g., the policy effective period) is *orthogonal* to `tx_time` (when we learned/recorded it). This supports "what did we believe on date X" *and* "what was actually true on date X" — Graphiti tracks both per edge; we extend to per claim.
3. **Typed edges** (`SUPERSEDES`, `REFINES`, `CONTRADICTS`, `SUPPORTS`) — not just edge invalidation. A 2026 endorsement that narrows a cyber exclusion is a `REFINES` edge, not a `CONTRADICTS`; this distinction is decisive for the underwriter's answer.
4. **Trust score replaces verbalized confidence.** Trust is computed from objective signals: ingest-time entailment score, count of independent corroborating sources, source authority weight, recency decay, contradiction count, human-review disposition.
5. **ACL labels are first-class fields on every claim, edge, and entity** — propagated from source-system permissions at ingestion and intersected at retrieval time (early-binding security trimming).
6. **Envelope encryption per claim** with per-subject key reference enables crypto-shredding for GDPR Article 17 / CCPA without breaking provenance chains.

#### 3.2 Algorithm 1 — Ingestion / Compile-on-Ingest

```text
function COMPILE(document D, source_meta M):
  # 1. Parse, layout, ACL extraction
  pages       <- LAYOUT_PARSE(D)        # PDF/HTML/Slack/Email/DOCX, preserve spans+bboxes
  acl         <- RESOLVE_ACL(M)         # principals from source IdP; classification labels
  pii_spans   <- PII_DETECT(pages)      # Presidio/Comprehend; redact-or-tokenize
  text_units  <- SEMANTIC_SEGMENT(pages, target_tokens=600, overlap=80)

  # 2. Prompt-injection sanitization (treat ingested content as data, not instructions)
  text_units  <- STRIP_INJECTION(text_units)  # remove imperatives in 'instructional' carriers;
                                              # neutralize ANSI/zero-width/hidden HTML;
                                              # mark as TAINTED for downstream prompts

  # 3. Atomic proposition extraction (Dense X + FOLK hybrid)
  raw_props   <- PROPOSITIONIZE(text_units, llm=PROP_LLM)   # produce list[ {nl_form, span} ]
  fol_props   <- FOLK_DECOMPOSE(raw_props)                   # predicate(subj, rel, obj, quant, modality)

  # 4. Entity canonicalization (Gate-0)
  for p in fol_props:
    p.subject_ref <- CANONICALIZE(p.subject, EntityIndex, valid_time=infer_from(p))
    p.object_ref  <- CANONICALIZE(p.object,  EntityIndex, valid_time=infer_from(p))

  # 5. Gate-1: source-entailment verification
  admitted = []
  for p in fol_props:
    es <- ENTAIL_SCORE(premise=p.span.text, hypothesis=p.nl_form, model=NLI_ENS)
    if es >= TAU_INGEST:           # default 0.85; calibrated per domain
      p.entail_score = es
      admitted.append(p)
    else:
      QUEUE_HUMAN_REVIEW(p, reason='gate1_below_threshold')

  # 6. Conflict / supersession resolution
  for p in admitted:
    neighbors <- KNN_CLAIMS(p, k=32) UNION GRAPH_NEIGHBORS_BY_ENTITY(p)
    for q in neighbors:
      rel <- CLASSIFY_REL(p, q)    # SUPPORTS / REFINES / CONTRADICTS / SUPERSEDES / NONE
      if rel != NONE:
        ADD_EDGE(p, q, type=rel, weight=ENTAILMENT_DELTA(p,q))
        if rel == SUPERSEDES and AUTHORITY(p) >= AUTHORITY(q) and RECENT(p, q):
          q.tx_time.t_invalidated = NOW()    # bitemporal invalidation (Graphiti pattern)

  # 7. Encrypt + persist
  for p in admitted:
    p.key_ref    <- KMS_GET_OR_CREATE_KEY(subject_of(p))
    p.ciphertext <- ENCRYPT(p, p.key_ref)
    UPSERT(ClaimStore, p)
    UPDATE_INDEXES(p)                # dense, sparse, graph, ACL, temporal

  # 8. Hierarchical community maintenance (RAPTOR / GraphRAG-style, incremental)
  AFFECTED <- COMMUNITIES_TOUCHED(admitted)
  for c in AFFECTED:
    INCREMENTAL_LEIDEN(c)            # only re-cluster affected subgraph
    RE_SUMMARIZE(c, with_entailment_proof=true)   # summary cites the ClaimIds it asserts
```

**Complexity.** Per document of `n` tokens producing `m` claims: layout O(n); proposition extraction O(m · L_prop) LLM calls (batched); entity canonicalization O(m · log |E|) via HNSW + blocking; Gate-1 O(m) NLI calls (cheap encoder); conflict resolution O(m · k) with k≈32 neighbors; incremental Leiden O(|affected_subgraph| · log) — *not* the full graph. Typical: 1MB PDF → ~600 claims → ~$0.05–$0.15 in LLM cost at mid-2026 prices, ~30–90 seconds wall time amortized.

**Idempotency & DAG.** The pipeline is a deterministic job DAG keyed on `content_hash(D)` + `extractor_version` + `prompt_version` + `model_version`. Re-running on the same input with the same versions is a no-op. Re-running with a newer extractor produces a new claim revision linked by `DERIVED_FROM`, preserving the prior version's provenance.

#### 3.3 Algorithm 2 — Hybrid + Graph + Temporal Retrieval

```text
function RETRIEVE(query Q, user U, t_query=NOW(), point_in_time=NOW()):
  # 1. Route the query
  intent      <- ROUTE(Q)   # {LOCAL_FACT, GLOBAL_SENSEMAKING, TEMPORAL, MULTI_HOP, REGULATORY}
  acl_filter  <- ACL_FOR(U) # principals; classifications; residency

  # 2. Hybrid candidate generation
  C_dense   <- DENSE_TOPK(Q, k=200, filter=acl_filter ∧ valid_at(point_in_time))
  C_sparse  <- SPLADE_TOPK(Q, k=200, filter=acl_filter ∧ valid_at(point_in_time))
  C_lex     <- BM25_TOPK(Q, k=200, filter=acl_filter ∧ valid_at(point_in_time))
  C         <- RRF_FUSE([C_dense, C_sparse, C_lex], k=60)

  # 3. Late-interaction rerank
  C         <- COLBERT_RERANK(Q, C, k=30)

  # 4. Graph expansion (HippoRAG-style PPR seeded by C)
  seeds     <- TOP(C, n=12)
  C_graph   <- PERSONALIZED_PAGERANK(seeds, alpha=0.5, restart=0.2,
                                     restrict_to=acl_filter ∧ valid_at(point_in_time),
                                     edge_weights_by_type={SUPPORTS:+1.0, REFINES:+0.8,
                                                            SUPERSEDES:-1.0, CONTRADICTS:-0.5})
  C_final   <- MERGE_AND_DEDUPE(C, C_graph, k=40)

  # 5. Pull supersession chain
  for c in C_final:
    if exists e: type=SUPERSEDES, dst=c, valid_at(point_in_time):
      SWAP(c -> follow_chain(e))   # prefer authoritative current claim

  # 6. For GLOBAL_SENSEMAKING, also pull community summaries valid at point_in_time
  if intent == GLOBAL_SENSEMAKING:
    C_final += RELEVANT_COMMUNITIES(Q, point_in_time)

  return C_final
```

**Complexity / cost.** Hybrid retrieval ~O(log N) per index via HNSW/IVF + inverted lists; RRF O(k); ColBERT MaxSim with PLAID/SPLATE indexing O(k · |Q|·|D|) but bounded to top-30 — SPLATE achieves PLAID-equivalent quality "by re-ranking 50 documents that can be retrieved under 10ms"; PPR over restricted subgraph O(|reachable| · iterations), typically <50ms on 10⁷-node graphs with damping. **Target SLA: P95 < 350ms** at ≥10⁷ claims, comparable to Graphiti/Zep's reported 300ms.

#### 3.4 Algorithm 3 — Answer synthesis with Gate-2 entailment

```text
function ANSWER(query Q, C: list[Claim]):
  # 1. Draft (Self-RAG-style critique tokens optional)
  draft <- LLM_DRAFT(Q, C, system=GROUND_ONLY_PROMPT)

  # 2. Sentence-level extraction
  sents <- SPLIT_SENTENCES(draft)
  attributed = []
  for s in sents:
    # 3. CoVe-style verification question
    vq    <- GEN_VERIFICATION_QUESTION(s)
    cites <- FIND_SUPPORTING_CLAIMS(s, C ∪ NEIGHBORS(C, hops=1))
    es    <- max_{c in cites} ENTAIL(premise=c.nl_form, hypothesis=s)
    if es >= TAU_ANSWER:           # default 0.80
      attributed.append( (s, top_cites_by_es(cites), es) )
    else:
      # Try retrieve-and-edit (RARR-style agreement gate)
      C'    <- RETRIEVE_FOR_SENTENCE(s)
      s'    <- EDIT_TO_FIT(s, C')
      es'   <- max ENTAIL(C', s')
      if es' >= TAU_ANSWER: attributed.append( (s', cites_from(C'), es') )
      else:                  attributed.append( (NULL, [], 0.0) )   # drop unsupported

  # 4. Trust aggregation per answer
  ans_trust <- AGGREGATE_TRUST(attributed)
       # = w1·mean(es) + w2·corroboration_score + w3·recency + w4·authority - w5·contradictions
  return render(attributed, ans_trust, point_in_time, citation_spans)
```

**Why the dual gate works.** Gate-1 ensures the *store* never contains a claim that isn't entailed by its cited source — eliminating the "cited but not verified" failure mode at the root. Gate-2 ensures the *answer* never asserts a sentence that isn't entailed by the (already-verified) claims used to construct it. Together they decouple faithfulness (Gate-2: answer ⇐ KB) from factuality (Gate-1: KB ⇐ sources), making each measurable independently.

#### 3.5 Algorithm 4 — Incremental truth-maintenance (no rebuild)

```text
on_event NEW_DOC(D):  COMPILE(D)
on_event SOURCE_DELETED(doc_id):
  for p in CLAIMS_FROM(doc_id):
    if NO_OTHER_SOURCE(p):
      p.tx_time.t_invalidated = NOW()
      MARK_DOWNSTREAM_FOR_REVERIFICATION(p)
on_event SUBJECT_ERASURE_REQUEST(subject_id):
  keys = KEYS_FOR_SUBJECT(subject_id)
  for k in keys:
    KMS_DESTROY(k)                         # crypto-shred (NIST SP 800-88 Purge via CE)
    TOMBSTONE_CLAIMS_ENCRYPTED_WITH(k)    # tx_invalidated; keep edge skeleton for audit
on_event REVIEWER_DISPUTE(claim_id, verdict):
  c.review_log.append(verdict)
  c.trust_score = RECOMPUTE_TRUST(c)
```

### 4. Why VERITAS is novel and superior

Versus **vanilla RAG**: the unit of memory is verified before storage; supersession is explicit; citations are span-bound to a verified claim, not a chunk.

Versus **RAPTOR**: VERITAS keeps the hierarchical-summary benefit (we maintain community summaries) but the leaves are atomic verified claims, not opaque chunks; summaries cite the ClaimIds they assert and are re-verified incrementally.

Versus **GraphRAG**: VERITAS does not require periodic full rebuilds (GraphRAG's documented weakness); entity resolution is bitemporal + alias-aware, not string-match (GraphRAG explicitly "uses exact string matching for entity matching"); edges are typed (`SUPERSEDES`/`REFINES`/`CONTRADICTS`/`SUPPORTS`) rather than untyped relational; community summaries carry entailment proofs.

Versus **HippoRAG 2**: VERITAS retains the PPR-over-KG retrieval primitive (which HippoRAG 2 demonstrates is cost/latency-efficient and beats GraphRAG/RAPTOR/LightRAG on multi-hop) but adds bitemporal validity, ACL gating, supersession edges, and the dual-gate verification HippoRAG does not provide.

Versus **Graphiti/Zep**: VERITAS preserves Graphiti's bitemporal core but extends it from edges to *claims* (which are richer than edges because they carry decontextualized natural-language form, predicate structure, and an entailment proof); adds typed supersession/refinement; adds answer-time entailment gate; adds permission-aware retrieval; and adds crypto-shred deletion.

Versus **Glean / Hebbia / Harvey / Copilot**: these tools all share the chunk-and-cite architecture and all exhibit either (a) permission amplification (Copilot's documented oversharing-driven rollout delays per Gartner's January 2025 report), (b) format-sensitive extraction errors (Hebbia G2 review: "variations in form can cause the wrong data to be pulled"), (c) hallucination rates of 17–33% even with citations (Stanford RegLab on Lexis+ AI, Westlaw AI-Assisted Research, Ask Practical Law AI), or (d) message-length / connector limits (Harvey's documented 100,000→4,000 character limit when a document is attached, per Justia Verdict, August 2025). VERITAS' Gate-1/Gate-2 design structurally eliminates the unverified-citation class of error; ACL-as-claim-field structurally eliminates the oversharing class.

Versus **Sixfold / Federato / Cytora / Indico / Roots / Shift Technology**: these systems are vertical workflow products (intake, triage, fraud) that consume a knowledge base; VERITAS *is* the underlying compiled knowledge substrate they would benefit from. Sixfold's 89% Cyber-summary accuracy gap (publicly documented missing backup-retention info per Insurtech Insights) is precisely the kind of omission a claim-graph with explicit `REFINES` edges and verification-gated retrieval prevents. Shift Technology's case-study acknowledgement that "detecting fraud is not the same as confirming fraud" (GoodData) is the same epistemic gap VERITAS' trust-score aggregator closes.

### 5. Modularity — clean interfaces for trivial and uncommon connectors

VERITAS defines five abstractions; everything else is a driver.

```text
interface IngestionDriver {
  list_changes(since: Cursor) -> Stream[SourceEvent]
  fetch(ref: SourceRef)        -> RawDocument
  acl_of(ref: SourceRef)       -> ACL
  pii_hints_of(ref: SourceRef) -> [PIIHint]    # optional
}
interface LLMProvider {
  complete(prompt, schema?, temp?, max_tokens?) -> Completion
  embed(text)    -> Vec
  rerank(q, ds)  -> [Score]
  capabilities() -> { ctx_window, fn_calling, json_mode, latency_class, cost_per_1k }
}
interface Verifier {
  entail(premise, hypothesis) -> { score: Float, rationale?: str }   # NLI / HHEM / Lynx / custom
  decompose(text)             -> [Proposition]
  contradicts(p, q)           -> Float
}
interface KMS {
  get_or_create(subject_id) -> KeyId
  encrypt(plaintext, key_id) -> Ciphertext
  decrypt(ciphertext, key_id) -> Plaintext   # subject to policy
  destroy(key_id) -> Receipt                  # crypto-shred
}
interface OutputRenderer {
  render(answer, citations, trust, audit) -> { html | markdown | json | claim-trace }
}
```

- **Trivial connectors**: `FileUploadDriver`, `GoogleDriveDriver`, `SlackDriver`, `OutlookDriver`, `SharePointDriver`, `ConfluenceDriver`, `JiraDriver`. Each implements `IngestionDriver`. ACLs are pulled via SCIM / Microsoft Graph / Google Workspace Admin SDK; permissions are kept in sync via webhooks (preferred) or short polling (the pattern Paragon documents for Google Drive `/list_folder/get_latest_cursor`-style long polling). Permission changes are first-class events on the DAG.
- **Uncommon connectors for insurance** (the Living Memory case):
  - **`PolicyAdminConnector`** — pulls policy wordings, schedules, endorsements from Guidewire PolicyCenter / Duck Creek / Majesco; carries effective-date metadata into `valid_time`.
  - **`ClaimsSystemConnector`** — Guidewire ClaimCenter file notes, FNOLs, adjuster diaries; ACL pinned to claim-team membership.
  - **`RegulatoryFeedConnector`** — NAIC bulletins, state DOI circulars, Lloyd's market bulletins, ABI guidance; `valid_time.from` = effective date, authority weight = `REGULATOR` (highest).
  - **`BureauFormConnector`** — ISO/AAIS form libraries with form numbers / edition dates as natural canonical IDs.
  - **`BrokerSubmissionConnector`** — email mailbox + ACORD form parser; permission pinned to UW team.
  - **`SlackDecisionConnector`** — channel-restricted; only ingests messages from `#uw-decisions` and reactions of `:gavel:`; provenance includes message permalink.
- **LLM providers**: drivers for OpenAI, Anthropic, Google, Bedrock, Azure OpenAI, and local (vLLM / TGI). The pipeline routes per-task by capability tag (`extraction`, `verification`, `summarization`, `generation`).
- **Verifiers**: HHEM-2.x, Patronus Lynx-70B, Bespoke-MiniCheck, AlignScore, a domain-fine-tuned encoder (for insurance: trained on contract-NLI + ContractNLI + ProtoQA-derived insurance pairs). Multi-verifier ensembles improve calibration (the FaithBench result that "even the best current models achieve near 50% accuracy" on long-form means we should *ensemble*).

### 6. Privacy & security architecture (first-class, not bolted on)

1. **Multi-tenant isolation.** Logical tenancy by `tenant_id` enforced at the **storage layer** (Postgres RLS / Cassandra keyspace / per-tenant namespace in vector DB). Physical isolation available for regulated tiers via per-tenant KMS keys and per-tenant index shards.
2. **Permission-aware retrieval (early-binding).** Every claim, entity, edge, and community carries `acl.allow_principals` / `acl.deny_principals` / `classification` / `residency` fields. The retrieval query is rewritten to: `query AND (allow_principal IN U_groups OR allow_principal == U) AND NOT (deny_principal IN U_groups OR deny_principal == U)` — the two-index pattern RheinInsights documents. ACLs are **hydrated continuously** from source systems via webhooks (the DataRobot ACL Hydration pattern). Pre-LLM enforcement only: post-LLM filtering is insufficient because the model has already seen the document (TianPan.co, May 2026).
3. **Data residency.** `residency` field gates routing: an EU-residency claim is processed only by EU-region LLM endpoints; a confidential-computing tier routes to TEEs (AWS Nitro Enclaves, Azure Confidential VMs, Intel TDX) where the prompt+claim plaintext is never visible to the cloud provider's hypervisor.
4. **PII detection / redaction.** Inline at ingestion (Microsoft Presidio / AWS Comprehend / a fine-tuned NER); PII is either redacted (replaced by tokens) or kept under per-subject key with stricter ACL. PII tags propagate into claim metadata for downstream policy.
5. **BYOK / KMS.** Customer-managed keys via AWS KMS / GCP KMS / Azure Key Vault / HashiCorp Vault. Envelope encryption: one DEK per claim, wrapped by a per-subject KEK, wrapped by a per-tenant master KEK in the customer's KMS. Key rotation does not require re-encryption of claims, only re-wrap of DEKs.
6. **Right to be forgotten (GDPR Art. 17) via crypto-shredding.** On erasure request: destroy the per-subject KEK; all claims encrypted under it become permanent ciphertext blobs (NIST SP 800-88 Rev.1 Purge-level Cryptographic Erase). Keep edge skeletons + tombstones for audit, but strip plaintext. EDPB Guidelines 02/2025 endorse cryptographic-commitment + off-chain personal data as a permissible mitigation, and explicitly note that "it might be technically impracticable to grant the request for actual deletion when personal data is stored directly on a blockchain" — applicable by analogy to any immutable provenance store. **Caveat (Robinson)**: "If a decryption key is exposed it becomes impossible to confidently apply crypto shredding" — so we require KMS audit, no key export, and HSM-backed KEKs.
7. **Prompt injection defense.** Treat retrieved claim text as *data*, not instructions: enforce context isolation, label `<UNTRUSTED_CONTENT>` blocks in the system prompt, sanitize hidden text / zero-width chars / HTML / Markdown / ANSI / ARIA hidden text at ingest, strip imperatives in instructional carriers, allow-list tools per intent. Run an injection-detection classifier on retrieved claims before they enter the answer prompt. Mirror the OWASP Top 10 for LLM Applications 2025 controls. The EchoLeak incident (CVE-2025-32711, zero-click against M365 Copilot) and CVE-2026-26133 (cross-prompt injection in Copilot email summarization, patched March 2026) are the cautionary tales.
8. **Differential privacy** is offered as an optional layer on *aggregate* queries (counts/statistics over the claim graph), with a tenant-configurable ε budget. We do not apply DP to individual answer retrieval, because the noise floor would degrade per-answer faithfulness; we use DP only on telemetry and analytics, where the standard tradeoff applies.
9. **Auditability.** Every claim has an immutable provenance chain; every answer is a typed transaction (`(query, user, point_in_time, claim_set, entail_scores, trust, output)`) appended to a WORM audit log. EU AI Act Art. 12 (record-keeping for high-risk systems) and NYDFS Cybersecurity Reg. 23 NYCRR 500 are both directly satisfied by the same log.
10. **Sector rules / EU AI Act.** Insurance underwriting and claims systems making decisions about natural persons are *high-risk* under Annex III of the EU AI Act. VERITAS is designed to operate as the "data governance & traceability" layer of an Art. 10 / Art. 13 / Art. 14 compliant system: data lineage, accuracy metrics per use case, human oversight via the dual-gate disagreement queue.

### 7. Evaluation framework — named metrics, named benchmarks, CI-wired

We measure VERITAS across the axes below. All metrics are computed nightly on a versioned eval set and wired into CI as regression gates.

| Axis | Metric | How computed | Benchmark / target |
|---|---|---|---|
| Retrieval quality | Recall@k, nDCG@10, MRR, Hit@k | Labeled (query, gold-claim-set) pairs | KILT, BEIR, MuSiQue, HotpotQA, 2WikiMultiHopQA; internal Living-Memory set of ≥2,000 underwriter+claims queries with gold spans |
| Answer faithfulness (grounded vs. KB) | RAGAS faithfulness, HHEM-2.3 score, Lynx-70B PASS/FAIL, Bespoke-MiniCheck | Ensemble over (answer-sentence, retrieved-claim) pairs | RAGTruth, AggreFact-SOTA, FaithBench; target ≥0.95 ensemble agreement |
| Attribution precision/recall (citation actually entails the claim) | AutoAIS (T5-XXL NLI mixture) + human spot-audit of n=200/week | Per-sentence: does the cited span entail the sentence? | Attributed-QA / AIS, CAQA; target AutoAIS ≥0.92 |
| Factual precision (grounded vs. reality) | FActScore, VeriScore, VeriFastScore | Decompose answer → search external evidence → score | FActScore biographies; internal insurance-fact set vs. authoritative policy wordings |
| Hallucination rate | (1 − HHEM_pass) on a fixed prompt suite | Per-prompt; rolling 7-day | Vectara Hallucination Leaderboard methodology; target <2% on internal suite |
| Contradiction-catch rate | TP / (TP+FN) where TP = system flagged a real contradiction in the KB | Synthetic contradiction injection at 1% rate + held-out set | Internal; target ≥0.90 recall |
| Supersession / temporal correctness | Point-in-time exact-match accuracy | Time-stamped (query, t_query, gold_answer) tuples | FreshQA (never/slow/fast-changing + false-premise); internal endorsement-effective-date set |
| Entity resolution | B³ Precision/Recall/F, CEAF | Held-out alias pairs and reference entity sets | Internal; target B³-F ≥0.93 |
| Freshness / staleness latency | p50/p95 minutes from source-system change to KB updated | Synthetic "canary" claims modified hourly | Internal SLA: p95 < 15 min |
| End-to-end answer accuracy | LongMemEval 5-axis score; CRAG accuracy + hallucination rate | Full pipeline run | LongMemEval, CRAG; target LongMemEval ≥0.80 (commercial assistants currently show 30% drop on sustained interactions; long-context LLMs 30–60% on LongMemEvalS) |
| Calibration | ECE, Brier score, reliability diagram (Trust score vs. correctness) | Bin trust_score in 10 bins; compare empirical accuracy | Internal; target ECE ≤0.05 |
| Privacy: permission-bypass rate | Red-team queries impersonating low-priv users asking for high-priv content | Confusion matrix vs. ground-truth ACL | Internal; target 0 violations |
| Privacy: PII-redaction recall | Synthetic PII corpus + Presidio-style adversarial set | Per-PII-type recall | Internal; target ≥0.99 on SSN, CC, PHI |
| Privacy: prompt-injection rejection | Curated suite of indirect-injection payloads in ingested docs | Did model follow the injected instruction? | OWASP LLM Top-10 derived; OpenRAG-Soc carriers; target ≥0.98 rejection |
| Cost & latency | $/ingest-MB, $/query, P50/P95 ms | Telemetry | Targets: <$0.20/MB ingest, <$0.01/query (excl. generation), P95 retrieval <350ms |

**CI wiring**: every PR runs the *unit eval suite* (≈500 queries) gated at ≤2pp regression on Recall@10, Faithfulness, AutoAIS, ECE, and zero regression on permission-bypass. Nightly runs the *full suite*; weekly runs human-in-the-loop spot audits of 200 sampled answers. Failing builds block merge.

### 8. Phased implementation roadmap

- **Phase 0 (Weeks 0–4) — Foundations.** Multi-tenant data plane (Postgres + RLS), KMS integration (AWS KMS BYOK), vector store (Qdrant/pgvector), sparse index (OpenSearch BM25 + SPLADE), graph store (Neo4j or Memgraph), object storage for source docs (S3 with bucket-level KMS).
- **Phase 1 (Weeks 4–10) — Ingest MVP.** `FileUploadDriver`, `SharePointDriver`, `GoogleDriveDriver`, `SlackDriver`, `OutlookDriver`. Layout parser (LayoutLMv3 or Unstructured), Presidio PII, propositionizer (GPT-4o-mini or Llama-3.1-70B prompt), Gate-1 NLI (HHEM-2.3 + Bespoke-MiniCheck ensemble). End-to-end ingest at ≥10K docs/hour per worker.
- **Phase 2 (Weeks 10–16) — Retrieval + answers.** Hybrid retrieval, ColBERT/PLAID rerank, PPR graph expansion, draft+verify answer loop, citation rendering. Hit P95 < 350ms on 10⁶ claims.
- **Phase 3 (Weeks 16–22) — Bitemporal + supersession.** Typed edges, supersession resolver, point-in-time queries, valid-time inference from source metadata (effective dates).
- **Phase 4 (Weeks 22–28) — Insurance connectors.** Guidewire PolicyCenter + ClaimCenter, Duck Creek, ACORD parser, NAIC/DOI regulatory feed, ISO/AAIS form library, broker submission mailbox.
- **Phase 5 (Weeks 28–36) — Eval framework + CI gates.** Wire all benchmarks, build internal labeled set (≥2,000 underwriter queries), reliability diagrams, weekly human audits.
- **Phase 6 (Weeks 36–48) — Privacy hardening + AI Act compliance.** Confidential compute tier, full DP for analytics, crypto-shred SLA ≤24h, audit log shipping to customer SIEM, AI Act Art. 10/12/13/14 conformity assessment.
- **Phase 7 (Year 2) — Active learning + reviewer loop.** Disagreement-triaging UI for SMEs; reviewer disposition feeds back into trust score; auto-fine-tune a domain NLI head from accumulated dispositions.

### 9. Open research questions and risks

1. **Claim atomicity is task-dependent.** VeriScore's authors observe that "many long-form outputs interleave factual claims with unverifiable content" and over-decomposition penalizes hypotheticals. The right granularity for an underwriter ("the cyber sublimit is $5M aggregate") differs from a claims handler ("the adjuster noted possible subrogation against the contractor on 2024-03-12"). Open: a learned per-domain decomposition policy.
2. **NLI domain shift.** Off-the-shelf NLI underperforms on insurance/legal language; we likely need domain-tuned entailment heads. ContractNLI is a starting point but undersized.
3. **Verifier ensemble calibration.** FaithBench shows even the best detectors achieve "near 50% accuracy" on long-form. Stacked ensembles with conformal prediction may close the gap; this is an open empirical question.
4. **Crypto-shred under "harvest now, decrypt later."** Quantum-capable adversaries could decrypt today's ciphertext later. Open: post-quantum KEM for KEK wrapping (e.g., ML-KEM / Kyber).
5. **Trust score as a learning target.** With enough reviewer feedback, the weights (`w1…w5`) in trust aggregation can be learned per domain via isotonic or Platt-scaling against human dispositions.
6. **Adversarial contradiction.** A malicious insider could ingest authoritative-looking but false claims. Mitigation: source-authority weighting + sandboxed pre-publication review for any claim with `SUPERSEDES` edges to a high-authority source.
7. **GraphRAG-style "global sensemaking" cost.** Community summaries are expensive to recompute; we use incremental Leiden, but at very high update rates (≥10⁵ claims/day) cost grows. Open: hierarchical streaming clustering algorithms (online Leiden variants).
8. **Permission semantics across systems.** Slack channels, SharePoint sites, and Salesforce records have different permission models; normalizing to a common ACL algebra (group/role/principal) is non-trivial and a source of leaks if done wrong.

---

## Recommendations

1. **Adopt VERITAS as the substrate for "Living Memory" (the insurance product).** It is the only architecture surveyed that simultaneously addresses the four documented insurance failure modes: (a) wrong-version-of-endorsement (bitemporal supersession), (b) cited-but-not-verified (Gate-1/Gate-2), (c) cross-team data leak (claim-level ACL), and (d) regulator-asks-for-audit-trail (provenance + WORM log).
2. **Build Phase 1 against the SharePoint + Outlook + Slack + Guidewire stack in parallel.** The majority of insurer "messy" content lives in those four systems. Defer Confluence/Jira/Notion connectors to Phase 4 unless a design-partner needs them.
3. **Pick HHEM-2.3 + Bespoke-MiniCheck as your initial Gate-1/Gate-2 ensemble.** Per Vectara's benchmark, HHEM-2.1 outperforms GPT-3.5-Turbo and GPT-4-as-judge on AggreFact-SOTA, RAGTruth-Summ and RAGTruth-QA, runs sub-1.5s on Intel Xeon, and the ensemble is cheap. Add Lynx-70B as a third opinion for high-stakes claims tier (Patronus reports it beat GPT-4o on PubMedQA medical-inaccuracy by 8.3%).
4. **Insist on customer-managed KMS keys from day one.** This is the only mechanism that makes GDPR Art. 17 + EDPB-compliant deletion possible on an append-only store. Without BYOK, you will be forced into either (a) breaking the immutable provenance chain or (b) accepting regulatory risk.
5. **Wire CI regression gates before scaling beyond design partners.** The metrics that should *block* merges: permission-bypass rate (must be 0), prompt-injection rejection rate (≥0.98), Recall@10 (≤2pp regression), AutoAIS (≤2pp regression), ECE (≤0.05).
6. **Refuse to ship features that fail Gate-2 to underwriters/claims handlers.** Present "unsupported — escalate to SME" rather than a low-trust answer. This is the single biggest differentiator from Copilot/Glean/Hebbia, which all confidently render unverified text.

**Benchmarks/thresholds that would change these recommendations:**
- If an open verifier ensemble can be shown to exceed 0.90 accuracy on FaithBench *and* HHEM/Lynx licensing costs make per-claim verification uneconomical (>$0.001/claim), pivot to a single fine-tuned verifier head.
- If a customer's source systems lack ACL APIs (rare but seen in legacy mainframe environments), Phase 1 must include a manual ACL-mapping UI before any production answers are served.
- If a regulator (e.g., EIOPA, NAIC) issues prescriptive guidance on retention windows for AI training data, the bitemporal model may need to add a `retain_until` field and a scheduled crypto-shred job.

---

## Caveats

1. **Several commercial accuracy figures cited (Sixfold 89%, Federato 3.7× bind-rate, Cytora 100% case-rate increase, Roots 95%+ STP, Indico 97%) are vendor-supplied and not third-party benchmarked.** Treat them as directional, not authoritative.
2. **Harvey's hallucination rate has not been publicly benchmarked.** The Stanford RegLab study (Magesh et al., *J. Empirical Legal Studies*, 2025) covered Lexis+ AI and Westlaw AI-Assisted Research / Ask Practical Law AI (17–33% hallucination) but explicitly noted Thomson Reuters declined access; Harvey's actual rate is unknown. We extrapolate by structural analogy.
3. **The ~50% accuracy ceiling for hallucination detectors on long-form (FaithBench, arXiv:2505.04847, arXiv:2410.13210)** means even VERITAS' Gate-2 will not be perfect; the value comes from compounding it with Gate-1, which structurally eliminates large classes of error before they enter the store.
4. **Crypto-shredding's legal status under GDPR Art. 17 is untested.** Per Soatok (security researcher, 2024): "An untested legal theory circulating around large American tech companies is that 'crypto shredding' is legally equivalent to erasure." We treat it as best-available practice anchored in NIST SP 800-88 Rev.1 and EDPB Guidelines 02/2025, not as a guarantee.
5. **Latency targets (P95 <350ms) assume modern hardware** (NVMe, ≥256GB RAM per retrieval node, dedicated GPU pool for verifier/embedding). On commodity hardware these targets will not be met.
6. **The proposed architecture is buildable but non-trivial — Phase 0–6 is realistically a 12-person, 12-month build** with strong ML platform, IR, and security engineering disciplines. It is not a weekend prototype.
7. **Some sources used (eesel.ai, Feathery, Supermemory, TianPan.co) are competitor-authored.** Their criticisms of Hebbia/Zep/Sixfold are directionally consistent with G2 reviews and Gartner reports but should be re-verified before being cited externally.
8. **The architecture intentionally omits some popular techniques** (e.g., model-internal knowledge editing like ROME/MEMIT, or pure parametric continual fine-tuning) because they undermine provenance and verifiability — the design optimizes for traceability and auditability over headline benchmark scores.
9. **Gartner attributions (Goss, Wilson, Xu) refer to *director analyst* titles, not VP**, per Gartner's own publications (Goss/Litan/Wilson, *How to Secure and Govern Microsoft 365 Copilot at Scale*, January 23, 2025).