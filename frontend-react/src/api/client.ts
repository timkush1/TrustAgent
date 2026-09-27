// Central API / WebSocket client helpers.
//
// Two DISTINCT credentials exist in this system — do not confuse them:
//   • the DASHBOARD key (handled here) authenticates the browser to the Go
//     proxy (TRUTHTABLE_API_KEYS). It is a low-sensitivity, localhost-scoped
//     access credential and is the ONLY key the browser ever holds.
//   • the PROVIDER key (Anthropic / OpenAI) lives server-side ONLY. It is never
//     sent to, returned to, or stored in the browser. The Settings tab writes
//     it once (write-only) and only ever reads back a masked "set/unset" hint.

export function getApiBase(): string {
  return import.meta.env.VITE_API_URL || 'http://localhost:8080';
}

export function getWsBase(): string {
  return import.meta.env.VITE_WS_URL || 'ws://localhost:8080/ws';
}

/** The dashboard→proxy access key, if the deployment enabled proxy auth. */
export function getDashboardKey(): string | undefined {
  const key = import.meta.env.VITE_TRUTHTABLE_API_KEY;
  return key && String(key).trim() ? String(key).trim() : undefined;
}

/**
 * WebSocket URL with the dashboard key appended as a query param. Browsers
 * cannot set WebSocket handshake headers, so the proxy also accepts ?api_key=
 * (see backend-go cmd/proxy/main.go). Without a key configured, returns the
 * bare URL (auth disabled — local dev).
 */
export function getWsUrl(): string {
  const base = getWsBase();
  const key = getDashboardKey();
  if (!key) return base;
  const sep = base.includes('?') ? '&' : '?';
  return `${base}${sep}api_key=${encodeURIComponent(key)}`;
}

/** fetch() wrapper that attaches the dashboard key (when set) as a Bearer token. */
export function fetchWithAuth(input: string, init: RequestInit = {}): Promise<Response> {
  const key = getDashboardKey();
  const headers = new Headers(init.headers);
  if (key && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${key}`);
  }
  return fetch(input, { ...init, headers });
}
