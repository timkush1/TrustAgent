# 60-second demo script

A narrated screen recording for the launch post and the repository: one caught hallucination,
followed from the moment it arrives to the policy that stops it, then what checking costs. The
viewer should understand the product in the first 15 seconds and see something they have not
seen before (a check written in plain English) before 40.

## The featured trace

The take follows one answer: the recorded tape's **featured trace** (`featured.trace_id` in
`frontend-react/demo-data/recorded/manifest.json`). It is picked by rule when the tape is recorded,
never by hand and never edited (D209): `contradicted_with_span` takes the first flagged, fallback
or blocked trace with a CONTRADICTED claim that has a span in the answer, from the refund
scenario first, then the rest of the support-bot route. Only when no trace meets the rule does the
owner accept the best flagged one instead (`best_flagged`), at the recording step.

The social card, the README hero, the screenshots, this video and the post's opening lines
(`make linkedin-post`) all show this same trace, so the words below in angle brackets are
whatever it holds: `<claim>` is the contradicted claim's words in the answer, `<evidence>` the
document words that contradict it, and `<document>` that document's name.

## Before recording

- **Nobody records it by hand** (D210). The stack beats (Studio, Policies, Savings) are taken by
  `make launch-video-stack` on the record stack of the attempt under review, which
  `make record-demo ACCEPT=1 VIDEO=1` runs at B8, inside the record ledger and its cap. At C2,
  `make launch-video` takes the static beats from the recorded Pages build and joins them with
  those, with the captions below burned in. The static site has no Policies page and runs no paid
  dry-runs, which is why those beats come from the stack. There is no manual paid recording to
  make on your own stack.
- **Rehearse for free.** `make demo` runs every beat with zero keys, and
  `make launch-video VIDEO_ALLOW_MOCK=1` assembles a rehearsal take in `tmp/launch/rehearsal/`,
  but every number in them is Simulated. Never publish a rehearsal take.
- **One theme** for the whole take, browser zoom at its default, a 1920×1080 window, notifications off,
  the bookmarks bar hidden.
- **No keys on screen**, ever: connect the key before recording, and keep Settings out of frame.
- **Captions burned in.** Most feeds autoplay muted; the captions below carry the story without
  the voice-over.

## The take

| Time | Screen | What you do | Caption (and voice-over) |
|---|---|---|---|
| 0–5 s | Landing page hero | Let the hero card play its three beats, then press **Try the demo** | "Catch hallucinations before your users do." |
| 5–15 s | Traffic (replay of the recorded dataset) | Wait for the featured answer to arrive; its row turns **Flagged** (or its outcome), with "Checked against: `<document>`" | "Every answer is checked against the documents the app already sent." |
| 15–27 s | Trace detail of the featured trace | Point at `<claim>`, underlined as contradicted, beside the document's `<evidence>`; open the checks to show Jev's probabilities; wait for the deep-audit explanation to arrive claim by claim | "TrustAgent flags it before the user reads it. The deep audit explains why." |
| 27–40 s | Evaluator Studio (your stack) | Type "Flag when the bot promises a refund", press **Draft with AI**, run the test on its samples, then **Test on past traffic**: how often it would have fired in the last day, and what that run cost, with its label | "Describe a new check in plain English, and test it before it touches users." |
| 40–50 s | Policies (your stack) | Show the `payments-faq` route in **fallback**; replay one of its answers: the client receives the grounded fallback answer instead. The route is calibrated, or its "uncalibrated" chip is in frame | "Each route decides what happens: observe, flag, block or fall back." |
| 50–60 s | Savings strip, then a terminal, then the repository | Hold on the Savings strip with every figure's label visible; type `make demo` in a terminal; end on github.com/timkush1/TrustAgent | "Open source. `make demo` runs it with zero keys." |

## Rules for the edit

- Every number on screen keeps its label chip in frame (Measured, Estimate, Vendor,
  Third-party); never crop a chip off or cut away before it is readable.
- Do not speed-ramp the fast check: if the take shows it happening, it happens at real speed.
- The last frame stays up for at least a second with the repository URL legible.
- Export an MP4 for LinkedIn and the release assets; the README uses the separate hero GIF (see
  [og-spec.md](og-spec.md) for the card, and `make hero-gif` for the GIF).
