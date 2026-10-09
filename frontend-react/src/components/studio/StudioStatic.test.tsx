/**
 * The static build has no gateway to draft, test or save with: Studio stays "Coming soon" until P5
 * (D72), says how to run it yourself, and calls nothing.
 */
import { configure, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { fakeDataSource } from '@/test/fakeDataSource';
import { renderApp } from '@/test/renderApp';
import { warmRoutes } from '@/test/warmRoutes';

warmRoutes('/app/evaluators/studio', '/app/evaluators/studio/refusal/1');

configure({ asyncUtilTimeout: 5000 });

vi.mock('@/components/replay/useReplay', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/replay/useReplay')>()),
  useIsStatic: () => true,
}));

describe('Studio in the static build', () => {
  it.each(['/app/evaluators/studio', '/app/evaluators/studio/refusal/1'])('%s is coming soon, with the way to run it', async (path) => {
    const ds = fakeDataSource();
    const draft = vi.spyOn(ds, 'draftEvaluator');
    renderApp(path, ds);
    expect(await screen.findByText('Coming soon')).toBeInTheDocument();
    // Studio exists (on your own stack): the page never says it is not built.
    expect(screen.getByText(/^Studio runs on your own TrustAgent stack\. The online demo cannot/)).toBeInTheDocument();
    expect(screen.queryByText(/not built yet/)).toBeNull();
    expect(screen.getByText(/Run TrustAgent yourself to use Studio/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Draft with AI' })).not.toBeInTheDocument();
    expect(draft).not.toHaveBeenCalled();
  });
});
