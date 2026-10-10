/**
 * The static /connect/callback/ page (D105, D106): the code leaves the address bar before the
 * exchange, the verifier is taken once, StrictMode sends one POST, a code without this tab's
 * verifier is never exchanged, and nothing from the query string is ever rendered.
 */
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import auth200 from '@contracts/fixtures/vendor/openrouter/auth_keys.200.json';
import { copy } from '@/domain/presentation';
import { resetCallbackForTests } from '@/lib/openrouter/connect';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { pageNav } from '@/lib/openrouter/navigate';
import { axeViolations, formatViolations } from '@/test/axe';
import StaticConnectCallback from './StaticConnectCallback';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const VERIFIER = 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk';
const CALLBACK = '/connect/callback/';

let events: string[];
let fetchMock: ReturnType<typeof vi.fn>;

function at(search: string) {
  window.history.replaceState(null, '', `${CALLBACK}${search}`);
}

beforeEach(() => {
  resetCallbackForTests();
  window.sessionStorage.clear();
  browserKeyStore().refresh();
  events = [];
  const replaceState = window.history.replaceState.bind(window.history);
  vi.spyOn(window.history, 'replaceState').mockImplementation((data, unused, url) => {
    events.push(`replaceState ${String(url)}`);
    replaceState(data, unused, url);
  });
  vi.spyOn(pageNav, 'replace').mockImplementation((url) => {
    events.push(`leave ${url}`);
  });
  fetchMock = vi.fn(async (url: string) => {
    events.push(`fetch ${url} with ${window.location.search || 'no query'}`);
    return new Response(JSON.stringify(auth200.body), { status: 200, headers: auth200.headers });
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  resetCallbackForTests();
  window.sessionStorage.clear();
  browserKeyStore().refresh();
  window.history.replaceState(null, '', '/');
});

describe('StaticConnectCallback', () => {
  it('takes the code out of the address bar before the exchange, then stores the key and leaves', async () => {
    browserKeyStore().savePending(VERIFIER, 'app/playground/?scenario=refund-contradiction-flag#live');
    at('?code=e2e-code-1');
    events.length = 0;
    render(<StaticConnectCallback />);
    await waitFor(() => expect(events.some((e) => e.startsWith('leave'))).toBe(true));
    expect(events[0]).toBe(`replaceState ${CALLBACK}`);
    expect(events[1]).toBe('fetch https://openrouter.ai/api/v1/auth/keys with no query');
    expect(events.at(-1)).toBe('leave /app/playground/?scenario=refund-contradiction-flag#live');
    expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual({ code: 'e2e-code-1', code_verifier: VERIFIER, code_challenge_method: 'S256' });
    expect(fetchMock.mock.calls[0][1].headers).not.toHaveProperty('authorization');
    expect(browserKeyStore().currentKey()).toBe(auth200.body.key);
    // The verifier is gone: it never serves a second code.
    expect(browserKeyStore().hasPending()).toBe(false);
    expect(window.location.search).toBe('');
  });

  it('sends one POST under StrictMode', async () => {
    browserKeyStore().savePending(VERIFIER, null);
    at('?code=e2e-code-1');
    render(
      <StrictMode>
        <StaticConnectCallback />
      </StrictMode>,
    );
    await waitFor(() => expect(pageNav.replace).toHaveBeenCalledTimes(1));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(pageNav.replace).toHaveBeenCalledWith('/app/playground/#live');
  });

  it('never exchanges a code without this tab’s verifier (login CSRF)', async () => {
    at('?code=attacker-code');
    render(<StaticConnectCallback />);
    expect(await screen.findByText(copy.playground_live.callback.missing_verifier)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(browserKeyStore().currentKey()).toBeNull();
    expect(window.location.search).toBe('');
  });

  it('never renders the error OpenRouter (or anyone) put in the query', async () => {
    browserKeyStore().savePending(VERIFIER, null);
    at('?error=Paste+your+key+at+evil.example&error_description=Visit+evil.example');
    const { container } = render(<StaticConnectCallback />);
    expect(await screen.findByText(copy.playground_live.callback.refused)).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/evil/);
    expect(document.body.innerHTML).not.toMatch(/evil/);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(browserKeyStore().hasPending()).toBe(false);
    // Paste a key instead opens the Playground's consent dialog on Paste.
    expect(screen.getByRole('link', { name: copy.playground_live.paste_key }).getAttribute('href')).toBe('/app/playground/?paste=1#live');
  });

  it('says expired for a sign-in older than 10 minutes', async () => {
    // A sign-in started 10 minutes and a second ago.
    window.sessionStorage.setItem('ta.pkce.verifier', JSON.stringify({ verifier: VERIFIER, created_at: Date.now() - 10 * 60 * 1000 - 1000 }));
    at('?code=late');
    render(<StaticConnectCallback />);
    expect(await screen.findByText(copy.playground_live.callback.expired)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('says already connected on a reload after success', async () => {
    browserKeyStore().set(KEY);
    at('');
    render(<StaticConnectCallback />);
    expect(await screen.findByText(copy.playground_live.callback.already_connected)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: copy.playground_live.paste_key })).toBeNull();
    expect(screen.getByRole('link', { name: 'Back to the Playground' }).getAttribute('href')).toBe('/app/playground/#live');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('says network when openrouter.ai cannot be reached', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    browserKeyStore().savePending(VERIFIER, null);
    at('?code=c');
    render(<StaticConnectCallback />);
    expect(await screen.findByText(copy.playground_live.callback.network)).toBeInTheDocument();
    expect(pageNav.replace).not.toHaveBeenCalled();
  });

  it('exchanges nothing inside a frame and offers only a new tab (D106)', async () => {
    const top = Object.getOwnPropertyDescriptor(window, 'top');
    Object.defineProperty(window, 'top', { configurable: true, get: () => ({}) });
    try {
      browserKeyStore().savePending(VERIFIER, null);
      at('?code=c');
      render(<StaticConnectCallback />);
      expect(await screen.findByText(copy.playground_live.callback.framed)).toBeInTheDocument();
      const link = screen.getByRole('link', { name: copy.playground_live.open_in_tab });
      expect(link.getAttribute('target')).toBe('_blank');
      expect(link.getAttribute('rel')).toBe('noopener noreferrer');
      expect(screen.queryByRole('link', { name: copy.playground_live.paste_key })).toBeNull();
      expect(fetchMock).not.toHaveBeenCalled();
    } finally {
      if (top) Object.defineProperty(window, 'top', top);
    }
  });

  it('has no axe violations in a failure state', async () => {
    at('?code=x');
    render(<StaticConnectCallback />);
    await screen.findByText(copy.playground_live.callback.missing_verifier);
    expect(formatViolations(await axeViolations())).toEqual([]);
  });
});
