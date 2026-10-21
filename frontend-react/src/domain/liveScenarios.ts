/**
 * "Try Jev live" (D99, D100): which scenarios the static site offers live, where the link goes, and
 * what the browser may do with openrouter.ai. Frozen: the constants are the results of the Step 0a
 * checks (free CORS preflights and one headless Chromium run with a fake key, 2026-10-09; SPEC
 * Appendix D "OpenRouter browser CORS"). Live scope L: every endpoint the browser needs allows CORS
 * from the Pages origin and returns readable error bodies.
 *
 * The landing imports only this module from the live feature (never lib/openrouter, lib/gate or
 * components/replay/live; D99, D112).
 */

/** The scenarios a visitor can run live with their own key, in the order offered. */
export const LIVE_SCENARIOS = ['grounded-pass', 'refund-contradiction-flag', 'pii-leak-flag'] as const;
export type LiveScenarioId = (typeof LIVE_SCENARIOS)[number];

export function isLiveScenario(id: unknown): id is LiveScenarioId {
  return typeof id === 'string' && (LIVE_SCENARIOS as readonly string[]).includes(id);
}

/** The live Playground for a scenario, relative to the site's base (`base` ends in "/"). */
export function liveHref(base: string, id: LiveScenarioId): string {
  return `${base.endsWith('/') ? base : `${base}/`}app/playground/?scenario=${id}#live`;
}

/** Live checks from the browser work (a decisions path allows CORS). False: scope R. */
export const LIVE_CHECKS = true;
/** Chat completions from the browser work (Studio's Draft with AI, D108). */
export const CHAT_FROM_BROWSER = true;
/** The PKCE key exchange (POST /api/v1/auth/keys) works from the browser. */
export const PKCE_FROM_BROWSER = true;
/** OpenRouter's error bodies are readable from the browser (ACAO on errors). */
export const ERRORS_READABLE = true;
/**
 * Which consent action is primary: `connect` (PKCE) or `paste`. From the CORS result alone, never
 * from D88; the P6 Pages PKCE check may flip it to `paste` (§23 CO-14). Paste is always offered.
 */
export const STATIC_CONNECT_PRIMARY: 'connect' | 'paste' = 'connect';
/** The decisions endpoint under https://openrouter.ai/api/, chosen at build time (never a runtime fallback). */
export const DECISIONS_PATH: 'alpha/decisions' | 'v1/systemone' = 'alpha/decisions';
/** The exact request header names the browser may send to openrouter.ai (lower case; D100, D138). */
export const ALLOWED_REQUEST_HEADERS = ['authorization', 'content-type', 'accept', 'http-referer', 'x-openrouter-title'] as const;
