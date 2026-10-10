/**
 * The /og social card (D115, RP-6) over the committed recording: the featured answer's real crop
 * (outcome pill, "Checked against", the answer's contradicted claim mark, the contradicting line),
 * the Simulated chip a capture of the simulated tape must carry, `data-og-ready`, and no serious
 * accessibility violation. Without VITE_LAUNCH_ROUTES the route answers with the not-found page.
 */
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { configure, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { ServicesContext, type AppServices } from '@/app/services';
import { NotFound, RouteError } from '@/components/common/RouteStates';
import { buildRuntimeConfig } from '@/config/runtime';
import { createReplayDataSource } from '@/data/replay/replayDataSource';
import { copy } from '@/domain/presentation';
import { routeTree } from '@/routeTree.gen';
import { axeViolations, formatViolations } from '@/test/axe';
import { committedDetail, committedManifest, ManualClock, recordingFetch } from '@/test/recording';
import { createTestQueryClient } from '@/test/renderApp';
import OgCard from './OgCard';

configure({ asyncUtilTimeout: 5000 });

async function services(manifest?: unknown): Promise<AppServices> {
  const config = buildRuntimeConfig(undefined, {
    location: { href: 'https://example.github.io/TrustAgent/', origin: 'https://example.github.io', protocol: 'https:' },
    env: { BASE_URL: '/TrustAgent/', VITE_APP_MODE: 'static' },
  });
  const tape = recordingFetch('/TrustAgent/', { manifest });
  const dataSource = createReplayDataSource({ basePath: config.basePath, fetchImpl: tape.fetch, clock: new ManualClock(), dataset: manifest ? 'recorded' : 'mock' });
  await dataSource.ready;
  return { config, dataSource, queryClient: createTestQueryClient() };
}

function withServices(s: AppServices, children: ReactNode) {
  return (
    <ServicesContext.Provider value={s}>
      <QueryClientProvider client={s.queryClient}>{children}</QueryClientProvider>
    </ServicesContext.Provider>
  );
}

const featured = committedManifest().traces.find((t) => t.action === 'flag' && t.audit.verdict === 'HALLUCINATED');

describe('OgCard', () => {
  it('shows the caught answer as a 1280×640 card, simulated and ready to capture', async () => {
    const { container } = render(withServices(await services(), <OgCard />));
    const card = container.querySelector('[data-og-card]') as HTMLElement;
    expect(card.style.width).toBe('1280px');
    expect(card.style.height).toBe('640px');
    await waitFor(() => expect(card).toHaveAttribute('data-og-ready'));

    expect(within(card).getByRole('heading', { level: 1 })).toHaveTextContent(copy.product.tagline);
    const crop = card.querySelector('[data-og-crop]') as HTMLElement;
    expect(crop.querySelector('[data-outcome="flagged"]')).toHaveTextContent('Flagged');
    expect(crop.querySelector('[data-og-checked]')).toHaveTextContent('Checked against: refund-policy.md');
    const detail = committedDetail(featured?.trace_id as string);
    expect(crop).toHaveTextContent(detail.body.user_request as string);
    const mark = crop.querySelector('[data-og-answer] mark[data-v="contradicted"]');
    expect(mark).toHaveTextContent('60 days');
    expect(crop.querySelector('[data-og-evidence]')).toHaveTextContent('Contradicted');
    expect(crop.querySelector('[data-og-evidence]')).toHaveTextContent('refund-policy.md: “Annual plans can be refunded within 30 days of purchase.”');
    // A capture of the simulated tape says what it is.
    expect(card).toHaveAttribute('data-recording-kind', 'mock');
    expect(card.querySelector('[data-og-simulated]')).toHaveTextContent(copy.recording.mock);
    expect(formatViolations(await axeViolations(card))).toEqual([]);
  });

  it('carries no Simulated chip on a recorded tape', async () => {
    const recorded = {
      ...committedManifest(),
      kind: 'recorded',
      label: 'Recorded 28 Sep 2026 · typesafe/jev-1.13 + claude-haiku-4.5 · scripted answers · replayed, not live',
      decision_model: { backend: 'openrouter-systemone', snapshot: 'typesafe/jev-1.13' },
      judge: { provider: 'anthropic', model: 'claude-haiku-4.5', fallback_active: false },
    };
    const { container } = render(withServices(await services(recorded), <OgCard />));
    const card = container.querySelector('[data-og-card]') as HTMLElement;
    await waitFor(() => expect(card).toHaveAttribute('data-og-ready'));
    expect(card).toHaveAttribute('data-recording-kind', 'recorded');
    expect(card.querySelector('[data-og-simulated]')).toBeNull();
    // The recording's own label says the answers were scripted: the card never calls them real.
    expect(card.querySelector('[data-og-recording-label]')).toHaveTextContent(recorded.label);
    expect(card).not.toHaveTextContent(/real answer/i);
  });

  it('is not a page of a build without VITE_LAUNCH_ROUTES', { timeout: 20_000 }, async () => {
    const s = await services();
    const router = createRouter({
      routeTree,
      context: s,
      history: createMemoryHistory({ initialEntries: ['/og'] }),
      defaultNotFoundComponent: NotFound,
      defaultErrorComponent: RouteError,
      defaultPendingMs: 0,
    });
    const { container } = render(withServices(s, <RouterProvider router={router} />));
    expect(await screen.findByText('There is no page at this address')).toBeInTheDocument();
    expect(container.querySelector('[data-og-card]')).toBeNull();
    expect(document.head.querySelector('meta[name="robots"]')).toBeNull();
  });
});
