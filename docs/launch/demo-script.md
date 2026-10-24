# 60-second demo script

The storyboard of the launch video for the post and the repository: one caught hallucination,
followed from the moment it arrives to the policy that stops it, then what checking costs. The
video is made by script, not recorded by hand (D210), and carries burned-in captions with no
voice-over. The viewer should understand the product in the first 15 seconds and see something
they have not seen before (a check written in plain English) before 40.

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

## How the take is made

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
- **One theme** for the whole take: the recorders use a 1920×1080 browser viewport in the light
  theme, with no browser chrome, bookmarks or notifications in frame.
- **No keys on screen**, ever: the stack beats sign in with the attempt's admin key behind the
  scenes, and no beat opens Settings.
- **Captions burned in, no voice-over.** Most feeds autoplay muted, so the Caption column below
  carries the whole story; `scripts/launch_video.py` turns it into the burned-in captions.

## The take

| Time | Screen | What the take shows | Caption (burned in) |
|---|---|---|---|
| 0–5 s | Landing page hero | The hero card plays its three beats, then **Try the demo** is pressed | "Catch hallucinations before your users do." |
| 5–15 s | Traffic (replay of the recorded dataset) | The featured answer arrives; its row turns **Flagged** (or its outcome), with "Checked against: `<document>`" | "Every answer is checked against the documents the app already sent." |
| 15–27 s | Trace detail of the featured trace | `<claim>`, underlined as contradicted, beside the document's `<evidence>`; the checks open on Jev's probabilities; the deep-audit explanation arrives claim by claim | "TrustAgent flags it before the user reads it. The deep audit explains why." |
| 27–40 s | Evaluator Studio (the record stack) | "Flag when the bot promises a refund" is typed and **Draft with AI** pressed; the test runs on its samples, then **Test on past traffic**: how often it would have fired in the last day, and what that run cost, with its label | "Describe a new check in plain English, and test it before it touches users." |
| 40–50 s | Policies (the record stack) | The `payments-faq` route in **fallback**; one of its answers replayed: the client receives the grounded fallback answer instead. The route is calibrated, or its "uncalibrated" chip is in frame | "Each route decides what happens: observe, flag, block or fall back." |
| 50–60 s | Savings strip, then the end card | The Savings strip held with every figure's label visible, then the end card with the repository's address, github.com/timkush1/TrustAgent | "Open source. `make demo` runs it with zero keys." |

## Rules the take keeps

- Every number on screen keeps its label chip in frame (Measured, Estimate, Vendor,
  Third-party); no beat crops a chip off or cuts away before it is readable.
- No speed-ramp on the fast check: if the take shows it happening, it happens at real speed.
- The end card stays up for at least a second with the repository URL legible.
- `make launch-video` writes the MP4s for LinkedIn (4:5) and the release assets (16:9); the
  README uses the separate hero GIF (see [og-spec.md](og-spec.md) for the card, and
  `make hero-gif` for the GIF).
