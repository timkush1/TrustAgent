/**
 * Settings → All configuration when neither the proxy's settings nor the built-in registry can be
 * read (the registry ships as its own chunk, which a flaky network can fail to load): the page
 * shows the error with Retry instead of loading forever (DESIGN.md §9 page contract).
 */
import { configure, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import { copy } from '@/domain/presentation';

vi.mock('@contracts/config/keys.v1.json', () => {
  throw new Error('Failed to fetch dynamically imported module');
});

installMswServer();
warmRoutes('/app/settings/all');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

describe('Settings → All configuration without settings or registry', { timeout: 20_000 }, () => {
  it('shows the error with Retry instead of loading forever', async () => {
    renderPage('/app/settings/all', { control: createControlApi({ settings: errorReply(503, 'unavailable', 'The proxy is starting.') }) });
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('The proxy is starting.');
    expect(screen.getByRole('button', { name: copy.states.retry })).toBeInTheDocument();
    expect(screen.queryByText('Showing the built-in defaults')).toBeNull();
  });
});
