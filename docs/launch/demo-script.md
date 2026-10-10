# 60-second demo script

A narrated screen recording for the launch post and the repository: one caught hallucination,
followed from the moment it arrives to the policy that stops it, then what checking costs. The
viewer should understand the product in the first 15 seconds and see something they have not
seen before (a check written in plain English) before 40.

## Before recording

- **Real material only.** Record after the measurement step of the
  [launch checklist](launch-checklist.md): the landing page and the replay from the Pages site
  with the recorded dataset, and the Studio, Policies and Savings beats on your own stack
  (`make up` with your key, the calibrated showcase route, and the demo traffic). The static site
  has no Policies page and runs no paid dry-runs, so those beats need the stack.
- **Rehearse for free.** `make demo` runs every beat with zero keys, but every number in it is
  Simulated. Never publish a rehearsal take.
- **One theme** for the whole take, browser zoom at its default, a 1920×1080 window, notifications off,
  the bookmarks bar hidden.
- **No keys on screen**, ever: connect the key before recording, and keep Settings out of frame.
- **Captions burned in.** Most feeds autoplay muted; the captions below carry the story without
  the voice-over.

## The take

| Time | Screen | What you do | Caption (and voice-over) |
|---|---|---|---|
| 0–5 s | Landing page hero | Let the hero card play its three beats, then press **Try the demo** | "Catch hallucinations before your users do." |
| 5–15 s | Traffic (replay of the recorded dataset) | Wait for the support-bot answer to arrive; its row turns **Flagged**, with "Checked against: refund-policy.md" | "Every answer is checked against the documents the app already sent." |
| 15–27 s | Trace detail of that answer | Point at "60 days", underlined as contradicted, beside the policy's "30 days"; open the checks to show Jev's probabilities; wait for the deep-audit explanation to arrive claim by claim | "A fast check flags it in a fraction of a second. The deep audit explains why." |
| 27–40 s | Evaluator Studio (your stack) | Type "Flag when the bot promises a refund", press **Draft with AI**, run the test on the six samples, then **Test on past traffic**: how often it would have fired in the last day, and what that run cost, with its label | "Describe a new check in plain English, and test it before it touches users." |
| 40–50 s | Policies (your stack) | Show the `payments-faq` route in **fallback**; replay one of its answers: the client receives the grounded fallback answer instead. The route is calibrated, or its "uncalibrated" chip is in frame | "Each route decides what happens: observe, flag, block or fall back." |
| 50–60 s | Savings strip, then a terminal, then the repository | Hold on the Savings strip with every figure's label visible; type `make demo` in a terminal; end on github.com/timkush1/TrustAgent | "Open source. `make demo` runs it with zero keys." |

## Rules for the edit

- Every number on screen keeps its label chip in frame (Measured, Estimate, Vendor,
  Third-party); never crop a chip off or cut away before it is readable.
- Do not speed-ramp the fast check: if the take shows it happening, it happens at real speed.
- The last frame stays up for at least a second with the repository URL legible.
- Export an MP4 for LinkedIn and the release assets; the README uses the separate hero GIF (see
  [og-spec.md](og-spec.md) for the card, and `make hero-gif` for the GIF).
