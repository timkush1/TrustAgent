/**
 * The static build's chrome over the committed recording (SPEC §3.3 "Static build", §0 rows 4-5):
 * the six static nav items, the simulated banner, the hidden pages, the recorded Playground and
 * the Studio placeholder.
 */
import { beforeEach, describe, expect, it } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import { ServicesContext, type AppServices } from '@/app/services';
import { NotFound, RouteError } from '@/components/common/RouteStates';
import { buildRuntimeConfig } from '@/config/runtime';
import { createReplayDataSource } from '@/data/replay/replayDataSource';
import { useStreamStore } from '@/data/stream/streamStore';
import { routeTree } from '@/routeTree.gen';
import { axeViolations, formatViolations } from '@/test/axe';
import { ManualClock, recordingFetch } from '@/test/recording';
import { createTestQueryClient } from '@/test/renderApp';
import { hiddenStaticPage } from './staticPages';

const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';

async function renderStatic(path: string) {
  const config = buildRuntimeConfig(undefined, {
    location: { href: 'https://example.github.io/TrustAgent/', origin: 'https://example.github.io', protocol: 'https:' },
    env: { BASE_URL: '/TrustAgent/', VITE_APP_MODE: 'static' },
  });
  const dataSource = createReplayDataSource({ basePath: config.basePath, fetchImpl: recordingFetch('/TrustAgent/').fetch, clock: new ManualClock() });
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
  return { ...utils, router, dataSource };
}

beforeEach(() => {
  useStreamStore.getState().reset();
});

describe('hiddenStaticPage', () => {
  it('hides the pages that need a gateway, with or without the base path', () => {
    expect(hiddenStaticPage('/app/policies')?.name).toBe('Policies');
    expect(hiddenStaticPage('/TrustAgent/app/settings/models')?.name).toBe('Settings');
    expect(hiddenStaticPage('/app/knowledge')?.name).toBe('Knowledge');
    expect(hiddenStaticPage('/app/evaluators/groundedness')?.name).toBe('Evaluators');
    expect(hiddenStaticPage('/app/setup')?.name).toBe('Setup');
  });

  it('keeps the replay pages and the Studio', () => {
    for (const path of ['/app/overview', '/app/traffic', '/app/traffic/abc', '/app/playground', '/app/evaluators/studio', '/app/policiesx']) {
      expect(hiddenStaticPage(path), path).toBeNull();
    }
  });
});

describe('static build', () => {
  it('lists Overview (recorded), Traffic (replay), Trace, Playground, Studio and Run it yourself', async () => {
    await renderStatic('/app/traffic');
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    const labels = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('aria-label'))
      .filter(Boolean);
    expect(labels).toEqual(['Overview', 'Traffic', 'Trace', 'Playground', 'Studio', 'Run it yourself']);
    expect(within(nav).getByRole('link', { name: 'Run it yourself' })).toHaveAttribute('href', '/TrustAgent/#self-host');
    expect(within(nav).getByText('recorded')).toBeInTheDocument();
    expect(within(nav).getByText('replay')).toBeInTheDocument();
  });

  it('says the traffic is simulated and replayed, in one banner', async () => {
    const { container } = await renderStatic('/app/traffic');
    await waitFor(() => expect(container.querySelectorAll('[data-banner]')).toHaveLength(1));
    const banner = container.querySelector('[data-banner="replay"]') as HTMLElement;
    expect(within(banner).getByText('Replay of simulated traffic.')).toBeInTheDocument();
    expect(within(banner).getByText(MOCK_LABEL)).toBeInTheDocument();
    // Generated from the scenarios (scripts/build-demo-recording.ts), not recorded from a stack.
    expect(banner).toHaveTextContent(/generated from the zero-key demo’s scripted scenarios\. Run it yourself/);
    expect(banner).not.toHaveTextContent(/recorded/i);
    expect(within(banner).getByRole('link', { name: 'Run it yourself' })).toHaveAttribute('href', '/TrustAgent/#self-host');
    // No Health pill or demo badge for a gateway that is not there.
    expect(screen.queryByRole('button', { name: /System health/ })).toBeNull();
    expect(screen.getByRole('button', { name: /replay the recorded traffic again/i })).toBeEnabled();
  });

  it('lists the recorded requests on Traffic', async () => {
    await renderStatic('/app/traffic');
    const rows = () =>
      within(screen.getByRole('table', { name: 'Requests' }))
        .getAllByRole('row')
        .filter((r) => r.hasAttribute('data-trace-id'));
    // Half of the recording is on screen before the tape plays.
    await waitFor(() => expect(rows()).toHaveLength(9));
    const flagged = rows().find((r) => within(r).queryByText('Flagged')) as HTMLElement;
    expect(within(flagged).getByText('refund-policy.md')).toBeInTheDocument();
    expect(within(flagged).getByText('Hallucinated')).toBeInTheDocument();
    // Times follow the tape's clock ("just now", "1 min. ago"), not the days since the recording.
    const times = rows().map((r) => r.querySelector('time')?.textContent ?? '');
    expect(times.filter((t) => /Sep/.test(t))).toEqual([]);
    expect(times.some((t) => /now|ago/.test(t))).toBe(true);
  });

  it('shows why Policies is not here, with a real way to run it', async () => {
    await renderStatic('/app/policies');
    expect(await screen.findByRole('heading', { name: 'Policies is not part of the static demo' })).toBeInTheDocument();
    const main = screen.getByRole('main');
    expect(within(main).getByText('make demo')).toBeInTheDocument();
    expect(within(main).getByRole('link', { name: /Traffic/ })).toHaveAttribute('href', '/app/traffic');
    expect(formatViolations(await axeViolations(main))).toEqual([]);
  });

  it('opens the recorded runs from the Playground, and says the live check is coming soon', async () => {
    await renderStatic('/app/playground');
    const list = await screen.findByRole('list', { name: 'Example requests' });
    const cards = within(list).getAllByRole('listitem');
    expect(cards).toHaveLength(3);
    for (const card of cards) expect(within(card).getByRole('link', { name: /Open this request/ }).getAttribute('href')).toMatch(/^\/app\/traffic\//);
    expect(screen.getByText(/checked by the demo’s mock checks and mock judge \(simulated, not model output\)/)).toBeInTheDocument();
    expect(screen.queryByText(/checked by the demo stack/)).toBeNull();
    expect(screen.getByText('Coming soon')).toBeInTheDocument();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('shows the Overview of the recording with its Simulated badge', async () => {
    await renderStatic('/app/overview');
    expect(await screen.findByText(/18 requests on 28 Sept? 2026/)).toBeInTheDocument();
    expect(screen.queryByRole('radiogroup', { name: 'Time window' })).toBeNull();
  });

  it('opens the Studio placeholder, labelled coming soon', async () => {
    await renderStatic('/app/evaluators/studio');
    expect(await screen.findByText('Coming soon')).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2 })).toHaveTextContent('Write your own checks');
  });
});
