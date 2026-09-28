/**
 * The one-time setup token of the proxy's "SETUP: open <public URL>/app/setup#token=…" line. It
 * travels in the fragment (never sent to a server) and is percent-encoded by the proxy.
 */

/** Reads `#token=…` once and removes it from the address bar (and so from the history). */
export function takeSetupToken(): string | null {
  if (typeof window === 'undefined') return null;
  const hash = window.location.hash.replace(/^#/, '');
  if (!hash) return null;
  const token = new URLSearchParams(hash).get('token');
  if (token) {
    const { pathname, search } = window.location;
    window.history.replaceState(window.history.state, '', `${pathname}${search}`);
  }
  return token && token.trim() !== '' ? token : null;
}
