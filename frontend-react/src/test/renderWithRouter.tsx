/**
 * Renders a component inside a minimal TanStack router (memory history), for components that use
 * <Link> without needing the app's routes, services or data.
 */
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { act, render } from '@testing-library/react';
import type { ReactNode } from 'react';

export async function renderWithRouter(ui: ReactNode) {
  const rootRoute = createRootRoute({ component: () => <>{ui}</> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await act(() => router.load());
  const utils = render(<RouterProvider router={router} />);
  return { ...utils, router };
}
