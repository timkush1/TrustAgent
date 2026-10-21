/**
 * Renders the real route tree against a fake DataSource and memory history. Pass `queryClient` to
 * share one with a live stream started by the test (see renderLive.tsx).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { render } from '@testing-library/react';
import { ServicesContext, type AppServices } from '@/app/services';
import { NotFound, RouteError } from '@/components/common/RouteStates';
import { buildRuntimeConfig } from '@/config/runtime';
import type { DataSource } from '@/data/source';
import { routeTree } from '@/routeTree.gen';
import { fakeDataSource } from './fakeDataSource';

export function createTestQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

export function renderApp(
  path: string,
  dataSource: DataSource = fakeDataSource(),
  { queryClient = createTestQueryClient() }: { queryClient?: QueryClient } = {},
) {
  const config = buildRuntimeConfig(undefined, {
    location: { href: 'http://localhost:3000/', origin: 'http://localhost:3000', protocol: 'http:' },
    env: { BASE_URL: '/' },
  });
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
  return { ...utils, router, dataSource, queryClient };
}
