/**
 * PKCE for Connect OpenRouter from the browser (RFC 7636, D103, D105): the code verifier from 32
 * random bytes, its S256 challenge and the openrouter.ai/auth URL. Pure: the random bytes come from
 * random.ts and the pending sign-in lives in keyStore.ts, so the golden vectors
 * (contracts/vectors/pkce, run by lib/decision/vectors/pkce.ts) pin every function here against the
 * Go reference. The challenge uses the decision core's synchronous SHA-256, so Connect works in an
 * insecure context too (no crypto.subtle). Owned by WEB-LIVE (LV-4).
 */
import { sha256 } from '@/lib/decision/sha256';
import { AUTH_PAGE_URL } from './endpoints';

/** How many random bytes a verifier encodes: 43 base64url characters, inside RFC 7636's 43-128. */
export const PKCE_VERIFIER_BYTES = 32;
/** A verifier this module makes: 43 characters of the base64url alphabet, no padding. */
export const VERIFIER_SHAPE = /^[A-Za-z0-9_-]{43}$/;
/** The label OpenRouter gives the key it creates (`key_label`, documented in its PKCE guide). */
export const KEY_LABEL = 'TrustAgent demo';

const B64URL = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

/** base64url without padding (RFC 4648 §5), as Go's base64.RawURLEncoding. */
export function base64Url(bytes: Uint8Array): string {
  let out = '';
  let i = 0;
  for (; i + 2 < bytes.length; i += 3) {
    const n = (bytes[i] << 16) | (bytes[i + 1] << 8) | bytes[i + 2];
    out += B64URL[(n >> 18) & 63] + B64URL[(n >> 12) & 63] + B64URL[(n >> 6) & 63] + B64URL[n & 63];
  }
  const rest = bytes.length - i;
  if (rest === 1) {
    const n = bytes[i] << 16;
    out += B64URL[(n >> 18) & 63] + B64URL[(n >> 12) & 63];
  } else if (rest === 2) {
    const n = (bytes[i] << 16) | (bytes[i + 1] << 8);
    out += B64URL[(n >> 18) & 63] + B64URL[(n >> 12) & 63] + B64URL[(n >> 6) & 63];
  }
  return out;
}

/** The code verifier of 32 random bytes; anything else is refused (the vectors' and Go's rule). */
export function verifierFromBytes(bytes: Uint8Array): string {
  if (bytes.length !== PKCE_VERIFIER_BYTES) throw new RangeError(`a PKCE verifier needs ${PKCE_VERIFIER_BYTES} random bytes, got ${bytes.length}`);
  return base64Url(bytes);
}

/** The S256 code challenge: base64url(SHA-256(ASCII(verifier))), no padding. */
export function challengeS256(verifier: string): string {
  if (verifier === '') throw new RangeError('a PKCE challenge needs a verifier');
  return base64Url(sha256(new TextEncoder().encode(verifier)));
}

/**
 * Percent-encodes every byte except the RFC 3986 unreserved characters (A-Z a-z 0-9 - . _ ~), in
 * upper-case hex: Go's uriComponent in the vector reference. encodeURIComponent also leaves
 * ! ' ( ) * as they are, so it is not used.
 */
export function uriComponent(s: string): string {
  let out = '';
  for (const byte of new TextEncoder().encode(s)) {
    const c = String.fromCharCode(byte);
    out += /[A-Za-z0-9._~-]/.test(c) ? c : `%${byte.toString(16).toUpperCase().padStart(2, '0')}`;
  }
  return out;
}

export interface AuthUrlInput {
  /** Where OpenRouter sends the browser back with `?code=` (this site's /connect/callback/). */
  callbackUrl: string;
  challenge: string;
  /** The created key's label; absent leaves the parameter out. */
  keyLabel?: string;
}

/**
 * The openrouter.ai/auth URL: callback_url, code_challenge, code_challenge_method=S256 and, when
 * given, key_label, in that order.
 */
export function authUrl(input: AuthUrlInput): string {
  if (input.callbackUrl === '' || input.challenge === '') throw new RangeError('an auth URL needs a callback URL and a challenge');
  const params: [string, string][] = [
    ['callback_url', input.callbackUrl],
    ['code_challenge', input.challenge],
    ['code_challenge_method', 'S256'],
  ];
  if (input.keyLabel !== undefined) params.push(['key_label', input.keyLabel]);
  return `${AUTH_PAGE_URL}?${params.map(([k, v]) => `${k}=${uriComponent(v)}`).join('&')}`;
}
