# TrustAgent dashboard design rules

These rules apply to everything under `frontend-react/src`. The tokens live in
`src/styles/tokens.css`, which the app (`src/index.css`) and the landing page
(`src/landing/landing.css`) both import. The words, icons and tones for server values live in
`src/domain/presentation.ts`, which reads `contracts/ui/*.v1.json`. SPEC §3.3 is the normative page list. This file is the house style.

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
| `fg-subtle` | oklch(0.55 0.01 265) — 4.9:1 | oklch(0.62 0.01 265) — 5.1:1 | Hints, icons (text only on `bg`, `surface`, `surface-raised`; on `surface-sunken` or `surface-hover` use `fg-muted`) |
| `border` / `border-strong` | 0.91 / 0.80 | 0.27 / 0.36 | Hairlines (1px) |

**Verdict hues.** Contrast is given on the surface, then on the verdict's own 10% (light) or 16%
(dark) tint over the surface, composited in sRGB as browsers do (the contrast test's method).
Every value is at least 4.5:1 except `v-not-checked` on its dark tint (4.4:1), so grey verdict
text is never drawn on a tint: the neutral tint badge uses `fg-muted`.

| Verdict | Light | Dark | Contrast (light / dark) |
|---|---|---|---|
| `v-supported` | oklch(0.50 0.10 165) | oklch(0.80 0.13 165) | 5.7, 4.9 / 10.4, 7.5 |
| `v-contradicted` | oklch(0.50 0.17 27) | oklch(0.72 0.16 25) | 6.6, 5.6 / 7.0, 5.5 |
| `v-unsupported` | oklch(0.50 0.11 62) | oklch(0.82 0.14 80) | 6.2, 5.4 / 10.4, 7.5 |
| `v-partial` | oklch(0.48 0.12 252) | oklch(0.76 0.11 250) | 6.6, 5.6 / 8.7, 6.5 |
| `v-unknown` | oklch(0.50 0.012 265) | oklch(0.72 0.01 265) | 6.0, 5.2 / 7.4, 5.7 |
| `v-not-checked` | oklch(0.52 0.005 265) | oklch(0.64 0.005 265) | 5.5, 4.8 / 5.5, 4.4 |

**Outcome tones** (the `tone` of `contracts/ui/outcomes.v1.json`) reuse the verdict hues:
`success` = supported, `danger` = contradicted, `warning` = unsupported, `info` = partial,
`neutral` = not checked.

**Derived colors** (no new tokens, computed from the ones above):

- **Control boundary**: `fg-subtle` at 80% (`border-fg-subtle/80`, `CONTROL_BORDER` in
  `src/components/ui/control-styles.ts`), about 3.2:1 on the surface in both themes. Inputs,
  selects, checkboxes, radios and switches use it, because a control's edge is how it is
  recognised (WCAG 1.4.11). The `border-strong` hairline (about 1.8:1) is for decoration only.
- **Chart light shade**: a tone mixed 70% toward the surface (`palette.ts`), at least 3:1 on the
  surface in both themes. It separates two series of the same tone.
- **Neutral tint badge**: grey text on a grey tint misses 4.5:1 in the dark theme, so a neutral
  `tint` badge uses `fg-muted` text.

`src/components/charts/tokenContrast.test.ts` reads these tokens from `styles/tokens.css` and checks every
pair the kit uses, in both themes: text 4.5:1, control edges, focus ring and chart marks 3:1.
Colors at partial alpha are composited in sRGB, as browsers do, so a tint may measure slightly
lower than an OKLab mix would suggest.

To add a token: define it once with `light-dark()` in `:root`, expose it in `@theme inline`,
check both sides for 4.5:1 on `surface`, add a row here, and add its pairs to the contrast test.

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

**Instant-check marks.** A detector finding (`decision.detectors[].findings`, UTF-16) is marked
as soon as the gate decides, before any claim: `<mark data-det="<detector id>">`, a 2px solid
line in `fg-muted`, drawn 7px below the text (below a claim's underline), and the scan-search
icon after the finding. It is achromatic because a detector is not a verdict. Where a finding
overlaps a claim, it nests inside the claim's mark and both lines show; the claim's link names
the finding ("…, instant checks: links to a URL not in your documents"). A finding on its own
repeats the `det:*` reason copy in a tooltip and in screen-reader text, and the answer gets the
legend "Marked by the instant checks" (a glossary term).

## 4. Outcome encoding

Every trace has one outcome (SPEC §2.9, `outcomeOf()`). It is shown as `<OutcomePill>` (drawn by
the shared `<Badge>`, so any other pill uses the same shapes), which combines icon, label, tone
and shape:

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
- Page content has 24px side padding (16px below 640px). Section headings are `h2`.

## 7. Motion

- 150–200 ms ease-out, animating `transform` and `opacity` only.
- Spinners (Checking, Deep audit running) are the only looping animation.
- `prefers-reduced-motion` turns animation and transitions off globally (`src/styles/tokens.css`).

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
| Degraded | The Health pill checklist, plus the banner when users are affected; in place, `<DegradedState>` says what is missing, what still works and the fix |
| Demo | The top-bar badge and demo banner; writes explain themselves with `<DisabledReason reason="demo">` |
| Read-only | The badge and banner; writes use `<DisabledReason reason="readonly">` |

**No dead buttons.** A disabled action always says why, next to it, and links to the fix
(`<DisabledAction reason>` wires the reason to the control). On the static site, a page that
needs a running gateway renders the static notice (`StaticRouteGuard`), which says what the page
does on your own stack and how to run it, and contains only real links.

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

## 11. Components

Import each part from its own file (`@/components/ui/select`, not a barrel), so a page only
ships what it uses. `ui/` holds Base UI parts styled with the tokens and knows nothing about the
data layer; `common/` composes them with the page contract and `src/domain`; `charts/` holds the
data displays. There is no gallery route: `src/components/ui/kit.a11y.test.tsx` renders the whole
kit on one page and runs axe over it, closed and with each popup open.

### Actions (`ui/`)

| Component | Use it for | Rules |
|---|---|---|
| `Button` (`button.tsx`, classes in `button-variants.ts`) | Any action | `secondary` by default; `primary` (inverted fg) for the page's main action, one per view; `ghost` in toolbars; `danger` only on the confirm step of `ConfirmDialog`. Disabled buttons stay focusable. `pending` shows a spinner and ignores presses. Style a `<Link>` as a button with `buttonVariants()` |
| `IconButton` | Icon-only actions | `aria-label` is required and is repeated as the tooltip. Never put new information in the tooltip |
| `Menu`, `MenuItem`, `MenuCheckboxItem`, `MenuRadioItem`, `MenuLinkItem` | A few secondary actions behind one button | A disabled item carries `disabledReason`, shown under its label. Destructive items open a `ConfirmDialog` |
| `Kbd` | Keyboard hints ("C", `keys={['Shift','?']}`) | A hint only: every shortcut also has a visible control |

**The danger-confirm pattern** (`common/ConfirmDialog.tsx`). A destructive or risky action starts
from an ordinary secondary button (with an icon) and happens only in an alert dialog: the title
asks the question, the body says what will happen, Cancel has focus first, a click outside does
not close it, and the confirm button names the action ("Remove key") in the danger fill. Actions
that are hard to undo or that change what users receive ("Allow uncalibrated blocking") add
`confirmationText`: the viewer types the route or key name, and the instruction describes the
disabled button until it matches. A failing `onConfirm` keeps the dialog open with the server's
message and code.

### Form controls (`ui/`)

| Component | Use it for | Rules |
|---|---|---|
| `Field` | Label, description and error around any control | Fields are required unless marked `optional`. `error` takes the server's message and marks the control invalid. `hideLabel` keeps the label for screen readers only |
| `Input`, `Textarea` | Text | `Textarea mono` for JSON, YAML and code (spell check off) |
| `Select` | One value from a short, fixed list | Inside a `Field`, or with `aria-label`. Options may have a second line |
| `Combobox` | One value from a long or searchable list | Says so when nothing matches (`emptyText`) |
| `Checkbox` | A choice confirmed by a form submit | The label wraps the box; clicking the words toggles it |
| `Switch` | A setting that applies at once | Same layout as Checkbox |
| `RadioGroup` | One of a few options that each need a sentence | Group label and per-option descriptions are announced |
| `SegmentedControl` | Two to five short, equal options (time window, Chart / Table) | A radio group: one option is always selected, arrow keys move it |
| `Slider` | Thresholds (`warn_at`, `critical_at`) | One or two thumbs (`thumbLabels` names each), the value written beside the label, `marks` for reference points ("Now", "Suggested"); a thumb on a mark says the mark's name. Save in `onValueCommitted` |
| `Tabs`, `TabsList`, `Tab`, `TabPanel` | Views of the same subject | Manual activation: arrows move, Enter opens |

### Overlays (`ui/`)

| Component | Use it for | Rules |
|---|---|---|
| `Tooltip` | A visual repeat of an `aria-label` | Never the only place for information |
| `Popover` | Content a reader must not miss (glossary infotips, the Health checklist) | Opens on click and focus; `<Term>` opens it on hover too |
| `Dialog`, `DialogContent`, `DialogFooter` | A modal task (review a diff, edit a key) | Title names it, description describes it; Escape and the corner button close it; focus returns to the trigger |
| `Sheet`, `SheetContent` | A side panel that keeps the page in view (label queue, versions) | A dialog at the right edge; header and footer stay put, the body scrolls |
| `Toaster` (`toast.tsx`) + `notify` (`common/notify.ts`) | Confirming what the viewer just did; background failures | Mount `<Toaster>` once at the root. `notify.error(title, error)` shows the server's message and code and stays until dismissed. A toast is never the only copy of something the viewer must act on |

### Display (`ui/`)

| Component | Use it for | Rules |
|---|---|---|
| `Badge` | Any pill: tone + emphasis (`solid`, `tint`, `outline`, `quiet`) | Always with an icon or words that carry the same meaning. `OutcomePill` is a Badge |
| `Chip` | A value the viewer chose (a filter, "Used for" entries) | Achromatic; `onRemove` needs `removeLabel` ("Remove filter: Blocked") |
| `OutcomePill`, `VerdictBadge`, `AuditState` (`outcome/`) | Server outcomes, verdicts and audit states | Words, icons and tones come from `presentation.ts` |
| `Spinner` | Work in progress | With `label` it is a polite status; without one the text beside it must say what is happening |
| `Skeleton` | Loading placeholders | Decorative; pair with a status text (`LoadingState` does) |
| `Card`, `CardHeader`, `CardContent`, `CardFooter` | A surface with a hairline | Card titles are h3 under a section |
| `Section` | A titled region of a page | Its heading is an h2; `collapsible` turns it into a disclosure whose heading stays navigable |

### Page-level composites (`common/`)

| Component | Use it for | Rules |
|---|---|---|
| `PageHeader` | The top of a page's content | Never an h1 (the top bar has it). A description, page-wide actions, and one row of tabs or filters. A detail page may add an h2 `title` |
| `EmptyState`, `ErrorState`, `LoadingState`, `QueryState` (`StateViews.tsx`, `QueryState.tsx`) | The page contract | See section 9 |
| `DegradedState` | A part that works with less than usual | What is missing, why, what still works, the fix. In place of the part, not a second banner |
| `DisabledReason`, `DisabledAction` | No dead buttons | Contract reasons link their fix (Connections, Access, All configuration, or the demo command); page-specific reasons pass `{ text, fix }` |
| `CopyButton` | Copying ids, keys, commands | Clipboard API with an `execCommand` fallback; announces "Copied", or how to copy by hand |
| `CodeBlock` | Commands, snippets, JSON | Mono, copy button, a focusable scroll region named by its label. Mask secrets before passing them in (`copyValue` may differ from what is shown) |
| `KeyValueList` | Ids, timings, costs | Missing values are an em dash; `mono` for ids, `numeric` for numbers, `copy` for a copy button |
| `DataTable` | Long lists | Virtualized rows (`@tanstack/react-virtual`) in a scroll viewport with a sticky header that never hides the focused row; one tab stop, arrows/Page/Home/End move the row focus, Enter or a click opens the row. Loading, error and empty take the body's place; a later page's error shows under the rows. `onEndReached` asks once per list end. Cells hold text and pills, not their own buttons |
| `Pagination` | Server lists with a total | The range is written and announced; the end buttons say why they are disabled |
| `EvidenceBadge`, `LabelledValueBadge` | Where a number comes from: Measured, Estimate, Third-party, Vendor, Simulated (SPEC §7.4) | Achromatic; a popover gives the glossary definition, the server's `basis`, source link and date. Every cost, rate and count a page shows carries one; on the demo stack it says Simulated |
| `LabelledRateView` | A `LabelledRate` (hallucination rate, audited share) | Below `min_n` it shows "Needs 30 unbiased samples (have 12)" and a meter, never the number |
| `DiffView` | Reviewing a change (policy save, rollback, evaluator versions) | From two texts or a server unified diff. Achromatic: "+" and full-weight text on a raised row for additions, "−" and struck muted text for removals, "Added:"/"Removed:" for screen readers; unchanged runs fold |
| `useWriteReason(level)` | The reason for `<DisabledAction>` on writes | `'admin'`: read-only instance, then a caller without the admin scope. `'post'`: read-scope POSTs (validate, Preview impact) that a read-only instance still refuses |

## 12. Charts

Recharts is the only charting library (SPEC §3.3), and it is used only by the two wrappers below;
the small displays are plain HTML or SVG. Chart code is imported by the pages that draw charts, so
it stays out of every other bundle.

**Rules** (all enforced by the components):

- **Color means a verdict or an outcome.** Series colors come only from `charts/palette.ts`:
  the outcome tones (tone from `ui/outcomes.v1.json`, shade from the pill's emphasis), the verdict
  tokens, or the neutral inks (`fg-muted`, `fg-subtle`) for measures with no verdict (latency,
  cost, counts). Two outcomes of one tone differ by shade (`solid` / `light`), and a color always
  follows its outcome, whatever order or filter.
- **Text wears text tokens**: labels, values, axes and legends are `fg`, `fg-muted`; a colored
  key beside the words carries the identity.
- **One axis**, recessive: solid 1px hairlines in `border`, tick text 11px in `fg-muted`,
  tabular figures.
- **Thin marks**: bars at most 24px thick with a 4px rounded data end and a square baseline; a 2px
  surface gap between stacked segments; lines 2px; end dots 8px with a 2px surface ring.
- **Legend for two or more series**, none for one (the title names it). Direct labels where they
  fit (bar values at the tip, probabilities at the end), never a number on every point.
- **Hover and focus show the same readout** (values first, then series names, keyed by a short
  line); the chart is focusable and its arrow keys move the readout. The readout never holds the
  only copy of a number: every chart has a **table view** (the Chart / Table switch), and the chart
  itself is described in words (`aria-describedby`).
- No chart animation (Recharts animates geometry; `isAnimationActive` is off).

| Component | Shows | Notes |
|---|---|---|
| `ChartFrame` | Title (a heading), description, legend, Chart / Table switch | Wrap every chart in it |
| `ChartLegend`, `ChartTable`, `ChartTooltipBox` (`ChartParts.tsx`) | Legend, table view, hover readout | Plain HTML in text tokens |
| `OutcomeTimeSeries` | Requests per time bucket, stacked by outcome ("Requests by outcome") | Recharts. Stacks in `OUTCOME_STACK_ORDER`; only the top segment of a column is rounded |
| `BarChart` | One measure across named categories ("What was caught", stage latencies) | Recharts, horizontal bars with the value at the tip, neutral ink unless the measure is an outcome |
| `RiskBar`, `RiskBarLegend` | One evaluator's risk with its flag and block ticks | A `meter`; its text names the value and both thresholds. `outcome/RiskBar` re-exports it |
| `ProbabilityBars` | How the fast check split its answer across a question's options | A list: every option with its probability written out, the most likely one marked in words. Color only on options that count toward the risk |
| `Sparkline` | A word-sized trend next to a number | An image named with what it shows and a summary; nulls leave a gap; hover shows a point. Its exact values belong in the table of the chart it summarises |

The reliability chart, confusion grid and timing waterfall are custom SVG in their own features
(SPEC §3.3); they follow the same rules and draw their colors from `palette.ts`.

## 12a. Pages (P3)

SPEC §3.3 is the page list; these notes record how each page applies the rules above.

- **Overview** (`components/overview/`). Order: setup checklist (folds to "Setup complete"),
  "Connect your app" while no request has arrived (that is the empty state), the at-a-glance
  strip (one surface with hairline dividers, not stat cards), "Requests by outcome", "What was
  caught" beside evidence coverage and its "Why not?" disclosure, the savings strip, the audited
  share, Performance (collapsed), and "Connect your app" again, folded. The time window and the
  Summary / Savings tab live in the URL (`?window=7d&tab=savings`); the window picker sits in the
  page header until the top bar grows one. Snippets never contain a key: they read
  `$TRUSTAGENT_KEY` and the provider key from the environment.
- **Policies** (`components/policies/`). Nothing changes on the server until a review: route
  cards, presets, one-click fixes and the YAML editor all edit a **draft** (YAML text, edited
  with the `yaml` Document API so comments survive and only touched lines change). The draft bar
  leads to one review dialog (diff, server validation and block readiness, "Preview impact (free
  — replays past answers)", a message) and the save sends `If-Match`; a 412 offers to reload.
  "Allow uncalibrated blocking" is a typed confirmation (the route name). A downgraded route shows
  "Block → currently acting as Flag" in a dashed box with a warning icon (the degraded-state
  shape), each downgrade followed by its fix. The advanced editor is CodeMirror 6, loaded with the
  dialog: achromatic syntax styling, token colors, Tab left to the browser, problems both as lint
  marks and as a text list. Its schema check is policy.v1 compiled ahead of time
  (`src/lib/policy/policyValidator.gen.ts`, `make gen-policy-validator`), because ajv's runtime
  compiler needs `eval`, which the CSP forbids. The save's `If-Match` is the ETag the draft started
  from, never the one on screen (the stream refreshes that). A diverged policies.yaml follows SPEC
  §7.2: the dashboard version stays in effect and the file waits for "Adopt file"; "Restore my
  edit" appears only when the file itself is in effect.
- **Playground** (`components/playground/`). The form lives in a store, so it survives runs and
  page changes (never persisted: prompts may be sensitive). The result is a numbered timeline
  (your model → fast check → what your app received → deep audit), each step with a state icon
  and words; the deep audit keeps updating from the live stream. The curl equivalent is always
  shown and follows the form. An unedited example keeps its demo scenario id and sends it only to a
  demo or test-mode stack, whose mock checks answer it as trafficgen's scripted run (labelled
  simulated); editing the example drops it. A proxy that answers 404/501 to a run gets a degraded
  state (what is missing, that the curl command still works, a link to Traffic) and Run stays off
  with that reason for the rest of the session.
- **Sign in** (`components/setup/`, `/app/setup`). The target of the proxy's one-time
  `SETUP: open …/app/setup#token=…` line. The token is read once from the fragment and removed from
  the address bar; claiming it is an explicit button (never on page load). The created admin key
  is shown once with a copy button and stays on screen even when the browser keeps no session
  (a cross-origin dashboard). Sign-in with a key is the fix every `needs_admin` reason links to;
  key management (create, revoke, sign out everywhere) is Settings → Access. The setup wizard
  (secure, connect, see it work) lives on the same page.
- **Evaluators** (`components/evaluators/`). Library cards, checks that run on a route first.
  Calibration progress uses the contract copy and one meter per requirement. The label queue,
  Calibrate now, adopt and Studio need an admin key; without one they are shown disabled with the
  shared `needs_admin` reason and its fix. The
  reliability chart is custom SVG: neutral ink bars at most 24px thick, a dashed diagonal, one
  focusable surface whose arrow keys move the readout, a table view, and the caption "agreement
  with labels, not ground truth".
- **Knowledge** (`components/knowledge/`). In the nav only while `/api/status` reports the add-on
  enabled; with it off (or a 409 `knowledge_disabled`) the page explains the add-on and shows the
  one command that turns it on.

## 12b. Landing page, static site and live checks

SPEC §3.3 ("Static build"), §7.5 ("Static GitHub Pages demo") and §8.1 are normative, with the
lead decisions in `docs/v2/P5-PLAN.md` §1 (cited below as D-numbers).

**Two entries.** `index.html` is the dashboard; `landing.html` (`src/landing/`) is the landing
page. The landing entry imports no router, TanStack Query, Recharts, Base UI, `motion`, zustand
or `components/ui/`, and never `lib/openrouter`, `lib/gate` or `components/replay/live`: its
pieces are plain elements over the shared tokens (`src/landing/ui.tsx`). `landing.test.tsx` walks
its import graph to hold that line, and `npm run size` fails when its first load (HTML, JS, CSS;
woff2 fonts listed, not counted) is over 110 KB gzip. New landing sections are lazy or paid for by
trims; the refund example and the "What Jev was asked" panel come from virtual modules and a
committed generated file, never from the whole scenario pack.

| Build | `/` | Dashboard | Landing |
|---|---|---|---|
| Full (`npm run build`) | the dashboard | `/app/…` | `/welcome/` |
| Static (`npm run build:static`, `VITE_APP_MODE=static`, `VITE_BASE_PATH` default `/TrustAgent/`) | the landing page | `/app/…` replaying the dataset the build copies to `demo/` (`demo-data/simulated`, or `demo-data/recorded` when it exists; see *Datasets*), with `app/{overview,traffic,playground,evaluators/studio,savings}/index.html` and `connect/callback/index.html` copies and `404.html` for other deep links | `/` |

**Landing page rules.**

- At most seven sections (`main > section`), one h1 (the contract tagline). Section eyebrows are
  sentence case, not caps.
- Display type is the landing's only addition to §5: hero 72 px (52 below 1024 px, 40 below
  640 px), section titles 40 px (32 below 640 px), a 19 px lead. Body text stays 16/26. Weights
  stay 400/500/600.
- **Every number** is a claim from `contracts/claims/value-claims.v1.json`, rendered by
  `ClaimValue` with its `LabelChip` (Measured, Estimate, Third-party, Vendor, Simulated; dashed
  border for Simulated). The chip links to the claim's entry in the footer's "Where every number on
  this page comes from". A claim with a caveat shows the caveat next to its chip. A `measured`
  claim reads `contracts/claims/measured.json` and says "not yet measured" until a benchmark run
  publishes it; it never borrows an estimate. Contract copy with `{claim:id}` placeholders goes
  through `splitClaims`. Proxy overhead is never placed next to the fast check's latency.
- **Live links only when they work.** "Try it live" links (`liveHref`) appear only when
  `LIVE_CHECKS` is true; otherwise the same spot says "Run it yourself: `make demo`". No control
  promises something the build cannot do.
- **No invented results.** The Studio strip shows the steps (describe, draft, test on samples,
  watch in shadow) with their cost labels, not results nobody produced. The "What Jev was asked"
  panel reads the committed `glassbox.gen.json` (rebuilt by `compile()` from the refund example);
  its latency and cost are the Simulated hero-card claims.
- **The comparison table** reads `contracts/claims/compare.v1.json`. A cell says what the vendor
  documents, with its link and date, never a ranking; an unverified cell reads "Not yet verified"
  (`copy.compare.not_verified`), never "TODO". At 400 px the table becomes stacked cards, or stays
  a named, focusable scroll region.
- The hero card is an illustration of the scenario pack's refund example, labelled as such; its
  three beats ease in over 200 ms, and with reduced motion it shows the last beat at once and
  offers no "Play again" (nothing would move).
- Diagrams show direction honestly: in "How it works" every link carries the request out and the
  answer back, and the deep audit hangs off the gateway, in the background.
- 16 px side gutter below 640 px; nothing may scroll the page sideways at 400 px (wide tables
  scroll inside a named, focusable region).

**Static site rules.**

- **Datasets** (D110). `demo-data/simulated/` is the deterministic tape generated from the
  zero-key demo's scripted scenarios; `demo-data/recorded/` holds only a `kind: recorded` export
  from `make record-demo`. `VITE_DEMO_DATASET` picks one (`mock`, `recorded`, or `auto`: recorded
  when it exists), and the build copies only that one to `demo/`. Every reader uses
  `demo/manifest.json` and `demo/traces/…`.
- **Say where a tape came from, and nothing more.** The simulated tape's copy says "generated from
  the zero-key demo's scripted scenarios", never "recorded"; only a `recorded` manifest is
  "Recorded {date} with {snapshot} and {judge}". The one banner says "simulated" once
  (`copy.recording.*`). A mock recording is Simulated everywhere; `check_recording.py` and
  `recording.ts` refuse a label that could pass it off as model output.
- Data comes from `ReplayDataSource` (`src/data/replay/`): REST reads answer from the tape
  position, and the rest of the tape plays as WS v2 events in compressed time. Everything a
  recording cannot answer comes from `unavailableControlPlane`.
- "Now" is the tape's clock while a recording replays (`useNow`, `replayClock`): relative times
  read "just now" and "1 min. ago" as rows arrive, like the Overview's counts, instead of the days
  since the recording was made. The exact time stays in the title.
- **Rates from a showcase are not traffic.** The Overview carries "Scripted showcase mix: rates are
  not representative of real traffic" next to the counts, which keep the dataset label.
- **The glass box on a replayed trace** is rebuilt in the browser by `compile()`. On a simulated
  tape it says "Rebuilt in your browser · simulated recording" (a match would prove nothing,
  because the tape was built the same way); on a recorded one it says whether the rebuild matches
  the recorded request, and why not.
- **First visit.** A dismissible four-step `ReplayGuide` (the caught answer, "60 days" against
  "30 days", Show exact Jev request, Savings) starts from a link to the featured row and never
  covers that row at 400 px. Its dismissal is the only thing the replay stores
  (`localStorage` `ta.replay.guide`).
- Nav: six items: Overview (recorded), Traffic (replay), Trace (the featured flagged answer),
  Playground, Studio, and "Run it yourself" (the landing's `#self-host`). Policies, Settings,
  Knowledge, Benchmarks, the evaluator library and setup are hidden; a deep link to them shows why
  (`StaticRouteGuard`), not an error whose Retry cannot work.
- The top bar shows the replay's status in place of the Health pill and demo badge: "Replaying ·
  {n} of {total} requests", then "Replay finished · {total} requests" with "Replay again". {total}
  is the tape's own request count, never a number written into the copy or a test.
- **The static Studio** has templates, the editor, the test bench (a one-evaluator route through
  `runGate`), the free threshold what-if, quick calibration on labels made in the page, and Draft
  with AI through the browser key. Save becomes **Copy JSON** and **Download .json**. Watching,
  dry-runs and activation are one "On your own stack" card with `make demo`, not disabled buttons.
- Both pages carry the §7.5 CSP meta and `referrer: no-referrer`; no inline script (the theme
  script is a file), no third-party request, no `preconnect` or `dns-prefetch` to openrouter.ai
  (it would contact it before consent), fonts self-hosted.

**Live checks** (the static Playground's `#live` section, below the replayed examples; D99–D106).

Live checks are the one place the static site calls a vendor, with the visitor's own key. Every
state below is a designed state with words from `copy.playground_live.*`; none is a dead end.

| State | What the visitor sees |
|---|---|
| No key | The consent text (where the key goes, what it costs [estimate], what is sent and to whom, how to delete the key) and the worst case "At most ≈ {max_spend} with these caps [estimate]"; **Connect OpenRouter** and **Paste a key instead** |
| Connecting | The `connect/callback/` page: "Connecting your OpenRouter key…", then it returns to where the visitor started (Back never lands on the spent callback). Refused, expired (10 minutes), started in another tab, or a network failure each have their own sentence and lead back to Connect or Paste |
| Connected | The chip "Live · {snapshot} · uncalibrated · your key", the counter "{n} of {max} calls to OpenRouter (checks and AI drafts)" and the running cost: Vendor when OpenRouter reports `usage.cost`, Estimate otherwise. **Forget key** is always one click away |
| Result | The decision with the same outcome words as everywhere else, calibrated nowhere (raw scores); the deep audit is a note pointing to `make demo`, because it does not run in the browser. A live result is never compared with a scenario's expected outcome |
| Retryable error | The error's own sentence (`errors.*`) and **Retry**. Nothing retries on its own |
| Key refused (401) | The key is forgotten at once; "Paste a new key" |
| No credits (402) | Its own screen with **Add credits**; it does not count against the cap |
| Paused | After 10 refusals in a row: "OpenRouter keeps refusing these requests…", with Retry and Forget key |
| Cap reached | The counter at its maximum and why; a check too large for the token cap says so before anything is sent |
| Storage blocked | The key lives only until the visitor leaves; Connect is disabled with that reason, Paste still works |
| Framed | Only "Open in its own tab": no key field, no exchange |
| Not available in this build | "Live checks from the browser aren't available here. Run it yourself: `make demo`" |

- The key lives in `sessionStorage` only, for at most 2 hours, and a restored tab (back-forward
  cache) re-checks that before showing it. Live mode writes nothing to `localStorage`, cookies or
  IndexedDB. Forget key also aborts requests in flight and says where to delete the key for good.
- The courtesy caps are a courtesy, not a security boundary; the key's own credit limit is the
  real one, and the copy says so.
- Every link that leaves the app (other `github.io` paths included) opens with
  `target=_blank rel="noopener noreferrer"`, so a new tab gets no copy of the session's storage.
- Errors use the gateway's error taxonomy mapped to the live words, never the dashboard's server
  banners. A request the browser blocked says that nothing was billed.

## 13. Accessibility

WCAG 2.2 AA:

- The focus ring is 2px in the foreground color, offset 2px.
- A skip link comes first on every page.
- Icons that sit next to a label are `aria-hidden`.
- Icon-only buttons carry an `aria-label`.
- Popovers are reachable by keyboard and touch.

`e2e/stack/shell.spec.ts` runs axe (wcag2a/aa, 21aa, 22aa) on every route in both themes. It
allows no serious or critical violations. In unit tests, `src/test/axe.ts` runs axe-core in jsdom
(the same tags plus best practices, contrast excepted) and the kit page test allows no violations
at all.

## 14. Decisions

- **Base UI over Radix.** It is the shadcn default and gives accessible primitives with a
  `render` prop.
- **The sheet is a Base UI Dialog at the edge**, not Base UI's Drawer: the Drawer's swipe
  gestures and snap points serve touch bottom sheets, which the dashboard does not have.
- **Segmented controls are radio groups**, not toggle groups: exactly one option is always
  selected, and screen readers hear "1 of 3".
- **DataTable is an ARIA grid with row focus.** A row does one thing (open the item), so the row
  is the focus target and the grid is one tab stop; the focused row is always kept rendered.
- **File-based routing** (`@tanstack/router-plugin`) with `autoCodeSplitting`. `routeTree.gen.ts`
  is committed because `tsc -b` runs before `vite build`.
- **`zod/mini`** for search params and `/config.json`. Zod 4 implements Standard Schema, so
  TanStack Router takes it directly without the zod 3–only `zod-adapter`, and the mini build keeps
  about 20 KB gzip out of the shell.
- **No persisted audit data.** The v1 `trustagent-audit-storage` key is deleted on boot.
- **Same-origin by default.** `/config.json` can point the dashboard at another API origin. It
  then uses key-header auth only, with no cookies.
