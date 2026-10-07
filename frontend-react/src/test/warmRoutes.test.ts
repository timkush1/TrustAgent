import { describe, expect, it } from 'vitest';
import { routesFor, warmRoutes } from './warmRoutes';

type Lazy = { options: { component?: { preload?: () => Promise<void> } } };
const loaded = (route: unknown) => (route as Lazy).options.component?.preload === undefined;

warmRoutes('/app/policies');

describe('warmRoutes', () => {
  it('finds the root, the app shell and the page on the way to a path', () => {
    expect(routesFor(['/app/policies']).map((r) => r.id)).toEqual(['__root__', '/app', '/app/policies']);
    expect(routesFor(['/app/policies', '/app/overview']).map((r) => r.id)).toEqual(['__root__', '/app', '/app/policies', '/app/overview']);
  });

  it('has loaded the split components of those routes before the first test, and only those', () => {
    const [, shell, policies] = routesFor(['/app/policies']);
    expect(loaded(shell)).toBe(true);
    expect(loaded(policies)).toBe(true);
    expect(loaded(routesFor(['/app/knowledge'])[2])).toBe(false);
  });
});
