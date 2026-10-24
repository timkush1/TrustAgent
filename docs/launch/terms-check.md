# Terms check (G0)

May this project publish figures about the Jev decision model, and redistribute its outputs (the
recorded demo tape, the captured vendor fixtures, the social card)? This page records what the
owner read, when, and what it means for the launch (D200). It is filled in at sitting S1 and
finished at step C3; until then every **TO FILL** below stays as it is, and nothing Jev-derived
is pushed (D222). `main` itself has been public with v2 phases P0 to P5 since 2026-10-21, by the
owner's decision (D220): the third-party and vendor Jev figures quoted there, with their sources,
are already public, and in mode R the owner decides in H-R whether they stay.

**Status: TO FILL** (one of: mode A, mode W, mode R)

## Why it is checked

A third-party write-up ([wunderlandmedia, 2026-09-18](https://wunderlandmedia.com/typesafe-ai-jev-terms-of-service-gdpr))
says TypeSafe's Master Customer Agreement §2.3(f) forbids publishing "benchmarks or performance
information about the Services", and calls a breach material. The primary text could not be read
when the plan was written. TypeSafe's public Terms and Acceptable Use Policy, and OpenRouter's
Terms, had no such clause at the time, but OpenRouter's §5.1 binds its users to each model's own
terms. Publishing the repository counts as publishing: it already carries third-party figures
about Jev.

## What was read

Each page was searched for: benchmark, performance, publish, compare, output, redistribut,
display, competitive, evaluat, disclos, reverse.

| Document | URL | Read on | Clause found (quoted), or "none found" |
|---|---|---|---|
| Jev on OpenRouter (model page and any model terms link) | https://openrouter.ai/typesafe/jev-1.13 | TO FILL | TO FILL |
| TypeSafe Terms of Service | https://typesafe.ai/legal/terms | TO FILL | TO FILL |
| TypeSafe Acceptable Use Policy | https://typesafe.ai/legal/acceptable-use-policy | TO FILL | TO FILL |
| TypeSafe Master Customer Agreement (the document the reported clause is in) | TO FILL | TO FILL | TO FILL (or "could not find or open it": that is unsure, mode W) |
| OpenRouter Terms of Service (§5.1, model terms) | https://openrouter.ai/terms | TO FILL | TO FILL |

OpenRouter's model data for TypeSafe's endpoint (read at Step 0, 2026-10-10): it does not train
on prompts and does not retain them; its `canPublish` flag is false, which is about publishing
prompts and says nothing about benchmarks.

## The decision

**Mode: TO FILL**, decided on TO FILL by the owner.

- **Mode A** (no clause found in any document, the Master Customer Agreement included, or written
  permission): the full plan runs. The owner sends the courtesy note (an FYI, whether TypeSafe may
  be tagged in the post, and the promise to send the results at least 3 days before posting).
  Note the date it was sent, the date the results went (step C5a of the plan, once the release
  branch is pushed: the measured figures, the link to the published run and the post's final
  text; the post waits at least 3 days from it) and any reply here.
- **Unread counts as unsure** (D231): if the Master Customer Agreement could not be found or
  opened, or it is unclear which terms OpenRouter's Jev page binds its users to, the mode is W,
  however little the other pages say. A search that found nothing is not a reading of the
  agreement.
- **Mode W** (a clause found, or unsure): the owner sends the permission request, which asks
  whether the Master Customer Agreement applies to OpenRouter users and whether benchmark figures
  and the recorded demo may be published (a written yes makes it mode A); paid Jev runs
  wait (the cloud session has no private place to keep their outputs, D222), and nothing
  Jev-derived is pushed. After seven days without a reply the owner chooses mode R or keeps waiting.
- **Mode R** (a clause and no permission): no Jev-derived figure, recorded tape or captured Jev
  response is committed or pushed. The site replays the simulated tape (`PAGES_TAG_DATASET` =
  `simulated`), the post is the mode-R variant (`make linkedin-post RESTRICTED=1`), and
  `check-claims --launch --restricted` is the launch gate. Whether third-party and vendor Jev
  figures that are public elsewhere stay (with their source links) is the owner's call in H-R.

Correspondence (dates only; the messages themselves stay out of the repository):

| Date | Direction | What |
|---|---|---|
| TO FILL | sent | courtesy note or permission request |
| TO FILL | received | reply, or "no reply after seven days" |
| TO FILL | sent | mode A: the results (C5a: figures, run link, post text), at least 3 days before the post |

## Dataset licences

What the benchmark and calibration data allow (read at Step 0, 2026-10-10, from each source):

| Dataset | Used for | Licence | What it means here |
|---|---|---|---|
| RAGTruth ([ParticleMedia/RAGTruth](https://github.com/ParticleMedia/RAGTruth), revision `c103204`) | the bench and calibration (QA split) | MIT (GitHub licence API) | Results may be published with attribution. |
| MS MARCO passages (the QA sources inside RAGTruth) | the evidence in each QA example | Non-commercial research only ([microsoft.github.io/msmarco](https://microsoft.github.io/msmarco/)): "intended for non-commercial research purposes only … made available free of charge without extending any license or other intellectual property rights" | Published results keep example ids, labels and scores only, never the passages (`per_example` stores no text). |
| LLM-AggreFact ([lytang/LLM-AggreFact](https://huggingface.co/datasets/lytang/LLM-AggreFact), revision `981dfd0`) | the optional second benchmark | CC-BY-ND-4.0, gated; each sub-dataset keeps its own licence | Scores may be published with attribution; the data is not redistributed or modified. |

Re-read each licence on the day of the paid runs and note any change here: TO FILL.
