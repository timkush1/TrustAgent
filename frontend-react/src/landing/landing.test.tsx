/**
 * The landing page (SPEC §8.1, §3.3, D99, D112-D116): at most seven sections, every number with its
 * label chip, honest "not yet measured" and "Not yet verified" states with no dead controls, the
 * replay card built at build time (no fetch), the live links only where live checks run, the
 * calculator on the TS cost core, and a separate entry that never pulls in the router, a chart
 * library or the live code.
 */
import { lazy } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import assumptions from '@contracts/pricing/assumptions.v1.json';
import compareDoc from '@contracts/claims/compare.v1.json';
import { copy } from '@contracts/ui/copy.v1.json';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import { playground } from '@/config/runtime';
import { LIVE_SCENARIOS, liveHref } from '@/domain/liveScenarios';
import type { PricingTable } from '@/types/api';
import { axeViolations, formatViolations } from '@/test/axe';
import { committedManifest, scenarioTrace } from '@/test/recording';
import { digest } from 'virtual:trustagent/tape-digest';
import claimsDoc from '@contracts/claims/value-claims.v1.json';
import { priceCall } from '@/lib/decision/cost';
import { claim, LABEL_TEXT, landingClaims, splitClaims } from './claims';
import { compareRows } from './compareData';
import { copy as landingCopy, DEMO_HREF, REFUND } from './content';
import { auditShareWords, defaultCalculatorInputs, formatMultiple, judgeOptions, modelName, monthlyCost, PRICE_TABLE, referenceJudge, type CalculatorInputs } from './costModel';
import glassbox from './glassbox.gen.json';
import { HeroCard } from './HeroCard';
import { Landing } from './Landing';
import { lazyPartsLoaded, loaders } from './lazyLoaders';
import { LazyPart } from './lazyParts';
import { sectionCopy } from './sectionCopy';
import { refundTraceHref } from './refundTrace';
import { oneLineSnippet } from './snippetLine';
import { TryIt } from './TryIt';
import { COMMITMENTS } from './commitments';
import { demoLinkText, STUDIO_HREF } from './links';

const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';

