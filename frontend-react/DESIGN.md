# TrustAgent dashboard design rules

These rules apply to everything under `frontend-react/src`. The tokens live in `src/index.css`.
The words, icons and tones for server values live in `src/domain/presentation.ts`, which reads
`contracts/ui/*.v1.json`. SPEC §3.3 is the normative page list. This file is the house style.

## 1. Point of view

- **Color means a verdict or an outcome tone. Nothing else.** The chrome is achromatic: greys at
  hue 265 with near-zero chroma. There is no brand color and no colored call to action. The
  primary button is the inverted foreground fill.
- **No emoji, glow, gradient or neon.** Also none of: rows of identical stat cards, colored
  left-border cards, or decorative shadows. Elevation is lightness. The one shadow belongs to
  floating popups.
- **Every number is tabular.** Use the `tabular` utility (`font-variant-numeric: tabular-nums`).
  Round with `r2()` from `src/domain/format.ts`, never with `Math.round`.
- **One home per fact.** Connection state lives in the Health pill. Demo and read-only live in the
  top-bar badge plus the single banner. Pages do not repeat them.
- **Plain words.** No internal jargon in primary UI (`avoid_in_primary_ui` in
  `contracts/ui/glossary.v1.json`: noul, Tier-0/1/2, LangGraph, Qdrant, cascade, "decision model").
  The tiers are always **Fast check (Jev)** and **Deep audit**. Wrap product terms in
  `<Term id>` so the glossary explains them.

## 2. Tokens

Each color is defined once with `light-dark()`, and `color-scheme` picks the side.
`public/theme-init.js` stamps the resolved `data-theme` on `<html>` before first paint:
the viewer's choice, or else `prefers-color-scheme`. `src/lib/theme.ts` keeps it in sync after
that. Tailwind's palette is removed (`--color-*: initial`), so only these names exist:

| Token (utility) | Light | Dark | Use |
|---|---|---|---|
| `bg` | oklch(0.985 0.002 265) | oklch(0.155 0.006 265) | Page |
| `surface` | oklch(1 0 0) | oklch(0.19 0.006 265) | Cards, inputs |
| `surface-sunken` | oklch(0.965 0.003 265) | oklch(0.17 0.006 265) | Nav, skeletons, quiet pills |
| `surface-raised` | oklch(1 0 0) | oklch(0.23 0.006 265) | Popovers |
| `surface-hover` | oklch(0.955 0.004 265) | oklch(0.245 0.006 265) | Hover, active nav |
| `fg` | oklch(0.22 0.01 265) — 17.3:1 | oklch(0.93 0.004 265) — 15.0:1 | Text |
| `fg-muted` | oklch(0.47 0.012 265) — 6.8:1 | oklch(0.70 0.01 265) — 6.9:1 | Secondary text |
| `fg-subtle` | oklch(0.55 0.01 265) — 4.9:1 | oklch(0.62 0.01 265) — 5.1:1 | Hints, icons |
| `border` / `border-strong` | 0.91 / 0.80 | 0.27 / 0.36 | Hairlines (1px) |

**Verdict hues.** Contrast is given on the surface, then on the verdict's own 10% (light) or 16%
(dark) tint. Every value is at least 4.5:1.

| Verdict | Light | Dark | Contrast (light / dark) |
|---|---|---|---|
| `v-supported` | oklch(0.50 0.10 165) | oklch(0.80 0.13 165) | 5.7, 4.9 / 10.4, 8.0 |
| `v-contradicted` | oklch(0.50 0.17 27) | oklch(0.72 0.16 25) | 6.6, 5.6 / 7.0, 5.6 |
| `v-unsupported` | oklch(0.50 0.11 62) | oklch(0.82 0.14 80) | 6.2, 5.4 / 10.4, 8.0 |
| `v-partial` | oklch(0.48 0.12 252) | oklch(0.76 0.11 250) | 6.6, 5.6 / 8.7, 6.8 |
| `v-unknown` | oklch(0.50 0.012 265) | oklch(0.72 0.01 265) | 6.0, 5.2 / 7.4, 6.0 |
| `v-not-checked` | oklch(0.52 0.005 265) | oklch(0.64 0.005 265) | 5.5, 4.8 / 5.5, 4.6 |

**Outcome tones** (the `tone` of `contracts/ui/outcomes.v1.json`) reuse the verdict hues:
`success` = supported, `danger` = contradicted, `warning` = unsupported, `info` = partial,
`neutral` = not checked.

To add a token: define it once with `light-dark()` in `:root`, expose it in `@theme inline`,
check both sides for 4.5:1 on `surface`, and add a row here.

## 3. Verdict encoding (never color alone)

| Verdict | Label | Icon | Underline in answer text |
|---|---|---|---|
| CONTRADICTED | Contradicted | octagon-x | wavy |
| UNSUPPORTED | Unsupported | circle-question-mark | dotted |
| PARTIALLY_SUPPORTED | Partly supported | contrast | dashed |
| SUPPORTED | Supported | check | thin solid |
| UNKNOWN | Unknown | circle-dashed | double |
| NOT_CHECKED | Not checked | minus | none (not marked) |

Answer marks use `<mark data-v="contradicted|unsupported|partial|supported|unknown">`, with
`data-fuzzy` for approximate spans. The fast check's lower-case verdicts are drawn like the
matching claim verdict (`gateVerdictView`). Each verdict differs in hue, icon, label and
underline, which a unit test enforces.

## 4. Outcome encoding

