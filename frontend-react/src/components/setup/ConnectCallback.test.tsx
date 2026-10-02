import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
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

describe('/connect/callback under StrictMode (main.tsx renders the app in it)', { timeout: 30_000 }, () => {
  it('spends the one-use nonce once, although effects run twice', async () => {
    const control = createControlApi();
    server.use(...createFakeApi({ status: adminStatus() }, { controlPlane: false }).handlers, ...control.handlers);
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }));
    window.history.replaceState(null, '', '/connect/callback?code=abc123');
    const config = buildRuntimeConfig(undefined, {
      location: { href: 'http://localhost:3000/', origin: 'http://localhost:3000', protocol: 'http:' },
      env: { BASE_URL: '/' },
    });
    const dataSource = new LiveDataSource({ apiBase: API, crossOrigin: true, wsUrl: 'ws://proxy.test/ws' });
    const queryClient = createTestQueryClient();
    const services: AppServices = { config, dataSource, queryClient };
    const router = createRouter({
      routeTree,
      context: services,
      history: createMemoryHistory({ initialEntries: ['/connect/callback?code=abc123'] }),
      defaultPendingMs: 0,
    });
    render(
      <StrictMode>
        <ServicesContext.Provider value={services}>
          <QueryClientProvider client={queryClient}>
            <RouterProvider router={router} />
          </QueryClientProvider>
        </ServicesContext.Provider>
      </StrictMode>,
    );
    await waitFor(() => expect(router.state.location.href).toBe('/app/setup?step=try'));
    expect(control.callsTo('/api/connect/openrouter/complete', 'POST').map((c) => c.body)).toEqual([{ code: 'abc123', nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }]);
    expect(window.location.search).toBe('');
    expect(sessionStorage.getItem(CONNECT_PENDING_KEY)).toBeNull();
  });
});
