/**
 * The static build's chrome over the committed recording (SPEC §3.3 "Static build", §0 rows 4-5):
 * the six static nav items, the simulated banner, the hidden pages, the recorded Playground and
 * the browser Studio.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { configure, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ServicesContext, type AppServices } from '@/app/services';
import { NotFound, RouteError } from '@/components/common/RouteStates';
import { buildRuntimeConfig } from '@/config/runtime';
import { createReplayDataSource } from '@/data/replay/replayDataSource';
import { useReplayStore } from '@/data/replay/replayStore';
import { useStreamStore } from '@/data/stream/streamStore';
import { copy, fillTemplate } from '@/domain/presentation';
import { routeTree } from '@/routeTree.gen';
import { axeViolations, formatViolations } from '@/test/axe';
import { committedManifest, ManualClock, recordingFetch } from '@/test/recording';
import { createTestQueryClient } from '@/test/renderApp';
import { GUIDE_STORAGE_KEY, useGuideState } from './guideState';
import { hiddenStaticPage } from './staticPages';

const TRACES = committedManifest().traces.length;

async function renderStatic(path: string, opts: { manifest?: unknown; dataset?: 'mock' | 'recorded' } = {}) {
  const config = buildRuntimeConfig(undefined, {
    location: { href: 'https://example.github.io/TrustAgent/', origin: 'https://example.github.io', protocol: 'https:' },
    env: { BASE_URL: '/TrustAgent/', VITE_APP_MODE: 'static' },
  });
  const tape = recordingFetch('/TrustAgent/', { manifest: opts.manifest });
  const dataSource = createReplayDataSource({ basePath: config.basePath, fetchImpl: tape.fetch, clock: new ManualClock(), dataset: opts.dataset });
  await dataSource.ready;
  const queryClient = createTestQueryClient();
  const services: AppServices = { config, dataSource, queryClient };
  const router = createRouter({
    routeTree,
    context: services,
    history: createMemoryHistory({ initialEntries: [path] }),
    defaultNotFoundComponent: NotFound,
    defaultErrorComponent: RouteError,
    defaultPendingMs: 0,
  });
  const utils = render(
    <ServicesContext.Provider value={services}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ServicesContext.Provider>,
  );
  return { ...utils, router, dataSource, requests: tape.requests };
}

beforeEach(() => {
  useStreamStore.getState().reset();
  // Every test is a first visit: the tour has not been hidden.
  window.localStorage.removeItem(GUIDE_STORAGE_KEY);
  useGuideState.getState().reload();
});
// The first render of a file loads the lazily split route modules; under load that can take longer
// than the 1 s default.
configure({ asyncUtilTimeout: 5000 });

describe('hiddenStaticPage', () => {
  it('hides the pages that need a gateway, with or without the base path', () => {
    expect(hiddenStaticPage('/app/policies')?.name).toBe('Policies');
    expect(hiddenStaticPage('/TrustAgent/app/settings/models')?.name).toBe('Settings');
    expect(hiddenStaticPage('/app/knowledge')?.name).toBe('Knowledge');
    expect(hiddenStaticPage('/app/evaluators/groundedness')?.name).toBe('Evaluators');
    expect(hiddenStaticPage('/app/setup')?.name).toBe('Setup');
    // Benchmarks is part of Evaluators, which the static nav does not list (D111, CO-4).
    expect(hiddenStaticPage('/app/evaluators/benchmarks')?.name).toBe('Evaluators');
    expect(hiddenStaticPage('/TrustAgent/app/evaluators/benchmarks/run-1')?.name).toBe('Evaluators');
  });

  it('keeps the replay pages and the Studio', () => {
    for (const path of ['/app/overview', '/app/traffic', '/app/traffic/abc', '/app/playground', '/app/savings', '/app/evaluators/studio', '/app/evaluators/studio/x/1', '/app/policiesx']) {
      expect(hiddenStaticPage(path), path).toBeNull();
    }
  });
});

describe('static build', () => {
  it('lists Overview (simulated on the simulated tape), Traffic (replay), Trace, Playground, Studio and Run it yourself', async () => {
    await renderStatic('/app/traffic');
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    const labels = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('aria-label'))
      .filter(Boolean);
    expect(labels).toEqual(['Overview', 'Traffic', 'Trace', 'Playground', 'Studio', 'Run it yourself']);
    expect(within(nav).getByRole('link', { name: 'Run it yourself' })).toHaveAttribute('href', '/TrustAgent/#self-host');
    // The committed tape is the simulated one: the nav never calls it recorded (D164).
    expect(within(nav).getByText('simulated')).toBeInTheDocument();
    expect(within(nav).queryByText('recorded')).toBeNull();
    expect(within(nav).getByText('replay')).toBeInTheDocument();
  });

  it('says the traffic is simulated and replayed, in one banner that says “simulated” once (D117)', async () => {
    const { container } = await renderStatic('/app/traffic');
    await waitFor(() => expect(container.querySelectorAll('[data-banner]')).toHaveLength(1));
    const banner = container.querySelector('[data-banner="replay"]') as HTMLElement;
    expect(within(banner).getByText(copy.recording.banner_simulated)).toBeInTheDocument();
    expect(banner.textContent?.match(/simulated/gi)).toHaveLength(1);
    expect(banner).not.toHaveTextContent(/recorded/i);
    expect(within(banner).getByRole('link', { name: 'Run it yourself' })).toHaveAttribute('href', '/TrustAgent/#self-host');
    expect(banner).toHaveTextContent(/Run it yourself with make demo\.$/);
    // No Health pill or demo badge for a gateway that is not there.
    expect(screen.queryByRole('button', { name: /System health/ })).toBeNull();
    expect(screen.getByRole('button', { name: /replay the recorded traffic again/i })).toBeEnabled();
  });

  it('says when and with which models a recorded tape was made, and that it is not live', async () => {
    const manifest = committedManifest();
    const recordedTape = {
      ...manifest,
      kind: 'recorded',
      label: 'Recorded 28 Sep 2026 · typesafe/jev-1.13 + claude-haiku-4.5 · scripted answers · replayed, not live',
      decision_model: { backend: 'openrouter-systemone', snapshot: 'typesafe/jev-1.13' },
      judge: { provider: 'anthropic', model: 'claude-haiku-4.5', fallback_active: false },
    };
    const { container } = await renderStatic('/app/traffic', { manifest: recordedTape, dataset: 'recorded' });
    const banner = await waitFor(() => {
      const el = container.querySelector('[data-banner="replay"]');
      if (!(el instanceof HTMLElement)) throw new Error('no banner yet');
      return el;
    });
    expect(banner).toHaveAttribute('data-recording-kind', 'recorded');
    expect(banner).toHaveTextContent(/^Recorded 28 Sept? 2026 with typesafe\/jev-1\.13 and claude-haiku-4\.5, replayed in your browser: not live\./);
    expect(banner).not.toHaveTextContent(/simulated/i);
    // The nav's Overview hint names the same tape (D164).
    const nav = screen.getByRole('navigation', { name: 'Main' });
    expect(within(nav).getByText('recorded')).toBeInTheDocument();
    expect(within(nav).queryByText('simulated')).toBeNull();
  });

  it('lists the recorded requests on Traffic', async () => {
    await renderStatic('/app/traffic');
    const table = () => screen.getByRole('table', { name: 'Requests' });
    const rows = () =>
      within(table())
        .getAllByRole('row')
        .filter((r) => r.hasAttribute('data-trace-id'));
    // Half of the recording is on screen before the tape plays (the table renders a window of it).
    await waitFor(() => expect(table()).toHaveAttribute('aria-rowcount', String(Math.floor(TRACES / 2) + 1)));
    expect(rows().length).toBeGreaterThan(0);
    const times = rows().map((r) => r.querySelector('time')?.textContent ?? '');
    // Times follow the tape's clock ("just now", "1 hr. ago"), not the days since the recording.
    expect(times.filter((t) => /Sep/.test(t))).toEqual([]);
    expect(times.some((t) => /now|ago/.test(t))).toBe(true);
  });

  it('shows what was caught and against which document on a filtered Traffic', async () => {
    await renderStatic('/app/traffic?route=support-bot&outcome=flagged');
    const rows = () =>
      within(screen.getByRole('table', { name: 'Requests' }))
        .getAllByRole('row')
        .filter((r) => r.hasAttribute('data-trace-id'));
    await waitFor(() => expect(rows().some((r) => within(r).queryByText('refund-policy.md'))).toBe(true));
    for (const r of rows()) expect(within(r).getByText('Flagged')).toBeInTheDocument();
    const flagged = rows().find((r) => within(r).queryByText('refund-policy.md')) as HTMLElement;
    expect(within(flagged).getByText('refund-policy.md')).toBeInTheDocument();
    expect(within(flagged).getByText('Hallucinated')).toBeInTheDocument();
  });

  it('shows why Policies is not here, with a real way to run it', async () => {
    await renderStatic('/app/policies');
    expect(await screen.findByRole('heading', { name: 'Policies is not part of the static demo' })).toBeInTheDocument();
    const main = screen.getByRole('main');
    expect(within(main).getByText('make demo')).toBeInTheDocument();
    expect(within(main).getByRole('link', { name: /Traffic/ })).toHaveAttribute('href', '/app/traffic');
    expect(formatViolations(await axeViolations(main))).toEqual([]);
  });

  it('answers a setup link with why sign-in needs a running stack, and probes no session or setup', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    const { dataSource, requests } = await renderStatic('/app/setup#token=not-a-real-token');
    expect(await screen.findByRole('heading', { name: 'Setup is not part of the static demo' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create the admin key' })).toBeNull();
    // The replay control plane answers without asking anyone.
    await expect(dataSource.getSession()).rejects.toMatchObject({ status: 404 });
    await expect(dataSource.getSetupStatus()).rejects.toMatchObject({ status: 404 });
    expect(requests.filter((url) => !url.includes('/demo/'))).toEqual([]);
    expect(fetchSpy).not.toHaveBeenCalled();
    fetchSpy.mockRestore();
  });

  it('opens the recorded runs from the Playground', async () => {
    await renderStatic('/app/playground');
    const list = await screen.findByRole('list', { name: 'Example requests' });
    const cards = within(list).getAllByRole('listitem');
    // One card per outcome the opening half of the tape shows: a short tour, not every request.
    const outcomes = cards.map((c) => c.querySelector('[data-outcome]')?.getAttribute('data-outcome'));
    expect(outcomes).toEqual(['passed', 'flagged', 'not_checked', 'would_block', 'replaced', 'blocked']);
    for (const card of cards) expect(within(card).getByRole('link', { name: /Open this request/ }).getAttribute('href')).toMatch(/^\/app\/traffic\//);
    expect(screen.getByText(/checked by the demo’s mock checks and mock judge \(simulated, not model output\)/)).toBeInTheDocument();
    expect(screen.queryByText(/checked by the demo stack/)).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('shows the Overview of the recording with its Simulated badge', async () => {
    await renderStatic('/app/overview');
    expect(await screen.findByText(new RegExp(`${TRACES} requests on 28 Sept? 2026`))).toBeInTheDocument();
    expect(screen.queryByRole('radiogroup', { name: 'Time window' })).toBeNull();
  });

  it('shows the recording’s Savings in its own words: per hour, one basis per comparison, no live sampling', async () => {
    await renderStatic('/app/overview?tab=savings');
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    // The recording spans two hours, so the chart draws per hour.
    const plot = await within(chart).findByRole('slider', { name: 'What checking cost per hour' });
    expect(plot).toHaveAccessibleDescription(/^Checking \d+ answers cost \$[\d.]+ over the replayed traffic\./);
    // The recording's mock judge is free while it deep-audits most answers: the chart never sets
    // that spend against the published full audit. The option stays, disabled, and says why.
    expect(within(chart).getByRole('radio', { name: 'Full audit' })).toHaveAttribute('data-disabled');
    expect(within(chart).getByRole('radiogroup', { name: 'Compare with' })).toHaveAccessibleDescription(
      /^Full audit is not comparable on the simulated demo: its mock judge is free and it deep-audits about \d+ in 100 answers\./,
    );
    expect(plot).not.toHaveAccessibleDescription(/auditing every answer/);
    // The table keeps the published figure, labelled an estimate, and says what to read it against.
    const row = screen.getByRole('rowheader', { name: /An LLM auditing every answer/ });
    expect(within(row).getByRole('button', { name: /^Estimate:/ })).toBeInTheDocument();
    expect(row).toHaveTextContent(/Compare it with TrustAgent with a real judge \(\$3 per 1,000 answers\) above, not with this simulated total/);
    // With a real judge, per 1,000 answers: the published estimates on one basis; the demo's own
    // rate comes after, in its own line.
    expect([...document.querySelectorAll('[data-alternative]')].map((el) => el.getAttribute('data-alternative'))).toEqual([
      'real_judge',
      'llm_same_checks',
      'llm_full_claim_audit',
    ]);
    expect(document.querySelector('[data-alternative="real_judge"]')).toHaveTextContent(
      /\$3.*Per 1,000 answers at the shipped defaults: a fast check on every answer \(\$0\.21\) \+ a deep audit of about 8 in 100 answers \(about \$2\.80\)/,
    );
    expect(document.querySelector('[data-alternative="llm_same_checks"]')).toHaveTextContent(/\$5\.75/);
    expect(document.querySelector('[data-alternative="llm_full_claim_audit"]')).toHaveTextContent(/\$35\.50/);
    expect(screen.getByText(/^The deep audit reads \$0 because the demo’s mock judge is free: the figures with a real judge are below\.$/)).toBeInTheDocument();
    expect(screen.getByText(/^This demo deep-audits about \d+ in 100 answers to show the audit flow/)).toBeInTheDocument();
    // No window to pick and no gateway to ask: no window words, no made-up sampling count.
    expect(screen.getByText('Over the replayed traffic, and per 1,000 checked answers.')).toBeInTheDocument();
    expect(screen.queryByText(/last 24 hours|pick a longer window|audits today/)).toBeNull();
    expect(screen.getByText(/^Sampling status is not part of the recording\./)).toBeInTheDocument();
    expect(screen.getByText('Not in the recording')).toBeInTheDocument();
  });

  it('opens the Studio page: it drafts and tests in the browser and exports, with no gateway (D107)', async () => {
    const { requests } = await renderStatic('/app/evaluators/studio');
    expect(await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ }, { timeout: 15_000 })).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: 'On your own stack' })).toBeInTheDocument();
    expect(requests.filter((r) => r.includes('/api/'))).toEqual([]);
  }, 30_000);

  it('hides Benchmarks like the rest of Evaluators, asking no gateway (D111)', async () => {
    const { requests } = await renderStatic('/app/evaluators/benchmarks');
    expect(await screen.findByRole('heading', { name: 'Evaluators is not part of the static demo' })).toBeInTheDocument();
    expect(screen.queryByText(/Committed runs only/)).toBeNull();
    expect(requests.filter((r) => r.includes('/api/'))).toEqual([]);
  });

  it('links the trace’s check into Studio, where its threshold can be tried, and says the route needs a stack (RP-4)', async () => {
    const featured = useReplayStore.getState().featuredTraceId;
    const user = userEvent.setup();
    const { router } = await renderStatic(`/app/traffic/${featured}`);
    const outcome = await screen.findByRole('region', { name: 'Outcome' });
    // Nothing links to the hidden evaluator or policy pages, and no dead button is left.
    expect(within(outcome).queryByRole('link', { name: /Open evaluator|Adjust threshold/ })).toBeNull();
    expect(within(outcome).queryByRole('button', { name: /Open evaluator|Adjust threshold/ })).toBeNull();
    const studio = within(outcome).getByRole('link', { name: new RegExp(`^${copy.trace.open_in_studio}: `) });
    expect(studio.getAttribute('href')).toMatch(/\/app\/evaluators\/studio\/[a-z_]+\/\d+$/);
    expect(outcome).toHaveTextContent(copy.trace.studio_static_note);
    // The checks list sends each check to Studio too, never to the hidden Policies page.
    const checks = await screen.findByRole('list', { name: 'Checks' });
    expect(within(checks).queryByRole('button', { name: /^Adjust threshold/ })).toBeNull();
    expect(within(checks).getAllByRole('button', { name: new RegExp(`^${copy.trace.open_in_studio}: `) }).length).toBeGreaterThan(0);
    for (const a of within(checks).getAllByRole('link')) expect(a.getAttribute('href')).toMatch(/\/app\/evaluators\/studio\/[a-z_]+\/\d+$/);
    // The note's fix is the landing's self-host section.
    expect(within(outcome).getByRole('link', { name: 'Set it up in one command' })).toHaveAttribute('href', '/TrustAgent/#self-host');
    // Labelling is a write too: it says the same, not "read-only instance".
    expect(within(outcome).queryByText(/read-only instance/)).toBeNull();
    expect(formatViolations(await axeViolations(outcome))).toEqual([]);
    // Studio opens the check as a copy.
    await user.click(studio);
    await waitFor(() => expect(router.state.location.pathname).toMatch(/\/app\/evaluators\/studio\/[a-z_]+\/\d+$/));
    expect(await screen.findByRole('heading', { level: 1, name: 'Studio' })).toBeInTheDocument();
  });

  it('shows the exact Jev request of a replayed trace, rebuilt in the browser (D109)', async () => {
    const featured = useReplayStore.getState().featuredTraceId;
    const { container, requests } = await renderStatic(`/app/traffic/${featured}`);
    const user = userEvent.setup();
    await user.click(await screen.findByText(copy.trace.show_jev_request));
    const panel = await waitFor(() => {
      const el = container.querySelector('[data-testid="jev-request"]');
      if (!el?.querySelector('[data-glassbox]')) throw new Error('not rebuilt yet');
      return el as HTMLElement;
    });
    expect(panel.querySelector('[data-glassbox]')).toHaveTextContent('Rebuilt in your browser · simulated recording');
    // What it read (the refund policy) and what it was asked.
    expect(within(panel).getAllByText(/: what it was asked$/).length).toBeGreaterThan(0);
    expect(panel).toHaveTextContent('30 days');
    expect(within(panel).queryByText(/no gateway to ask|make demo/)).toBeNull();
    // Rebuilt from the trace file already served: no gateway asked.
    expect(requests.filter((r) => r.includes('/api/'))).toEqual([]);
  });

  it('opens the tour on a first visit: four links, from the answer it caught to Savings (D117)', async () => {
    const featured = useReplayStore.getState().featuredTraceId as string;
    const { container } = await renderStatic('/app/traffic');
    const guide = await screen.findByRole('region', { name: copy.recording.guide.title });
    const links = within(guide).getAllByRole('link');
    expect(links.map((a) => a.textContent)).toEqual([
      copy.recording.guide.step1,
      copy.recording.guide.step2,
      copy.recording.guide.step3,
      copy.recording.guide.step4,
    ]);
    expect(links.map((a) => a.getAttribute('href'))).toEqual([
      `/app/traffic/${featured}`,
      `/app/traffic/${featured}#answer-title`,
      `/app/traffic/${featured}#jev-request`,
      '/app/overview?tab=savings',
    ]);
    // Step 1 is anchored to the row Traffic marks as featured.
    expect(links[0]).toHaveAttribute('data-guide-anchor', featured);
    // It sits in the page before the list, never over it.
    const table = await screen.findByRole('table', { name: 'Requests' });
    expect(guide.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(getComputedStyle(guide).position).not.toMatch(/absolute|fixed|sticky/);
    expect(formatViolations(await axeViolations(guide))).toEqual([]);

    // Hidden, it stays hidden (localStorage ta.replay.guide), here and on the next visit.
    await userEvent.setup().click(within(guide).getByRole('button', { name: copy.recording.guide.dismiss }));
    expect(screen.queryByRole('region', { name: copy.recording.guide.title })).toBeNull();
    expect(window.localStorage.getItem(GUIDE_STORAGE_KEY)).toBe('hidden');
    container.remove();
    useGuideState.getState().reload();
    await renderStatic('/app/traffic');
    await screen.findByRole('table', { name: 'Requests' });
    expect(screen.queryByRole('region', { name: copy.recording.guide.title })).toBeNull();
  });

  it('marks the answer the tour starts from in Traffic, and names it in the nav', async () => {
    const featured = useReplayStore.getState().featuredTraceId as string;
    await renderStatic('/app/traffic?route=support-bot&outcome=flagged');
    const table = await screen.findByRole('table', { name: 'Requests' });
    await waitFor(() => expect(table.querySelector(`[data-trace-id="${featured}"]`)).not.toBeNull());
    const marked = [...table.querySelectorAll('[data-featured]')];
    expect(marked.map((r) => r.getAttribute('data-trace-id'))).toEqual([featured]);
    expect(within(marked[0] as HTMLElement).getByRole('link')).toHaveAccessibleName(new RegExp(`${copy.recording.nav_hint_caught}$`));
    const nav = screen.getByRole('navigation', { name: 'Main' });
    expect(within(nav).getByRole('link', { name: 'Trace' })).toHaveAccessibleDescription(copy.recording.nav_hint_caught);
    expect(within(nav).getByText(copy.recording.nav_hints.trace)).toBeInTheDocument();
    // The list follows the replay; nothing on the static site is called live.
    expect(screen.getByRole('button', { name: copy.recording.follow_replay })).toHaveAttribute('aria-pressed');
    expect(screen.queryByRole('button', { name: 'Live' })).toBeNull();
  });

  it('continues the tour on the answer it caught, and opens the glass box from its link', async () => {
    const featured = useReplayStore.getState().featuredTraceId as string;
    const { container } = await renderStatic(`/app/traffic/${featured}#jev-request`);
    expect(await screen.findByRole('region', { name: copy.recording.guide.title })).toBeInTheDocument();
    const panel = await waitFor(() => {
      const el = container.querySelector('details#jev-request');
      if (!(el instanceof HTMLDetailsElement) || !el.open) throw new Error('the glass box is not open yet');
      return el;
    });
    await waitFor(() => expect(panel.querySelector('[data-glassbox="simulated"]')).not.toBeNull());
    // The Details section around it opened too.
    expect(panel.closest('details:not(#jev-request)')).toHaveAttribute('open');
  });

  it('says the Overview’s mix is a scripted showcase, next to its numbers (D117)', async () => {
    await renderStatic('/app/overview');
    const caveat = await screen.findByText(copy.recording.showcase_caveat);
    expect(caveat.closest('[data-showcase-caveat]')).not.toBeNull();
    // Its audits were picked by risk: the hallucination rate says why there is none, not just a count.
    const glance = document.querySelector('[aria-label="At a glance"]') as HTMLElement;
    const why = within(glance).getByText(fillTemplate(copy.recording.no_uniform_sample, { min_n: 30 }));
    // ...and draws no progress meter, which would read as a replay still collecting samples.
    const tile = why.closest('dd') as HTMLElement;
    expect(within(tile).queryByRole('meter')).not.toBeInTheDocument();
    expect(within(glance).queryByRole('meter')).not.toBeInTheDocument();
    // The mock checks' milliseconds are not presented as a real fast check's time.
    expect(within(glance).getByText(copy.overview.mock_latency_note, { exact: false })).toBeInTheDocument();
    // Its headline is a dash; the mock's own milliseconds sit in the note, marked as the mock's.
    expect(within(glance).getByText('No real time to show')).toBeInTheDocument();
    expect(within(glance).getByText(/^Mock: \d+ ms \/ \d+ ms$/)).toBeInTheDocument();
  });
});