Every trace has one outcome (SPEC §2.9, `outcomeOf()`). It is shown as `<OutcomePill>`, which
combines icon, label, tone and shape:

| Outcome | Shape | Icon |
|---|---|---|
| Blocked | solid fill (the answer was stopped) | shield-x |
| Replaced, Flagged, Check failed, Passed | tinted fill with a tone border | corner-down-right, flag, triangle-alert, circle-check |
| Would block, Would flag | dashed outline (observe only: it *would* have) | shield-alert, flag |
| Checking, Not checked | quiet neutral | loader-circle, circle-minus |

Level words are **Low risk / Elevated risk / High risk** (`levelWord`). Grades and trust scores
are not shown in lists or headers.

## 5. Typography

- Inter Variable for UI text and JetBrains Mono Variable for ids, code and JSON only. Both are
  self-hosted (`@fontsource-variable/*`).
- Scale (px, size/line): 11/16 `text-2xs`, 12/16 `text-xs`, **13/20 `text-sm` (body)**,
  14/20 `text-base`, 15/24 `text-md` (answer text), 16/24 `text-lg`, 20/28 `text-xl`,
  28/34 `text-2xl` (hero numbers only).
- Weights 400, 500 and 600 only. Sentence case everywhere. No all caps.

## 6. Layout

- 4px grid (Tailwind spacing), radii 6 / 8 / 12 (`rounded-sm|md|lg`) and pills, 1px hairlines.
- Shell: the nav is an icon rail below 1024px and labelled at 208px above that. The top bar
  (48px) holds the page title (the only `h1`), the demo and read-only badge, the Health pill and
  the theme toggle. At most one full-width banner sits under it.
- Banner priority: unauthorized → no credits → budget exhausted → breaker open on an enforcing
  route → read-only → demo. The one that changes what users receive wins. Copy comes from
  `contracts/ui/copy.v1.json`.
- Page content has 24px side padding. Section headings are `h2`.

## 7. Motion

- 150–200 ms ease-out, animating `transform` and `opacity` only.
- Spinners (Checking, Deep audit running) are the only looping animation.
- `prefers-reduced-motion` turns animation and transitions off globally (`src/index.css`).

## 8. Data honesty

- Every number has a source, and simulated values say so (labels come from
  `copy.value_labels`).
- A component the dashboard cannot see is **unknown**, never "running". The Health pill says
  "Unknown (status not available)".
- A missing value is an em dash from `format.ts`, never 0.
- "Agreement" is never called "accuracy".

## 9. Page contract (every page)

Every page defines these states:

| State | How |
|---|---|
| Empty | `<EmptyState>`: what is missing, then the next step as a real link or a command |
| Loading | `<QueryState>` / `useDelayedLoading`: nothing for 200 ms, then a skeleton shown for ≥ 400 ms. The router's pending UI uses the same timing |
| Error | `<ErrorState>`: the server's own message, its code, and **Retry** |
| Degraded | The Health pill checklist, plus the banner when users are affected |
| Demo | The top-bar badge and demo banner; writes explain themselves with `<DisabledReason reason="demo">` |
| Read-only | The badge and banner; writes use `<DisabledReason reason="readonly">` |

**No dead buttons.** A disabled action always says why, next to it, and links to the fix. A page
that is not built yet renders `<PhasePlaceholder>`, which states what the page will show and the
phase it arrives in, and contains only real links.

## 10. Code rules

- Components never render raw enum strings. ESLint `no-restricted-syntax` rejects
  `{trace.action}`-style children and UPPER_SNAKE verdict text in `src/components` and
  `src/routes`. Use `outcomeView`, `levelWord`, `claimVerdictView`, `auditStateView`,
  `reasonCopy` and the other helpers in `presentation.ts`.
- Server data enters through `src/data/` only: `http.ts` for REST, `errors.ts` for `ApiError`,
  `normalize.ts` for the single normalizer, `queries.ts` for query keys, and the `DataSource`
  interface. Live events flow StreamClient → ring buffer → Flusher (≤ 1 commit per 100 ms) →
  `streamStore`, which is not persisted.
- Primitives are Base UI parts styled locally in `src/components/ui/` (shadcn style). Use Popover
  for anything a reader must not miss, such as glossary infotips. A Tooltip is only a visual
  label that repeats an `aria-label`.

## 11. Accessibility

WCAG 2.2 AA:

- The focus ring is 2px in the foreground color, offset 2px.
- A skip link comes first on every page.
- Icons that sit next to a label are `aria-hidden`.
- Icon-only buttons carry an `aria-label`.
- Popovers are reachable by keyboard and touch.

`e2e/stack/shell.spec.ts` runs axe (wcag2a/aa, 21aa, 22aa) on every route in both themes. It
allows no serious or critical violations.

## 12. Decisions

- **Base UI over Radix.** It is the shadcn default and gives accessible primitives with a
  `render` prop.
- **File-based routing** (`@tanstack/router-plugin`) with `autoCodeSplitting`. `routeTree.gen.ts`
  is committed because `tsc -b` runs before `vite build`.
- **`zod/mini`** for search params and `/config.json`. Zod 4 implements Standard Schema, so
  TanStack Router takes it directly without the zod 3–only `zod-adapter`, and the mini build keeps
  about 20 KB gzip out of the shell.
- **No persisted audit data.** The v1 `trustagent-audit-storage` key is deleted on boot.
- **Same-origin by default.** `/config.json` can point the dashboard at another API origin. It
  then uses key-header auth only, with no cookies.
