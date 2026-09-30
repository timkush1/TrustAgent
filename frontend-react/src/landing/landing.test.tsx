/**
 * The landing page (SPEC §8.1, §3.3): at most seven sections, every number from value-claims with
 * its label chip, honest "not yet measured" and "coming soon" states with no dead controls, the
 * replay's own label, and a separate entry that never pulls in the router or a chart library.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import { copy } from '@contracts/ui/copy.v1.json';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import { axeViolations, formatViolations } from '@/test/axe';
import { recordingFetch } from '@/test/recording';
import { claim, LABEL_TEXT, landingClaims, splitClaims } from './claims';
import { COST_FIGURES, monthlyCost, perAnswer, statedPer1k } from './costModel';
import { REFUND } from './content';
import { HeroCard } from './HeroCard';
import { Landing } from './Landing';

const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';

async function renderLanding() {
  vi.stubGlobal('fetch', recordingFetch('/').fetch);
  const utils = render(<Landing />);
  // The replay card loads the recording's manifest after the first paint.
  await screen.findByText(MOCK_LABEL);
  return utils;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('landing page', () => {
  it('leads with the promise and at most seven sections', async () => {
    await renderLanding();
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(copy.product.tagline);
    const main = screen.getByRole('main');
    const sections = main.querySelectorAll(':scope > section');
    expect(sections.length).toBeGreaterThan(0);
    expect(sections.length).toBeLessThanOrEqual(7);
    for (const s of sections) expect(s.getAttribute('aria-labelledby')).toBeTruthy();
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

  it('says "not yet measured" instead of borrowing an estimate', async () => {
    await renderLanding();
    for (const id of ['gate_latency_p50_measured', 'fast_check_cost_per_1k_measured', 'ragtruth_qa_agreement_measured']) {
      expect(claim(id).hasValue).toBe(false);
    }
    expect(screen.getAllByText('not yet measured').length).toBeGreaterThanOrEqual(3);
    expect(document.querySelector('[data-value-label="measured"]')).toBeNull();
  });

  it('lists the source of every landing claim in the footer', async () => {
    await renderLanding();
    for (const c of landingClaims()) expect(document.getElementById(`source-${c.id}`), c.id).not.toBeNull();
  });

  it('sends "Try the demo" to the replay and offers make demo and GitHub', async () => {
    await renderLanding();
    const tries = screen.getAllByRole('link', { name: /try the demo/i });
    expect(tries.length).toBeGreaterThanOrEqual(2);
    for (const a of tries) expect(a.getAttribute('href')).toMatch(/\/app/);
    expect(screen.getAllByText('make demo').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByRole('link', { name: /github/i })[0].getAttribute('href')).toMatch(/^https:\/\/github\.com\//);
  });

  it('draws the request going out and the answer coming back through the gateway', async () => {
    await renderLanding();
    const figure = document.getElementById('how')?.querySelector('figure') as HTMLElement;
    expect(within(figure).getAllByText('request')).toHaveLength(2);
    expect(within(figure).getAllByText('answer')).toHaveLength(2);
    expect(within(figure).getByText(/· in the background, for flagged and sampled answers/)).toBeInTheDocument();
  });

  it('shows the replay with its own label, and the live playground as coming soon with no control', async () => {
    await renderLanding();
    const replay = screen.getByRole('heading', { name: 'The replay' }).closest('div[class*="flex-col"]') as HTMLElement;
    expect(within(replay).getByText(MOCK_LABEL)).toBeInTheDocument();
    expect(within(replay).getByText(/18 requests/)).toBeInTheDocument();
    // A simulated tape is generated from the scenarios; it never claims a stack recorded it.
    expect(within(replay).getByText(/Generated from the zero-key demo’s scripted scenarios, dated 28 Sept? 2026/)).toBeInTheDocument();
    expect(within(replay).queryByText(/Recorded/)).toBeNull();
    const live = screen.getByRole('heading', { name: /try it live in your browser/i }).closest('div[class*="border-dashed"]') as HTMLElement;
    expect(within(live).getByText('Coming soon')).toBeInTheDocument();
    // Only the copy button of `make demo`: nothing that pretends to connect a key.
    expect(within(live).getAllByRole('button').map((b) => b.getAttribute('aria-label'))).toEqual(['Copy make demo']);
    expect(within(live).queryByRole('textbox')).toBeNull();
  });

  it('marks every other product’s comparison cell as a dated TODO', async () => {
    await renderLanding();
    const table = screen.getByRole('table');
    const rows = within(table).getAllByRole('row');
    expect(rows).toHaveLength(6);
    const todos = table.querySelectorAll('[data-compare-todo]');
    expect(todos).toHaveLength(4 * 5);
    for (const t of todos) expect(t).toHaveTextContent(/TODO: verify · \d{4}-\d{2}/);
    for (const limit of copy.limits) expect(screen.getByText(limit)).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = await renderLanding();
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });

  it('says the recording could not be loaded instead of spinning forever', async () => {
    vi.stubGlobal('fetch', recordingFetch('/', { fail: true }).fetch);
    render(<Landing />);
    expect(await screen.findByText(/could not be loaded here/)).toBeInTheDocument();
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

  it('plays its three beats, and shows the last one at once with reduced motion', async () => {
    vi.useFakeTimers();
    try {
      const { container, unmount } = render(<HeroCard />);
      const mark = () => container.querySelector('mark.hero-mark');
      expect(mark()).toBeNull();
      await act(async () => {
        vi.advanceTimersByTime(10_000);
      });
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

describe('cost model', () => {
  it('reproduces every stated estimate within its tolerance', () => {
    for (const id of COST_FIGURES) {
      const stated = statedPer1k(id);
      expect(stated, id).not.toBeNull();
      if (stated) expect(Math.abs(perAnswer(id) * 1000 - stated.value), id).toBeLessThanOrEqual(stated.tolerance + 1e-12);
    }
  });

  it('scales with traffic and never makes the fast check dearer than the LLM', () => {
    const base = { tokensPerAnswer: 4000, flagRate: 0.05, passSampleRate: 0.02 };
    const small = monthlyCost({ ...base, answersPerMonth: 1000 });
    const large = monthlyCost({ ...base, answersPerMonth: 1_000_000 });
    expect(large.total / small.total).toBeCloseTo(1000, 6);
    expect(small.fastCheck).toBeLessThan(small.sameChecks);
    expect(small.sameChecks).toBeLessThan(small.fullAudit);
    expect(small.deepAudit).toBeCloseTo(small.total - small.fastCheck, 12);
  });
});

describe('landing entry', () => {
  // Walk the landing's import graph from its entry and collect the packages it pulls in.
  const [fsSpecifier, urlSpecifier] = ['node:fs', 'node:url'];

  it('never imports the router, a chart library or the dashboard kit', async () => {
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
    const seen = new Set<string>();
    const queue = [`${src}landing/main.tsx`];
    while (queue.length > 0) {
      const file = queue.pop() as string;
      if (seen.has(file)) continue;
      seen.add(file);
      const text = readFileSync(file, 'utf8');
      for (const m of text.matchAll(/(?:^|\n)\s*(?:import|export)\s[^'"]*?from\s+'([^']+)'|import\(\s*'([^']+)'\s*\)|(?:^|\n)import\s+'([^']+)'/g)) {
        const spec = m[1] ?? m[2] ?? m[3];
        if (spec.startsWith('@contracts/') || spec.startsWith('virtual:')) continue;
        const local = resolveLocal(file, spec.replace(/\?.*$/, ''));
        if (local) queue.push(local);
        else if (!spec.startsWith('.') && !spec.startsWith('@/')) packages.add(spec.startsWith('@') ? spec.split('/').slice(0, 2).join('/') : spec.split('/')[0]);
      }
    }
    expect(seen.size).toBeGreaterThan(10);
    for (const banned of ['@tanstack/react-router', '@tanstack/react-query', 'recharts', '@base-ui/react', 'motion', 'zustand', 'sonner']) {
      expect(packages.has(banned), `the landing imports ${banned}`).toBe(false);
    }
    expect([...seen].some((f) => f.includes('/components/ui/'))).toBe(false);
  });
});
