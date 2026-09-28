import { createRouter } from '@tanstack/react-router';
import type { AppServices } from './app/services';
import { LoadingState } from './components/common/StateViews';
import { NotFound, RouteError } from './components/common/RouteStates';
import { routeTree } from './routeTree.gen';

export function createAppRouter(services: AppServices) {
  return createRouter({
    routeTree,
    context: services,
    basepath: services.config.basePath,
    defaultPreload: 'intent',
    // Page contract: pending UI after 200 ms, shown for at least 400 ms.
    defaultPendingMs: 200,
    defaultPendingMinMs: 400,
    defaultPendingComponent: () => <LoadingState />,
    defaultErrorComponent: RouteError,
    defaultNotFoundComponent: NotFound,
    scrollRestoration: true,
  });
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
  interface StaticDataRouteOption {
    /** Page title shown in the top bar. */
    title?: string;
  }
}
