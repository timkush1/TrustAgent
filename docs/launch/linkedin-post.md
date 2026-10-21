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

Edit the words in the templates below; `scripts/linkedin_post.py` lists the placeholders. The
opening survives LinkedIn's "…see more" cut on its own: a real catch from the recorded tape and
the three measured figures, in two sentences.

## The post

```text
My support bot promised “{featured.claim}”. The policy said “{featured.evidence}”. TrustAgent caught it in {measured:gate_latency_ms_p50} for {measured:fast_check_usd_per_1k}/1,000 answers, {measured:same_checks_cost_ratio}× cheaper than an LLM judge doing the same checks [measured].

TrustAgent is my open-source hallucination gateway. Your app points its OpenAI or Anthropic SDK at a Go gateway, and every answer is checked against the documents the app already sent the model, by Jev, a small decision model that returns probabilities instead of text. One call answers every check at once: is it grounded, does it contradict the source, did it leak personal data, did it promise something it shouldn't. Each route's policy then observes, flags, blocks or falls back, and only the risky answers go on to a claim-by-claim LLM audit.

{agreement}

The finding that changed the design: without source documents, a classifier's groundedness scores invert. Haqq's public test of Jev on legal citations found it ranked fabricated citations above honest ones when it had no sources (link in the first comment). So TrustAgent never guesses: with nothing to check against, the answer is marked "not checked".

You can also describe a new check in plain English: an LLM drafts it once, and Jev runs it on every request. You test it on samples, calibrate it with a few labels, and watch it in shadow before it touches a user.

Every number in the product carries a label (measured, estimate, vendor, third-party or simulated), and CI fails if the README drifts from them. The figures above were measured on {run.date} with {snapshot}.

Built with Go, Python (LangGraph), React and TypeScript, Postgres, gRPC and Docker.
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
Support bots promise things their own documents don't say. I built TrustAgent, an open-source hallucination gateway, to catch that before the answer reaches the user.

Your app points its OpenAI or Anthropic SDK at a Go gateway. Every answer is checked against the documents the app already sent the model: deterministic checks first, then one call to a small decision model that answers every check at once. Each route's policy then observes, flags, blocks or falls back, and only the risky answers go on to a claim-by-claim LLM audit. With nothing to check against, the answer is marked "not checked" instead of guessed.

You can also describe a new check in plain English: an LLM drafts it once, and the gateway runs it on every request. You test it on samples, calibrate it with a few labels, and watch it in shadow before it touches a user.

Every number in the product carries a label (measured, estimate, vendor, third-party or simulated), and CI fails if the README drifts from them.

Built with Go, Python (LangGraph), React and TypeScript, Postgres, gRPC and Docker.
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
