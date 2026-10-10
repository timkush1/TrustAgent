# Social card (Open Graph image)

The picture GitHub, LinkedIn and chat apps show when someone shares the repository or the demo
link. It has one job: in the half-second a feed gives it, show that TrustAgent catches a wrong
answer, using a real catch from the product rather than a mock-up.

## The card

**1280×640 PNG, under 1 MB.** One image serves the GitHub social preview, `og:image` on the
landing page and the LinkedIn post.

```
┌────────────────────────────────────────────────────────────────────────────┐
│  64 px margin on every side                                                │
│                                                                            │
│  TrustAgent                                                                │
│                                      ┌──────────────────────────────────┐  │
│  Catch hallucinations                │ Flagged                          │  │
│  before your users do.               │                                  │  │
│                                      │ "You can get a full refund       │  │
│                                      │ within 60 days of purchase."     │  │
│                                      │         ~~~~~~~ contradicted     │  │
│                                      │                                  │  │
│                                      │ Evidence (refund-policy.md):     │  │
│                                      │ "Annual plans can be refunded    │  │
│                                      │ within 30 days of purchase."     │  │
│                                      │                                  │  │
│                                      │ Checked against refund-policy.md │  │
│                                      └──────────────────────────────────┘  │
│  Open source · self-hosted · github.com/timkush1/TrustAgent                │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

- **Left:** the wordmark (`copy.launch.og.title`) and the tagline as the headline
  (`copy.launch.og.tagline`, the landing page's h1). Nothing else competes with it.
- **Right:** a crop of the real trace detail of the featured caught answer (`featuredTrace()`):
  the outcome pill, the contradicted claim with its wavy underline, the contradicting line from
  the evidence, and "Checked against {sources}" (`checkedAgainst()`). The words come from the
  recording, not from this spec; the sketch shows the refund example.
- **Footer:** `copy.launch.og.footer`.
- **Colour** means a verdict or an outcome, as everywhere in the product (`DESIGN.md` §1–§4): the
  chrome is achromatic, and only the outcome pill and the contradicted claim carry colour, each
  with its icon and words as well (never colour alone).
- **Type:** the product's own fonts; the headline at least 64 px so it survives being shown at
  about a third of its size in a feed. Nothing important within 64 px of an edge, because some
  feeds crop.
- **No number on the card.** A latency or a cost would need its label chip to be honest, and a
  chip is unreadable at feed size.

## Where it comes from

- **Route:** `/og` in the dashboard entry (`src/routes/og.tsx`), outside the app shell and the nav,
  `noindex`. It exists only in the launch build (`npm run build:launch`, `vite build --mode
  launch`: the static build plus `VITE_LAUNCH_ROUTES=1`), which only the capture scripts run and
  nothing publishes; every other build, the Pages site and the dashboard image included, renders
  the not-found page there. The card (`OgCard`) marks itself `data-og-ready` once fonts and data are in.
- **Capture:** `make og` (`npm run og`: Playwright project `launch`, `e2e/launch/og.spec.ts`)
  screenshots the card at exactly 1280×640 and writes `frontend-react/public/og.png` and
  `docs/launch/og.png`.
- **Only from a recording.** The capture refuses a `kind: mock` dataset. `OG_ALLOW_MOCK=1` is for
  trying the layout: it stamps a "Simulated" chip on the card (`copy.launch.og.simulated`) and
  writes only to `test-results/launch/`, never to the two published paths.
- **Meta tags:** the landing page adds `og:image` (an absolute URL built from `VITE_SITE_URL`)
  only when `public/og.png` exists at build time. Until the recorded capture is committed, shared
  links show no image rather than a simulated one.

## Done when

- [ ] `make check-claims-launch` finds both files, 1280×640, under 1 MB.
- [ ] Shown at 400 px wide, the headline and the underlined claim are still readable.
- [ ] The GitHub social preview (Settings → General → Social preview) shows the uploaded
      `docs/launch/og.png`, and LinkedIn's post inspector shows the same card for the demo URL.