/** Renders the page and waits for every lazy part (sections, glass box, sources) to be in place. */
async function renderLanding() {
  const fetchSpy = vi.fn(() => Promise.reject(new Error('the landing fetched something')));
  vi.stubGlobal('fetch', fetchSpy);
  const utils = render(<Landing />);
  await act(async () => {
    await lazyPartsLoaded();
  });
  await screen.findByRole('heading', { name: 'On your laptop in one command.' });
  await screen.findByText('Where every number on this page comes from');
  await screen.findByText('What Jev was asked about the refund answer');
  expect(document.querySelector('[data-lazy-placeholder]')).toBeNull();
  return { ...utils, fetchSpy };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

/** A money, percentage, duration or multiple: the figures that need a label. */
const FIGURE = /\$\s?\d|\d\s?%|\d\s?ms\b|\d(\.\d+)?\s?s\b|\d\s?×/;

describe('landing page', () => {
  it('leads with the promise and at most seven sections', async () => {
    await renderLanding();
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(copy.product.tagline);
    const sections = screen.getByRole('main').querySelectorAll(':scope > section');
    expect(sections.length).toBe(7);
    for (const s of sections) expect(s.getAttribute('aria-labelledby')).toBeTruthy();
  });

  it('fetches nothing: the replay card is built with the page', async () => {
    const { fetchSpy } = await renderLanding();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('labels every number chip with where it comes from', async () => {
    await renderLanding();
    const chips = document.querySelectorAll<HTMLAnchorElement>('[data-value-label]');
    expect(chips.length).toBeGreaterThan(10);
    const kinds = new Set<string>();
    for (const chip of chips) {
      const label = chip.dataset.valueLabel as keyof typeof LABEL_TEXT;
      kinds.add(label);
      expect(chip).toHaveTextContent(LABEL_TEXT[label]);
      const target = chip.getAttribute('href')?.slice(1) ?? '';
      expect(document.getElementById(target), `chip ${target} points at a source entry`).not.toBeNull();
    }
    expect([...kinds].sort()).toEqual(['estimate', 'simulated', 'third_party', 'vendor']);
  });

  it('gives every number a chip', async () => {
    await renderLanding();
    const main = screen.getByRole('main');
    const walker = document.createTreeWalker(main, NodeFilter.SHOW_TEXT);
    let figures = 0;
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const text = node.textContent ?? '';
      if (!FIGURE.test(text)) continue;
      const el = node.parentElement as HTMLElement;
      // The reader's own calculator inputs are not claims, and a caveat qualifies the claim whose
      // chip it sits next to (checked below).
      if (el.closest('[data-input]') || el.closest('[data-caveat-for]')) continue;
      figures += 1;
      const scope = el.closest('[data-chip-scope]');
      expect(scope, `"${text.trim()}" has no chip next to it`).not.toBeNull();
      expect(scope?.querySelector('[data-value-label]'), `"${text.trim()}" has no chip next to it`).not.toBeNull();
    }
    expect(figures).toBeGreaterThan(15);
  });

  it('shows each claim’s caveat next to its chip', async () => {
    await renderLanding();
    const ids = new Set(landingClaims().map((c) => c.id));
    let caveats = 0;
    for (const chip of document.querySelectorAll<HTMLAnchorElement>('[data-value-label]')) {
      const id = chip.getAttribute('href')?.replace('#source-', '') ?? '';
      if (!ids.has(id)) continue; // a judge price in the calculator, not a claim
      const c = claim(id);
      if (!c.caveat) continue;
      caveats += 1;
      // The caveat sits in the chip's own scope, or in the small block that holds the claim.
      const scope = chip.closest('[data-caveat-scope]') ?? chip.closest('[data-chip-scope]');
      const note = scope?.querySelector(`[data-caveat-for="${id}"]`);
      expect(note, `${id}: its caveat sits next to its chip`).not.toBeNull();
      expect(note).toHaveTextContent(c.caveat);
      expect(scope?.querySelectorAll('[data-value-label]').length, `${id}: the block holding its caveat is small`).toBeLessThanOrEqual(2);
    }
    // The hero's third-party latency, the Arize test and OpenRouter's own test.
    expect(caveats).toBeGreaterThanOrEqual(3);
    const latency = document.querySelector('[data-caveat-for="jev_p50_latency_openrouter"]');
    expect(latency?.closest('section')?.getAttribute('aria-labelledby')).toBe('hero-title');
  });

  it('says "not yet measured" once instead of borrowing an estimate', async () => {
    await renderLanding();
    for (const id of ['gate_latency_p50_measured', 'fast_check_cost_per_1k_measured', 'ragtruth_qa_agreement_measured']) {
      expect(claim(id).hasValue).toBe(false);
    }
    expect(within(screen.getByRole('main')).getAllByText(/not yet measured/i)).toHaveLength(1);
    expect(document.querySelector('[data-value-label="measured"]')).toBeNull();
    const measured = document.querySelector('[data-measured]') as HTMLElement;
    expect(measured).toHaveTextContent('None published yet: measured fast-check cost; fast-check latency, p50; RAGTruth-QA agreement.');
  });

  it('lists the source of every landing claim, judge price and comparison cell in the footer', async () => {
    await renderLanding();
    for (const c of landingClaims()) expect(document.getElementById(`source-${c.id}`), c.id).not.toBeNull();
    const prices = within(screen.getByRole('list', { name: 'Judge prices in the calculator' })).getAllByRole('listitem');
    expect(prices).toHaveLength(judgeOptions().filter((o) => !o.local).length);
    const cited = new Set(compareRows.flatMap((r) => Object.values(r.cells).map((c) => c.source_url)).filter(Boolean));
    const group = screen.getByRole('list', { name: 'Comparison sources' });
    const links = within(group)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'));
    expect(new Set(links)).toEqual(cited);
  });

  it('sends "Try the demo" to the replay and offers make demo and GitHub', async () => {
    await renderLanding();
    const tries = screen.getAllByRole('link', { name: /try the demo/i });
    expect(tries.length).toBeGreaterThanOrEqual(2);
    for (const a of tries) expect(a.getAttribute('href')).toMatch(/\/app/);
    expect(screen.getAllByText('make demo').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByRole('link', { name: /github/i })[0].getAttribute('href')).toMatch(/^https:\/\/github\.com\//);
  });

  it('links its nav to every section it names, Try it and Checks included', async () => {
    await renderLanding();
    const nav = screen.getByRole('navigation', { name: 'Sections' });
    const links = within(nav).getAllByRole('link');
    expect(links.map((a) => a.textContent)).toEqual(expect.arrayContaining(['Try it', 'Checks']));
    for (const a of links) expect(document.getElementById((a.getAttribute('href') ?? '').slice(1)), a.textContent ?? '').not.toBeNull();
  });

  it('draws the request going out and the answer coming back through the gateway', async () => {
    await renderLanding();
    const figure = document.getElementById('how')?.querySelector('figure') as HTMLElement;
    expect(within(figure).getAllByText('request')).toHaveLength(2);
    expect(within(figure).getAllByText('answer')).toHaveLength(2);
    expect(within(figure).getByText(/· in the background, for flagged and sampled answers/)).toBeInTheDocument();
  });

  it('shows the replay tape with its own label, counts and one example per outcome', async () => {
    await renderLanding();
    const replay = document.querySelector('[data-tape-card]') as HTMLElement;
    expect(within(replay).getByText(MOCK_LABEL)).toBeInTheDocument();
    const manifest = committedManifest();
    expect(within(replay).getByText(new RegExp(`${new Intl.NumberFormat('en-US').format(manifest.traces.length)} requests`))).toBeInTheDocument();
    // A simulated tape is generated from the scenarios; it never claims a stack recorded it.
    const dated = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }).format(Date.parse(manifest.recorded_at));
    expect(within(replay).getByText(new RegExp(`Generated from the zero-key demo’s scripted scenarios, dated ${dated}`))).toBeInTheDocument();
    expect(within(replay).queryByText(/Recorded/)).toBeNull();
    // Its models are the mocks the label names: no raw "mock/mock" ids.
    expect(within(replay).queryByText(manifest.decision_model.snapshot ?? 'no snapshot')).toBeNull();
    expect(replay.textContent).not.toContain('mock/mock');
    // Its 50% flag rate is a showcase mix, and says so.
    expect(replay.querySelector('[data-showcase-mix]')).toHaveTextContent('not a real flag rate');
    const examples = within(within(replay).getByRole('list', { name: 'Examples from the tape' })).getAllByRole('listitem');
    expect(examples.length).toBeGreaterThanOrEqual(3);
    expect(examples.length).toBeLessThanOrEqual(5);
    const pills = examples.map((li) => li.firstElementChild?.textContent);
    expect(new Set(pills).size).toBe(pills.length);
    expect(pills).toEqual(expect.arrayContaining(['Passed', 'Flagged']));
    // The featured refund request is one of them (the glass-box panel links to it on the static site).
    expect(digest.examples.map((t) => t.trace_id)).toContain(scenarioTrace('refund-contradiction-flag').trace_id);
    expect(within(replay).getAllByText(REFUND.question, { exact: false }).length).toBeGreaterThanOrEqual(1);
    // Each example says what it was checked against, so the same question with and without
    // documents reads as two requests, not a duplicate.
    const docs = examples.map((li) => li.querySelector('[data-example-documents]')?.textContent);
    expect(docs).toContain('Checked against refund-policy.md');
    expect(docs).toContain('No documents to check against');
  });

  it('never says "Coming soon" and leaves no TODO', async () => {
    await renderLanding();
    expect(screen.queryByText(/coming soon/i)).toBeNull();
    expect(screen.queryByText(/TODO/)).toBeNull();
    expect(document.querySelector('[data-compare-todo]')).toBeNull();
    expect(screen.queryByText(/not in the online demo yet/)).toBeNull();
  });

  it('keeps the page up when a lazy part fails to load, and offers a reload', async () => {
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      const Gone = lazy(() => Promise.reject(new Error('the chunk is gone')));
      render(
        <main>
          <h1>Still here</h1>
          <LazyPart minHeight="1rem">
            <Gone />
          </LazyPart>
        </main>,
      );
      expect(await screen.findByText(/This part of the page could not be loaded/)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Reload the page' })).toBeEnabled();
      expect(screen.getByRole('heading', { name: 'Still here' })).toBeInTheDocument();
      expect(document.querySelector('[data-lazy-placeholder]')).toBeNull();
    } finally {
      quiet.mockRestore();
    }
  });

  it('waits for the calculator with the other lazy parts before a hash scroll', async () => {
    // The calculator sits above the sections a link can open (#self-host): the scroll must wait for it.
    const calculator = vi.spyOn(loaders, 'calculator');
    try {
      await renderLanding();
      expect(calculator).toHaveBeenCalled();
      expect(screen.getByLabelText('Deep-audit judge')).toBeInTheDocument();
      expect(document.querySelector('#cost [aria-busy="true"]')).toBeNull();
    } finally {
      calculator.mockRestore();
    }
  });

  it('has no axe violations', async () => {
    const { container } = await renderLanding();
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('try it', () => {
  it('links three live scenarios into the static Playground when live checks run', () => {
    render(<TryIt live />);
    const card = document.querySelector('[data-live-card="live"]') as HTMLElement;
    const links = [...card.querySelectorAll<HTMLAnchorElement>('a[data-live-scenario]')];
    expect(links.map((a) => a.getAttribute('href'))).toEqual(LIVE_SCENARIOS.map((id) => liveHref(import.meta.env.BASE_URL, id)));
    for (const a of links) expect(a.getAttribute('href')).toMatch(/app\/playground\/\?scenario=[a-z-]+#live$/);
    // The cost of one check carries its Estimate chip, and the cap is calls to OpenRouter.
    const cost = card.querySelector('[data-live-cost]') as HTMLElement;
    expect(cost.querySelector('[data-value-label="estimate"]')?.getAttribute('href')).toBe('#source-fast_check_cost_per_check');
    // "a typical check … costs about $0.0002", not "about ~$0.0002 per check".
    expect(cost).toHaveTextContent('a typical check (about 5,000 tokens of documents) costs about $0.0002');
    expect(cost.textContent).not.toMatch(/about ~|per check/);
    expect(cost).toHaveTextContent(`${playground.maxCalls} calls to OpenRouter`);
    expect(card.textContent).not.toMatch(new RegExp(`${playground.maxCalls}\\s+checks`));
    expect(card.textContent).not.toMatch(/\{max_calls\}|\{cost_per_check\}/);
    expect(within(card).getByText(sectionCopy.playground_static.consent[0])).toBeInTheDocument();
  });

  it('offers make demo instead, with no live link, where live checks do not run', () => {
    render(<TryIt live={false} />);
    const card = document.querySelector('[data-live-card="local"]') as HTMLElement;
    expect(within(card).queryAllByRole('link')).toHaveLength(0);
    expect(document.querySelector('a[href*="app/playground"]')).toBeNull();
    expect(within(card).getByText('Run it yourself:')).toBeInTheDocument();
    expect(within(card).getByText('make demo')).toBeInTheDocument();
    // Only the copy button of `make demo`: nothing that pretends to connect a key.
    expect(within(card).getAllByRole('button').map((b) => b.getAttribute('aria-label'))).toEqual(['Copy make demo']);
    expect(within(card).queryByRole('textbox')).toBeNull();
  });

  it('runs live only on the static site, never in the dashboard image’s build', async () => {
    // The test build is the full one (VITE_APP_MODE unset): its Playground has no live section.
    await renderLanding();
    expect(document.querySelector('[data-live-card]')?.getAttribute('data-live-card')).toBe('local');
  });
});

describe('write your own checks', () => {
  it('shows the four Studio steps with their costs and opens the Studio', async () => {
    await renderLanding();
    const steps = within(screen.getByRole('list', { name: 'Studio steps' })).getAllByRole('listitem');
    expect(steps.map((li) => li.querySelector('p')?.textContent?.replace(/^0\d/, ''))).toEqual(['Describe', 'Draft', 'Test on samples', 'Watch in shadow']);
    expect(steps[1].querySelector('[data-value-label]')?.getAttribute('href')).toBe('#source-studio_draft_cost_estimate');
    expect(steps[2].querySelector('[data-value-label]')?.getAttribute('href')).toBe('#source-fast_check_cost_per_check');
    const studio = screen.getByRole('link', { name: 'Open the Studio' });
    expect(studio.getAttribute('href')).toBe(STUDIO_HREF);
    expect(STUDIO_HREF).toMatch(/app\/evaluators\/studio\/?$/);
  });

  it('attaches the built-in commitments check the way the example policy does', () => {
    expect(COMMITMENTS.ref).toBe('commitments@1');
    expect(COMMITMENTS.exampleAllowed.length).toBeGreaterThan(10);
    expect(COMMITMENTS.options.some((o) => o.risky)).toBe(true);
  });
});

describe('what Jev was asked', () => {
  it('shows the compiled refund request: its evidence, questions and exact body', async () => {
    await renderLanding();
    const panel = document.querySelector('[data-glassbox]') as HTMLElement;
    expect(within(panel).getByText(REFUND.docText)).toBeInTheDocument();
    for (const q of glassbox.questions) expect(within(panel).getByText(q.name)).toBeInTheDocument();
    expect(within(panel).getByText(/"assistant_response": "You can get a full refund within 60 days of purchase."/)).toBeInTheDocument();
    // The token count is the request builder's estimate, and says so.
    expect(panel).toHaveTextContent(`an estimated ${new Intl.NumberFormat('en-US').format(glassbox.est_tokens)} tokens`);
    // The cost is the hero card's, with its Estimate chip; no time (the replay has its own).
    const chips = [...panel.querySelectorAll('[data-value-label]')].map((c) => [c.getAttribute('href'), c.getAttribute('data-value-label')]);
    expect(chips).toEqual([['#source-hero_card_cost_usd', 'estimate']]);
    expect(panel.textContent).toContain(`tokens ·${claim('hero_card_cost_usd').display}Estimate`);
  });

  it('prices the request it shows: its tokens at the Jev price (D168)', () => {
    const cost = claim('hero_card_cost_usd');
    const priced = priceCall({ provider: 'openrouter', model: glassbox.model, input_tokens: glassbox.est_tokens, output_tokens: 0, reported_cost_usd: null, table: PRICE_TABLE });
    expect(priced.cost_usd).not.toBeNull();
    // The claim is that price to two significant digits, and says it is computed from these tokens.
    expect(cost.value).toBe(Number(priced.cost_usd?.toPrecision(2)));
    expect(cost.label).toBe('estimate');
    const entries = claimsDoc.claims as { id: string; tokens_x_price?: { input_tokens: number; price_claim: string } }[];
    expect(entries.find((c) => c.id === 'hero_card_cost_usd')?.tokens_x_price).toEqual({ input_tokens: glassbox.est_tokens, price_claim: 'jev_input_price_per_mtok' });
  });

  it('is the gate’s own compile of the pack’s refund scenario', () => {
    expect(glassbox.scenario).toBe(REFUND.id);
    expect(glassbox.model).toBe(assumptions.prices.decision_model.match.replaceAll('*', ''));
    expect(glassbox.evidence.map((e) => e.id)).toEqual([REFUND.docId]);
    expect(glassbox.questions.map((q) => q.evaluator)).toEqual(expect.arrayContaining(['groundedness@1', 'contradiction@1']));
    expect(glassbox.requests[0].state.assistant_response).toBe(REFUND.answer);
  });

  it('links the static site to the refund request on the tape, and the full build to the dashboard', () => {
    const base = import.meta.env.BASE_URL;
    // The digest's flagged example is the tape's featured trace: the refund scenario's first run.
    expect(refundTraceHref('static')).toEqual({ href: `${base}app/traffic/${scenarioTrace('refund-contradiction-flag').trace_id}`, exact: true });
    // A tape without the refund request opens the replay instead of a trace that is not there.
    expect(refundTraceHref('static', [])).toEqual({ href: DEMO_HREF, exact: false });
    // The dashboard image's build has its own traffic: no tape trace to point at.
    expect(refundTraceHref('full')).toEqual({ href: DEMO_HREF, exact: false });
    expect(DEMO_HREF).toBe(`${base}app`);
    // There it opens that stack's own dashboard, so it never calls it the replay.
    expect([demoLinkText('static'), demoLinkText('full')]).toEqual(['Open the replay', 'Open the dashboard']);
  });

  it('names the dashboard, not a replay, where the build has no replay', async () => {
    await renderLanding();
    // The test build is the full one: the tape card's link and the panel's go to the stack's dashboard.
    const tape = document.querySelector('[data-tape-card]') as HTMLElement;
    expect(within(tape).getByRole('link', { name: 'Open the dashboard' })).toHaveAttribute('href', DEMO_HREF);
    expect(document.querySelector('[data-glassbox-link]')).toHaveTextContent('Open the dashboard');
    expect(screen.queryByRole('link', { name: 'Open the replay' })).toBeNull();
  });
});

describe('comparison', () => {
  it('gives every vendor cell a link and a date, or "Not yet verified"', async () => {
    await renderLanding();
    const table = screen.getByRole('table');
    expect(within(table).getAllByRole('row')).toHaveLength(2 + compareRows.length);
    // The caption counts the contract's rows; it never promises re-checks nothing enforces.
    const howMany = ['no', 'one', 'two', 'three', 'four', 'five', 'six'][compareRows.length] ?? String(compareRows.length);
    expect(table.querySelector('caption')).toHaveTextContent(`TrustAgent compared with ${howMany} kinds of tools.`);
    expect(document.getElementById('compare')?.textContent).not.toMatch(/re-checked/);
    const cells = table.querySelectorAll<HTMLElement>('[data-compare-cell]');
    expect(cells).toHaveLength(compareRows.length * compareDoc.columns.length);
    for (const cell of cells) {
      if (cell.dataset.compareCell === 'unverified') {
        expect(cell).toHaveTextContent(copy.compare.not_verified);
        continue;
      }
      const link = within(cell).getByRole('link');
      expect(link.getAttribute('href')).toMatch(/^https:\/\//);
      expect(cell.querySelector('time')?.getAttribute('dateTime')).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    }
    for (const limit of copy.limits) expect(screen.getByText(limit)).toBeInTheDocument();
  });

  it('renders each cell from the contract, in the table and in the phone cards', async () => {
    await renderLanding();
    const cards = screen.getByRole('list', { name: 'Comparison' });
    // The cell's paragraph, matched on its whole text: its dates sit in their own spans.
    const cellText = (text: string) => (_: string, el: Element | null) => el?.tagName === 'P' && (el.textContent ?? '').includes(text);
    for (const row of compareRows) {
      for (const [col, cell] of Object.entries(row.cells)) {
        if (cell.value === 'unverified') continue;
        // Once in the table, once in the cards.
        expect(screen.getAllByText(cellText(cell.text)), `${row.id}.${col}`).toHaveLength(2);
        expect(within(cards).getAllByText(cellText(cell.text))).toHaveLength(1);
      }
    }
  });

  it('keeps every date on one line, in the cells and their sources', async () => {
    await renderLanding();
    const compare = document.getElementById('compare');
    if (!compare) throw new Error('no #compare section');
    const dates = [...compare.querySelectorAll<HTMLElement>('[data-compare-cell] *')].filter(
      (el) => el.children.length === 0 && /\d{4}-\d{2}-\d{2}/.test(el.textContent ?? ''),
    );
    const dated = compareRows.flatMap((r) => Object.values(r.cells)).filter((c) => c.value !== 'unverified' && c.as_of);
    // At least each verified cell's source date, in the table and in the cards.
    expect(dates.length).toBeGreaterThanOrEqual(2 * dated.length);
    for (const el of dates) {
      expect(el.textContent).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(el).toHaveClass('whitespace-nowrap');
    }
  });
});

describe('calculator', () => {
  const inputs = (over: Partial<CalculatorInputs> = {}): CalculatorInputs => ({ ...defaultCalculatorInputs(1000), ...over });
  const ok = (c: CalculatorInputs) => {
    const r = monthlyCost(c);
    if (!r.ok) throw new Error(r.problem.message);
    return r.cost;
  };

  it('equals the stated default total per 1,000 answers within its tolerance', () => {
    const stated = assumptions.figures.find((f) => f.id === 'default_total_per_1k');
    const total = ok(inputs()).totalPer1k;
    expect(Math.abs(total - (stated?.per_1k_usd ?? 0))).toBeLessThanOrEqual(stated?.tolerance ?? 0);
    expect(Math.abs(total - (claim('default_total_cost_per_1k').value ?? 0))).toBeLessThanOrEqual(stated?.tolerance ?? 0);
    expect(total).toBe(3.0145);
  });

  it('says the fast check is about 27× cheaper than the same questions to an LLM judge', () => {
    const cost = ok(inputs());
    expect(formatMultiple(cost.sameChecksMultiple)).toBe('≈ 27×');
    expect(claim('same_checks_cost_multiple').display).toBe('about 27×');
    // 1,000 answers: the monthly figure is the per-1,000 one.
    expect(Math.abs(cost.sameChecks - (claim('llm_same_checks_cost_per_1k').value ?? 0))).toBeLessThan(0.5);
  });

  it('changes only the deep-audit cost (and the total) when the judge changes', () => {
    const base = ok(inputs());
    const local = judgeOptions().find((o) => o.local);
    expect(local).toBeDefined();
    const free = ok(inputs({ judge: { kind: 'table', option: local! } }));
    expect(free.deepAudit).toBe(0);
    expect(free.total).toBe(free.fastCheck);
    expect([free.fastCheck, free.sameChecks, free.fullAudit, free.auditedShare]).toEqual([base.fastCheck, base.sameChecks, base.fullAudit, base.auditedShare]);
    const pricey = ok(inputs({ judge: { kind: 'custom', inputPerMTok: 3, outputPerMTok: 15 } }));
    expect(pricey.deepAudit).toBeGreaterThan(base.deepAudit);
    expect([pricey.fastCheck, pricey.sameChecks, pricey.fullAudit]).toEqual([base.fastCheck, base.sameChecks, base.fullAudit]);
  });

  it('scales with traffic and never makes the fast check dearer than the LLM', () => {
    const small = ok(inputs({ answersPerMonth: 1000, tokensPerAnswer: 4000 }));
    const large = ok(inputs({ answersPerMonth: 1_000_000, tokensPerAnswer: 4000 }));
    expect(large.total / small.total).toBeCloseTo(1000, 6);
    expect(small.fastCheck).toBeLessThan(small.sameChecks);
    expect(small.sameChecks).toBeLessThan(small.fullAudit);
    expect(small.deepAudit).toBeCloseTo(small.total - small.fastCheck, 12);
  });

  it('returns a field problem instead of throwing on a price it cannot use', () => {
    for (const [inputPerMTok, outputPerMTok, field] of [
      [-1, 5, 'custom_input'],
      [Number.NaN, 5, 'custom_input'],
      [1, Number.POSITIVE_INFINITY, 'custom_output'],
    ] as const) {
      const r = monthlyCost(inputs({ judge: { kind: 'custom', inputPerMTok, outputPerMTok } }));
      expect(r.ok).toBe(false);
      if (!r.ok) expect(r.problem.field).toBe(field);
    }
    const bad = monthlyCost(inputs({ flagRate: 2 }));
    expect(!bad.ok && bad.problem.field).toBe('settings');
    // A token count that is not whole is rounded up, never a RangeError (D150).
    expect(ok(inputs({ tokensPerAnswer: 4999.2 })).fastCheck).toBe(ok(inputs({ tokensPerAnswer: 5000 })).fastCheck);
  });

  it('offers the judges the price table prices: one per model, no Jev, data-driven', () => {
    const options = judgeOptions();
    expect(options.map((o) => o.name)).toEqual(['Claude Haiku 4.5', 'Local model (Ollama)']);
    expect(referenceJudge(options).ref).toEqual({ provider: 'anthropic', model: 'claude-haiku-4-5' });
    // A sourced Sonnet pair would appear with no code change (D149), once per model.
    const sonnet = { input_per_mtok: 3, output_per_mtok: 15, label: 'vendor_price' as const, source: 'https://example.com', as_of: '2026-10-10' };
    const table: PricingTable = {
      ...PRICE_TABLE,
      entries: [...PRICE_TABLE.entries, { ...sonnet, match: 'claude-sonnet-4-5*', provider: 'anthropic' }, { ...sonnet, match: 'anthropic/claude-sonnet-4.5', provider: 'openrouter' }],
    };
    expect(judgeOptions(table).map((o) => o.name)).toEqual(['Claude Haiku 4.5', 'Local model (Ollama)', 'Claude Sonnet 4.5']);
    // The same model at two prices is two options, each naming its provider.
    const split: PricingTable = {
      ...table,
      entries: table.entries.map((e) => (e.provider === 'openrouter' && e.match === 'anthropic/claude-sonnet-4.5' ? { ...e, input_per_mtok: 3.3 } : e)),
    };
    expect(judgeOptions(split).map((o) => o.name)).toEqual(['Claude Haiku 4.5', 'Local model (Ollama)', 'Claude Sonnet 4.5 (Anthropic)', 'Claude Sonnet 4.5 (OpenRouter)']);
    expect(modelName({ match: 'anthropic/claude-haiku-4.5', provider: 'openrouter' })).toBe('Claude Haiku 4.5');
  });

  it('updates the deep-audit row only when the reader picks another judge, and shows a field error for a bad price', async () => {
    await renderLanding();
    const row = (id: string) => document.querySelector(`[data-cost-row="${id}"]`)?.lastElementChild?.textContent;
    const before = Object.fromEntries(['fast_check', 'deep_audit', 'total', 'same_checks', 'full_audit'].map((id) => [id, row(id)]));
    expect(before.total).toBe('$301');
    // The audited share reads as the sum it is, next to the sliders it comes from.
    expect(document.querySelector('[data-cost-row="deep_audit"]')).toHaveTextContent(
      `${auditShareWords(defaultCalculatorInputs().flagRate, defaultCalculatorInputs().passSampleRate, 0.079)}`,
    );
    expect(auditShareWords(0.05, 0.02, 0.079)).toBe('5% flagged + 1.9% sampled + 1% uniform = 7.9% of answers');
    const select = screen.getByLabelText('Deep-audit judge');
    fireEvent.change(select, { target: { value: judgeOptions().find((o) => o.local)?.id } });
    expect(row('deep_audit')).toBe('$0.00');
    expect([row('fast_check'), row('same_checks'), row('full_audit')]).toEqual([before.fast_check, before.same_checks, before.full_audit]);
    fireEvent.change(select, { target: { value: 'custom' } });
    const input = screen.getByLabelText('Input, per million tokens');
    fireEvent.change(input, { target: { value: '-2' } });
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('Enter a price of $0 or more per million tokens.')).toBeInTheDocument();
    expect(document.querySelector('[data-cost-row]')).toBeNull();
    expect(screen.getByText('Enter both judge prices, $0 or more, to see the estimate.')).toBeInTheDocument();
    fireEvent.change(input, { target: { value: '1' } });
    expect(row('deep_audit')).toBe(before.deep_audit);
  });
});

describe('run it yourself', () => {
  it('shows compose_up, encrypted keys, build-or-pull and the one SDK line', async () => {
    await renderLanding();
    const section = document.getElementById('self-host') as HTMLElement;
    expect(within(section).getByText('python3 scripts/configure.py --ensure && docker compose up -d --wait')).toBeInTheDocument();
    expect(section).toHaveTextContent('keys are encrypted at rest');
    expect(section).toHaveTextContent('Pulls the prebuilt images of a published release, otherwise builds them locally.');
    expect(oneLineSnippet()).toBe('client = OpenAI(base_url="http://localhost:8080/v1")');
    expect(section.querySelector('[data-snippet]')).toHaveTextContent(oneLineSnippet());
  });
});

describe('hero answer card', () => {
  it('is the scenario pack’s refund example: "60 days" against "30 days"', () => {
    const refund = scenarioPack.scenarios.find((s) => s.id === 'refund-contradiction-flag');
    expect(REFUND.answer).toBe(refund?.response);
    expect(REFUND.quote).toBe('60 days');
    expect(REFUND.evidenceNumber).toBe('30 days');
    expect(REFUND.docId).toBe('refund-policy.md');
  });

  it('labels itself a simulated example, with one chip per number and the document it was checked against', () => {
    const matchMedia = window.matchMedia;
    window.matchMedia = ((q: string) => ({ ...matchMedia(q), matches: q.includes('reduce') })) as typeof window.matchMedia;
    try {
      const { container } = render(<HeroCard />);
      expect(screen.getByText('Example · Simulated')).toBeInTheDocument();
      const chips = [...container.querySelectorAll('[data-value-label]')].map((c) => c.getAttribute('href'));
      expect(chips).toEqual(['#source-hero_card_latency_ms', '#source-hero_card_cost_usd']);
      expect(container.querySelector('[data-checked-against]')).toHaveTextContent('Checked against: refund-policy.md');
    } finally {
      window.matchMedia = matchMedia;
    }
  });

  it('plays its three beats, and shows the last one at once with reduced motion', async () => {
    vi.useFakeTimers();
    try {
      const { container, unmount } = render(<HeroCard />);
      const mark = () => container.querySelector('mark.hero-mark');
      const chipsHidden = () => [...container.querySelectorAll('[data-value-label]')].map((c) => c.closest('[inert]') !== null);
      expect(mark()).toBeNull();
      // A beat not shown yet is hidden from the keyboard too: its chips cannot take focus unseen.
      expect(chipsHidden()).toEqual([true, true]);
      await act(async () => {
        vi.advanceTimersByTime(10_000);
      });
      expect(chipsHidden()).toEqual([false, false]);
      expect(mark()).toHaveTextContent('60 days');
      expect(mark()).toHaveAttribute('data-on');
      expect(screen.getByRole('button', { name: /play again/i })).toBeEnabled();
      unmount();

      const matchMedia = window.matchMedia;
      window.matchMedia = ((q: string) => ({ ...matchMedia(q), matches: q.includes('reduce') })) as typeof window.matchMedia;
      try {
        const reduced = render(<HeroCard />);
        expect(reduced.container.querySelector('mark.hero-mark')).toHaveAttribute('data-on');
        // Nothing moves, so there is nothing to play again (no button that does nothing).
        expect(screen.queryByRole('button', { name: /play again/i })).toBeNull();
      } finally {
        window.matchMedia = matchMedia;
      }
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('claims', () => {
  it('drops a literal [estimate] tag after a claim: the chip says it', () => {
    const parts = splitClaims('for {claim:fast_check_cost_per_1k} [estimate] — and more');
    expect(parts).toHaveLength(3);
    expect('claim' in parts[1] && parts[1].claim.label).toBe('estimate');
    expect('text' in parts[2] && parts[2].text).toBe(' — and more');
  });

  it('throws on an unknown claim rather than print a number without a source', () => {
    expect(() => claim('nope')).toThrow(/no claim nope/);
  });
});

describe('landing entry', () => {
  // Walk the landing's import graph from its entry, lazy chunks included, and collect what it pulls in.
  const [fsSpecifier, urlSpecifier] = ['node:fs', 'node:url'];

  async function walk(): Promise<{ packages: Set<string>; files: Set<string>; src: string }> {
    const { readFileSync, existsSync } = (await import(/* @vite-ignore */ fsSpecifier)) as {
      readFileSync: (p: string, e: 'utf8') => string;
      existsSync: (p: string) => boolean;
    };
    const { fileURLToPath } = (await import(/* @vite-ignore */ urlSpecifier)) as { fileURLToPath: (u: string) => string };
    // Through a variable: Vite rewrites `new URL('…', import.meta.url)` into an asset URL.
    const here = import.meta.url;
    const src = fileURLToPath(new URL('../', here).href);
    const resolveLocal = (from: string, spec: string): string | null => {
      const base = spec.startsWith('@/') ? `${src}${spec.slice(2)}` : spec.startsWith('.') ? fileURLToPath(new URL(spec, `file://${from}`).href) : null;
      if (!base) return null;
      return [base, `${base}.ts`, `${base}.tsx`, `${base}/index.ts`].find((p) => existsSync(p) && !p.endsWith('/')) ?? null;
    };
    const packages = new Set<string>();
    const files = new Set<string>();
    const queue = [`${src}landing/main.tsx`];
    while (queue.length > 0) {
      const file = queue.pop() as string;
      if (files.has(file)) continue;
      files.add(file);
      if (file.endsWith('.json')) continue;
      const text = readFileSync(file, 'utf8');
      for (const m of text.matchAll(/(?:^|\n)\s*(?:import|export)\s[^'"]*?from\s+'([^']+)'|import\(\s*'([^']+)'\s*\)|(?:^|\n)import\s+'([^']+)'/g)) {
        const spec = m[1] ?? m[2] ?? m[3];
        if (spec.startsWith('@contracts/') || spec.startsWith('virtual:')) continue;
        const local = resolveLocal(file, spec.replace(/\?.*$/, ''));
        if (local) queue.push(local);
        else if (!spec.startsWith('.') && !spec.startsWith('@/')) packages.add(spec.startsWith('@') ? spec.split('/').slice(0, 2).join('/') : spec.split('/')[0]);
      }
    }
    return { packages, files, src };
  }

  it('never imports the router, a chart library, the dashboard kit or the live code', async () => {
    const { packages, files, src } = await walk();
    expect(files.size).toBeGreaterThan(15);
    // The lazy parts are walked too.
    expect([...files].some((f) => f.endsWith('landing/TryIt.tsx'))).toBe(true);
    for (const banned of ['@tanstack/react-router', '@tanstack/react-query', 'recharts', '@base-ui/react', 'motion', 'zustand', 'sonner']) {
      expect(packages.has(banned), `the landing imports ${banned}`).toBe(false);
    }
    for (const dir of ['components/ui/', 'lib/openrouter/', 'lib/gate/', 'components/replay/live/']) {
      expect(
        [...files].filter((f) => f.startsWith(`${src}${dir}`)),
        `the landing imports ${dir}`,
      ).toEqual([]);
    }
  });
});

describe('landing copy', () => {
  it('ships exactly the contract words it reads, nothing else', () => {
    // vite.site.ts copyModules serves only the listed parts; each must equal the contract's.
    expect(landingCopy).toEqual({ product: copy.product, tiers: copy.tiers, value_labels: copy.value_labels });
    expect(sectionCopy).toEqual({
      limits: copy.limits,
      playground_static: copy.playground_static,
      savings: copy.savings,
      compare: copy.compare,
      setup: { step_connect: { privacy: copy.setup.step_connect.privacy } },
    });
  });
});
