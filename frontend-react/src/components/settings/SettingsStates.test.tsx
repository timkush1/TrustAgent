/**
 * The page contract across the Settings sections WEB-ON owns (DESIGN.md §9): every section shows the
 * loading skeleton while its data is on the way, and works degraded when this proxy does not report
 * its status.
 */
import { configure, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import { createControlApi } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';

installMswServer();
warmRoutes('/app/settings/connections');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

describe('Settings sections: loading and degraded', { timeout: 30_000 }, () => {
  it.each(['connections', 'models', 'access', 'all'])('shows a loading state on %s while the data is slow', async (section) => {
    renderPage(`/app/settings/${section}`, { control: createControlApi({ latencyMs: 900 }) });
    expect((await screen.findAllByText(copy.states.loading)).length).toBeGreaterThan(0);
  });

  it('keeps Models and All configuration working without /api/status', async () => {
    renderPage('/app/settings/models', { status: null });
    expect(await screen.findByRole('radiogroup', { name: 'Backend' })).toBeInTheDocument();
    stopLive();
    document.body.innerHTML = '';
    renderPage('/app/settings/all', { status: null });
    expect(await screen.findByRole('rowheader', { name: 'TRUTHTABLE_JEV_BACKEND' })).toBeInTheDocument();
  });

  it('links every section from the Settings navigation, with no placeholder left', async () => {
    renderPage('/app/settings/connections');
    const nav = await screen.findByRole('navigation', { name: 'Settings sections' });
    expect(within(nav).getAllByRole('link').map((l) => l.textContent)).toEqual(['Connections', 'Models', 'Budgets', 'Access', 'Add-ons', 'All configuration']);
    await screen.findAllByRole('heading', { level: 2 });
    expect(screen.queryByText(/Arrives in P\d|Coming soon/)).not.toBeInTheDocument();
  });
});
