import { QueryClientProvider } from '@tanstack/react-query';
import { createBrowserHistory, createMemoryHistory, createRouter, RouterProvider, type RouterHistory } from '@tanstack/react-router';
import { configure, render, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import { ServicesContext, type AppServices } from '@/app/services';
import { buildRuntimeConfig } from '@/config/runtime';
import { LiveDataSource } from '@/data/live/liveDataSource';
import { routeTree } from '@/routeTree.gen';
import { createFakeApi } from '@/test/apiHandlers';
import { createControlApi } from '@/test/control/handlers';
import { adminStatus } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { createTestQueryClient } from '@/test/renderApp';
import { CONNECT_PENDING_KEY } from './connectFlow';

installMswServer();
afterEach(() => {
  sessionStorage.clear();
  window.history.replaceState(null, '', '/');
});
configure({ asyncUtilTimeout: 4000 });

const NONCE = 'q2Xw9fLr0aZk7TcVb3NmYg';

/** Renders the app's route tree at /connect/callback?code=abc123 with a pending nonce, as main.tsx does (StrictMode). */
function renderCallback(history: () => RouterHistory) {
  const control = createControlApi();
  server.use(...createFakeApi({ status: adminStatus() }, { controlPlane: false }).handlers, ...control.handlers);
  sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: NONCE }));
  window.history.replaceState(null, '', '/connect/callback?code=abc123');
  const config = buildRuntimeConfig(undefined, {
    location: { href: 'http://localhost:3000/', origin: 'http://localhost:3000', protocol: 'http:' },
    env: { BASE_URL: '/' },
  });
  const dataSource = new LiveDataSource({ apiBase: API, crossOrigin: true, wsUrl: 'ws://proxy.test/ws' });
  const queryClient = createTestQueryClient();
  const services: AppServices = { config, dataSource, queryClient };
  const router = createRouter({ routeTree, context: services, history: history(), defaultPendingMs: 0 });
  render(
    <StrictMode>
      <ServicesContext.Provider value={services}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ServicesContext.Provider>
    </StrictMode>,
  );
  return { control, router };
}

describe('/connect/callback under StrictMode (main.tsx renders the app in it)', { timeout: 30_000 }, () => {
  it('spends the one-use nonce once, although effects run twice', async () => {
    const { control, router } = renderCallback(() => createMemoryHistory({ initialEntries: ['/connect/callback?code=abc123'] }));
    await waitFor(() => expect(router.state.location.href).toBe('/app/setup?step=try'));
    expect(control.callsTo('/api/connect/openrouter/complete', 'POST').map((c) => c.body)).toEqual([{ code: 'abc123', nonce: NONCE }]);
    expect(window.location.search).toBe('');
    expect(sessionStorage.getItem(CONNECT_PENDING_KEY)).toBeNull();
  });

  it('completes with the code it was opened with, although the browser history drops it from the address bar first', async () => {
    // The app's own history: it follows history.replaceState, so the page re-renders without ?code=
    // before the complete call goes out.
    let history: RouterHistory | null = null;
    try {
      const { control, router } = renderCallback(() => (history = createBrowserHistory()));
      await waitFor(() => expect(router.state.location.href).toBe('/app/setup?step=try'));
      expect(control.callsTo('/api/connect/openrouter/complete', 'POST').map((c) => c.body)).toEqual([{ code: 'abc123', nonce: NONCE }]);
      expect(sessionStorage.getItem(CONNECT_PENDING_KEY)).toBeNull();
    } finally {
      (history as RouterHistory | null)?.destroy();
    }
  });
});
