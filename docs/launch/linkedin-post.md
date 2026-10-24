# LinkedIn launch post

The post goes out only after the [launch checklist](launch-checklist.md) is complete (Gate C), and
nobody types its figures. `make linkedin-post` prints it as plain text, filled from the measured
run (`contracts/claims/measured.json`), the recorded tape's featured trace and the claims contract,
and exits 1 when any value is missing, so a gap or a guessed number never reaches the clipboard.

```bash
make linkedin-post                   # print the post
make linkedin-post CLIP=1            # copy it to the Windows clipboard from WSL (UTF-16LE)
make linkedin-post ROLES=1 CLIP=1    # with the "open to roles" line
make linkedin-post RESTRICTED=1      # the mode-R post: no fast-check figures (see below)
```

Edit the words in the templates below; `scripts/linkedin_post.py` lists the placeholders. Five
rules hold whatever the wording, and the script refuses a post that breaks one (exit 2 for the
first three; exit 1, a value missing, when a cost's or a latency's conditions are not in the run):

- **It fits LinkedIn.** LinkedIn takes 3,000 characters; the filled post stays within 2,800, so a
  longer snapshot name or a last edit in the composer still fits. Over that, the command refuses
  (exit 2) and says how much to cut.
- **The first line shows before LinkedIn's "…see more" cut on a phone**: it names TrustAgent and
  what it is, and stays within 150 characters once filled.
- **The featured answer is said for what it is** (D227). The recorded demo's answers are scripted
  (written for the demo) or generated (a named model wrote them); the owner runs no support bot, so
  the post never says "my support bot". `{featured.source}` words it from the trace.
- **A cost is never bare** (D229): the measured cost is the fast check's alone (the LLM audit
  of risky and sampled answers is extra, and the post says so: "at extra cost"), on a public
  benchmark, and `{cost.conditions}` says what it was measured on (the dataset, n, the input
  tokens per answer): a cost per answer grows with the documents' length, so it holds only for
  answers of that size. The ratio names the judge it was compared with (`{same_checks.judge}`).
- **A latency is never bare** (D205): the median is the fast check's time on the benchmark, not the
  time of this one catch, and `{gate.conditions}` follows it (connection, requests at a time, where
  the run ran, snapshot, n).

The post names who makes Jev (TypeSafe AI) and says how TrustAgent was built (with AI coding
agents, to plans the owner approved and decisions the owner made). The owner settles that wording
at S1.

## The post

```text
TrustAgent, my open-source hallucination gateway: its fast check cost {measured:fast_check_usd_per_1k} per 1,000 answers on a public benchmark [measured].

I gave it {featured.source}: it promised “{featured.claim}”, but its document says “{featured.evidence}”. TrustAgent {featured.outcome}.

Your app points its OpenAI or Anthropic SDK at a Go gateway. Jev, TypeSafe AI's decision model (through OpenRouter), checks every answer against the documents the app already sent. One call answers every check (grounded? contradicted? personal data?) as probabilities. Each route's policy then flags, blocks or falls back; risky answers plus a small sample get a claim-by-claim LLM audit, at extra cost.

The benchmark: {cost.conditions}; cost grows with document length. The fast check took {measured:gate_latency_ms_p50} at the median ({gate.conditions}) and was {measured:same_checks_cost_ratio}× cheaper than {same_checks.judge} on the same questions [measured].

{agreement}

The finding that changed the design: without source documents, Jev's groundedness scores can invert (Haqq's test ranked fabricated legal citations above honest ones; link in the first comment). So without them, TrustAgent marks an answer "not checked".

A new check can start as plain English: an LLM drafts it, you test it and watch it in shadow, then Jev runs it on every request.

Every figure in the product carries a label (measured, estimate, vendor, third-party, simulated), checked in CI. Mine come from one benchmark run on {run.date}.

Designed and directed by me; built with AI coding agents (Claude Code) to plans I approved and decisions I made. Go, Python (LangGraph), React/TypeScript, Postgres, gRPC, Docker.

{roles}I'm open to backend and AI-platform engineering roles.

Try it: https://timkush1.github.io/TrustAgent/ (the replay needs no key; live mode uses your own OpenRouter key)
Code (MIT): https://github.com/timkush1/TrustAgent

#LLM #AIEngineering #OpenSource #Golang #LLMOps
```

`{agreement}` becomes one paragraph only when every part of it is measured: our RAGTruth-QA
accuracy and balanced accuracy, each with its confidence interval, the number of answers per
class (said once when both figures come from the same answers); the thresholds the bench flags
at, read from `contracts/evaluators` (groundedness 0.8, which came from Arize's tuned cutoff on
RAGTruth itself, so the sample may favour TrustAgent's figure, or contradiction 0.7); then
Arize's two figures for Jev from the claims contract, at 0.5 and at a tuned 0.80, each with its
[third-party] chip, from one question worded differently from TrustAgent's. Neither is like for
like, and the paragraph says so (D233). If any part is missing, the command leaves the
paragraph out and says so; the agreement then lives in the README only (D212).

## The post, mode R

Used only if the terms check ends in mode R (no fast-check figures may be published; see
[terms-check.md](terms-check.md)). It names no Jev figure and no recorded answer: the public demo
then replays the simulated tape, and live mode uses the visitor's own key.

```text
TrustAgent is my open-source gateway that catches LLM answers which contradict the app's own documents.

Support bots promise things their own documents don't say. TrustAgent checks the answer before it reaches the user.

Your app points its OpenAI or Anthropic SDK at a Go gateway. Every answer is checked against the documents the app already sent the model: deterministic checks first, then one call to a third-party decision model that answers every check at once. Each route's policy then flags, blocks or falls back; risky answers plus a small sample get a claim-by-claim LLM audit. With nothing to check against, an answer is marked "not checked" instead of guessed.

A new check can start as plain English: an LLM drafts it, you test it and watch it in shadow, then the gateway runs it on every request.

Every figure in the product carries a label (measured, estimate, vendor, third-party, simulated), checked in CI.

Designed and directed by me; built with AI coding agents (Claude Code) to plans I approved and decisions I made. Go, Python (LangGraph), React/TypeScript, Postgres, gRPC, Docker.

{roles}I'm open to backend and AI-platform engineering roles.

Try it: https://timkush1.github.io/TrustAgent/ (the demo replays simulated traffic; live mode uses your own OpenRouter key)
Code (MIT): https://github.com/timkush1/TrustAgent

#LLM #AIEngineering #OpenSource #Golang #LLMOps
```

## Notes for posting

- **When.** After Gate C, at the time the owner picks. In mode A, the results went to TypeSafe at
  least 3 days before the post, as the courtesy note said they would (step C5a of the plan, once
  the release branch is pushed; a box in S3c of the launch checklist checks the date), and
  TypeSafe is tagged only if they said yes.
- **The video is the post's media**: attach the 60-second launch video (`make launch-video`) to
  the post itself, not to a comment.
- **First comment**: the Haqq link the post promises,
  https://www.haqq.ai/blog/jev-legal-ai-hallucination-detection. The inversion finding is their
  published test, not our measurement; its figures are not in the claims contract, so none go in.
- **The link card** comes from the site's `og:image`, the [social card](og-spec.md). LinkedIn
  caches a link card for about a week, so check the URL in LinkedIn's Post Inspector before the
  first post that links it.
- **A figure looks wrong?** Fix its source (the measured run, the tape or the claims contract) and
  run the command again. Never edit a number in the printed text.
