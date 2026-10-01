import { configure, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import type { SessionInfo, SetupStatus } from '@/types/api';
import { takeSetupToken } from './setupToken';

installMswServer();
afterEach(() => {
  stopLive();
  window.history.replaceState(null, '', '/');
});
configure({ asyncUtilTimeout: 4000 });

const setupStatus = (overrides: Partial<SetupStatus> = {}): SetupStatus => ({
  setup_required: true,
  ingest_auth: 'keys',
  demo: false,
  readonly: false,
  persistence: true,
  public_url: 'http://localhost:3000',
  recovery_command: 'docker compose exec -T proxy /proxy -new-setup-token',
  ...overrides,
});

const adminSession: SessionInfo = {
  actor: 'admin',
  scopes: ['ingest', 'read', 'content', 'admin'],
  expires_at: '2026-10-28T12:00:00Z',
  sliding: true,
};

/** The session endpoints of a proxy without a signed-in browser; records what was posted. */
function sessionApi(opts: { status?: SetupStatus; claim?: (body: Record<string, string>) => Response; keepsCookie?: boolean } = {}) {
  const posted: Record<string, string>[] = [];
  let signedIn: SessionInfo | null = null;
  server.use(
    http.get(`${API}/api/setup/status`, () => HttpResponse.json(opts.status ?? setupStatus())),
    http.get(`${API}/api/session`, () =>
      signedIn ? HttpResponse.json(signedIn) : HttpResponse.json({ error: { code: 'unauthorized', message: 'Sign in first.' } }, { status: 401 }),
    ),
    http.post(`${API}/api/session`, async ({ request }) => {
      const body = (await request.json()) as Record<string, string>;
      posted.push(body);
      if (opts.claim) return opts.claim(body);
      if (opts.keepsCookie !== false) signedIn = adminSession;
      return HttpResponse.json({ ...adminSession, ...(body.setup_token ? { created_admin_key: 'ta_created-admin-key' } : {}) });
    }),
    http.delete(`${API}/api/session`, () => {
      signedIn = null;
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return { posted };
}

describe('takeSetupToken', () => {
  it('reads the percent-encoded token once and removes it from the address bar', () => {
    window.history.replaceState(null, '', '/app/setup#token=a%2Bb%20c');
    expect(takeSetupToken()).toBe('a+b c');
    expect(window.location.hash).toBe('');
    expect(window.location.pathname).toBe('/app/setup');
    expect(takeSetupToken()).toBeNull();
  });
});

describe('Setup page', { timeout: 20_000 }, () => {
  it('claims the setup link, shows the admin key once and signs the browser in', async () => {
    const user = userEvent.setup();
    const { posted } = sessionApi();
    window.history.replaceState(null, '', '/app/setup#token=setup-token-123');
    renderPage('/app/setup');
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('Your admin key (shown once)')).toBeInTheDocument();
    expect(screen.getByText('ta_created-admin-key')).toBeInTheDocument();
    expect(posted).toEqual([{ setup_token: 'setup-token-123' }]);
    expect(await screen.findByText(/Admin actions are available/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Open Policies/ })).toHaveAttribute('href', '/app/policies');
  });

  it('keeps the created key on screen when the browser does not keep the session cookie', async () => {
    const user = userEvent.setup();
    sessionApi({ keepsCookie: false });
    window.history.replaceState(null, '', '/app/setup#token=cross-origin');
    renderPage('/app/setup');
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('This browser did not keep the session')).toBeInTheDocument();
    expect(screen.getByText('ta_created-admin-key')).toBeInTheDocument();
  });

  it('says what to do when the setup link was already used', async () => {
    const user = userEvent.setup();
    sessionApi({
      status: setupStatus({ setup_required: false }),
      claim: () => HttpResponse.json({ error: { code: 'setup_token_used', message: 'This setup token was already used.' } }, { status: 410 }),
    });
    window.history.replaceState(null, '', '/app/setup#token=used');
    renderPage('/app/setup');
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The setup link was refused: This setup token was already used.');
    expect(alert.textContent).toContain('make setup-url');
  });

  it('signs in with a key, and signs out', async () => {
    const user = userEvent.setup();
    const { posted } = sessionApi({ status: setupStatus({ setup_required: false }) });
    renderPage('/app/setup');
    const signIn = await screen.findByRole('button', { name: 'Sign in' });
    expect(signIn).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('Enter a key first')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Key'), 'ta_admin-key');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText(/Signed in as/)).toBeInTheDocument();
    expect(posted).toEqual([{ key: 'ta_admin-key' }]);
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument());
  });

  it('shows how to print a setup link while no admin exists', async () => {
    sessionApi();
    renderPage('/app/setup');
    expect(await screen.findByText('No admin yet')).toBeInTheDocument();
    expect(screen.getByText('docker compose exec -T proxy /proxy -new-setup-token')).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    sessionApi();
    window.history.replaceState(null, '', '/app/setup#token=t');
    const { container } = renderPage('/app/setup');
    await screen.findByRole('button', { name: 'Create the admin key' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
