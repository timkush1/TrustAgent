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

Edit the words in the templates below; `scripts/linkedin_post.py` lists the placeholders. Three
rules hold whatever the wording (the script refuses a post that breaks the first two):

- **The first line shows before LinkedIn's "…see more" cut on a phone**: it names TrustAgent and
  stays within 150 characters once filled.
- **The featured answer is said for what it is** (D227). The recorded demo's answers are scripted
  (written for the demo) or generated (a named model wrote them); the owner runs no support bot, so
  the post never says "my support bot". `{featured.source}` words it from the trace.
- **A latency is never bare** (D205): the median is the fast check's time on the benchmark, not the
  time of this one catch, and `{gate.conditions}` follows it (connection, requests at a time, where
  the run ran, snapshot, n).

The post names who makes Jev (TypeSafe AI) and says how TrustAgent was built (with AI coding
agents, to plans, tests and reviews the owner set). The owner settles that wording at S1.

## The post

```text
TrustAgent checks each LLM answer against the app's own documents, for {measured:fast_check_usd_per_1k} per 1,000 answers [measured].

I gave it {featured.source}. It promised “{featured.claim}”; the document it was checked against says “{featured.evidence}”. TrustAgent {featured.outcome}.

TrustAgent is my open-source hallucination gateway. Your app points its OpenAI or Anthropic SDK at a Go gateway, and every answer is checked against the documents the app already sent the model by Jev, TypeSafe AI's decision model (through OpenRouter), which returns probabilities instead of text. One call answers every check at once: is it grounded, does it contradict the source, did it leak personal data, did it promise something it shouldn't. Each route's policy then observes, flags, blocks or falls back, and only the risky answers go on to a claim-by-claim LLM audit.

On a public benchmark the fast check took {measured:gate_latency_ms_p50} at the median ({gate.conditions}) and cost {measured:same_checks_cost_ratio}× less than an LLM judge asking the same questions [measured].

{agreement}

The finding that changed the design: without source documents, Jev's groundedness scores can invert. Haqq's public test of Jev on legal citations found it ranked fabricated citations above honest ones when it had no sources (link in the first comment). So TrustAgent never guesses: with nothing to check against, the answer is marked "not checked".

You can also describe a new check in plain English: an LLM drafts it once, and Jev runs it on every request. You test it on samples, calibrate it with your own labels, and watch it in shadow before it touches a user.

Every number in the product carries a label (measured, estimate, vendor, third-party or simulated), and CI fails if the README drifts from them. The figures above were measured on {run.date} with {snapshot}.

Designed and directed by me, built with AI coding agents (Claude Code) working to the plans, tests and reviews I set. Go, Python (LangGraph), React and TypeScript, Postgres, gRPC and Docker.
{roles}I'm open to backend and AI-platform engineering roles, and my messages are open.

Try it in your browser: https://timkush1.github.io/TrustAgent/ (the replay needs nothing; live mode uses your own OpenRouter key)
Code (MIT): https://github.com/timkush1/TrustAgent

#LLM #AIEngineering #OpenSource #Golang #RAG
```

`{agreement}` becomes one paragraph only when every part of it is measured: our RAGTruth-QA
accuracy and balanced accuracy, each with its confidence interval and the number of answers per
class, at the default thresholds, beside Arize's accuracy at the same 0.5 threshold (never beside
Arize's tuned-threshold figure). If any part is missing, the command leaves the paragraph out and
says so; the agreement then lives in the README only (D212).

## The post, mode R

Used only if the terms check ends in mode R (no fast-check figures may be published; see
[terms-check.md](terms-check.md)). It names no Jev figure and no recorded answer: the public demo
then replays the simulated tape, and live mode uses the visitor's own key.

```text
TrustAgent is my open-source gateway that catches LLM answers which contradict their own documents.

Support bots promise things their own documents don't say. TrustAgent checks the answer before it reaches the user.

Your app points its OpenAI or Anthropic SDK at a Go gateway. Every answer is checked against the documents the app already sent the model: deterministic checks first, then one call to a third-party decision model that answers every check at once. Each route's policy then observes, flags, blocks or falls back, and only the risky answers go on to a claim-by-claim LLM audit. With nothing to check against, the answer is marked "not checked" instead of guessed.

You can also describe a new check in plain English: an LLM drafts it once, and the gateway runs it on every request. You test it on samples, calibrate it with your own labels, and watch it in shadow before it touches a user.

Every number in the product carries a label (measured, estimate, vendor, third-party or simulated), and CI fails if the README drifts from them.

Designed and directed by me, built with AI coding agents (Claude Code) working to the plans, tests and reviews I set. Go, Python (LangGraph), React and TypeScript, Postgres, gRPC and Docker.
{roles}I'm open to backend and AI-platform engineering roles, and my messages are open.

Try it in your browser: https://timkush1.github.io/TrustAgent/ (the demo replays simulated traffic; live mode uses your own OpenRouter key)
Code (MIT): https://github.com/timkush1/TrustAgent

#LLM #AIEngineering #OpenSource #Golang #RAG
```

## Notes for posting

- **When.** After Gate C, at the time the owner picks. In mode A, TypeSafe has had the results
  for 3 days (the courtesy note) and may be tagged only if they said yes.
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
