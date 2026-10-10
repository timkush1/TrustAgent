/**
 * Where the browser talks to OpenRouter (D99, D100): the Step 0a constants from
 * domain/liveScenarios.ts, re-exported for the transport, and the URLs and timeouts built from them.
 * The decisions path is chosen at build time; there is never a runtime fallback to the other one,
 * which would double billable calls. Owned by WEB-LIVE (LV-2).
 */
import { DECISIONS_PATH } from '@/domain/liveScenarios';
import type { DecisionBackend } from '@/types/api';

export {
  ALLOWED_REQUEST_HEADERS,
  CHAT_FROM_BROWSER,
  DECISIONS_PATH,
  ERRORS_READABLE,
  LIVE_CHECKS,
  PKCE_FROM_BROWSER,
  STATIC_CONNECT_PRIMARY,
} from '@/domain/liveScenarios';

/** The only origin live mode contacts besides its own. */
export const OPENROUTER_ORIGIN = 'https://openrouter.ai';
/** The exact host the key may be sent to (D100). */
export const OPENROUTER_HOST = 'openrouter.ai';
export const OPENROUTER_API = `${OPENROUTER_ORIGIN}/api`;

/** POST: a Jev decision (the fast check). */
export const DECISIONS_URL = `${OPENROUTER_API}/${DECISIONS_PATH}`;
/** POST: a chat completion (Studio's Draft with AI). */
export const CHAT_URL = `${OPENROUTER_API}/v1/chat/completions`;
/** GET: the key's own label, limit and usage (free, never counted). */
export const KEY_URL = `${OPENROUTER_API}/v1/key`;
/** POST: the PKCE code exchange (free, no key, never counted). */
export const AUTH_KEYS_URL = `${OPENROUTER_API}/v1/auth/keys`;
/** The PKCE sign-in page (a navigation, not a fetch). */
export const AUTH_PAGE_URL = `${OPENROUTER_ORIGIN}/auth`;

/** decide()'s meta.backend for the build's decisions path. */
export const DECISIONS_BACKEND: DecisionBackend = DECISIONS_PATH === 'alpha/decisions' ? 'openrouter-decisions' : 'openrouter-systemone';

/** Per-request time limits (D100). */
export const TIMEOUTS_MS = {
  decisions: 10_000,
  chat: 60_000,
  key: 10_000,
  authKeys: 15_000,
} as const;

/** The attribution title OpenRouter shows for these calls (D100). */
export const APP_TITLE = 'TrustAgent demo';

/** OpenRouter pages a message links to (opened in a new tab, D102). */
export const OPENROUTER_PAGES = {
  credits: `${OPENROUTER_ORIGIN}/settings/credits`,
  keys: `${OPENROUTER_ORIGIN}/settings/keys`,
  privacy: `${OPENROUTER_ORIGIN}/settings/privacy`,
} as const;
