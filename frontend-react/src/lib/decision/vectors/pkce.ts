/**
 * The `pkce` vector kind's TS runner (contracts/vectors/pkce; D103) over lib/openrouter/pkce.ts,
 * mirroring backend-go/internal/decision/vectors/pkce.go. The input's `op` names the operation:
 *
 *   challenge:           {op, verifier}                            -> {challenge}
 *   verifier_from_bytes: {op, bytes_hex}                           -> {verifier}
 *   auth_url:            {op, callback_url, challenge, key_label?} -> {url}
 *
 * Decoding is as strict as Go's (DisallowUnknownFields; an absent or null member is its zero value,
 * and a null key_label is absent), so a malformed vector fails instead of pinning a wrong value.
 * Owned by WEB-LIVE (LV-4).
 */
import { authUrl, challengeS256, PKCE_VERIFIER_BYTES, verifierFromBytes } from '@/lib/openrouter/pkce';
import type { VectorKind } from './kinds';

type Obj = Record<string, unknown>;

const FIELDS = ['op', 'verifier', 'bytes_hex', 'callback_url', 'challenge', 'key_label'] as const;

function decode(input: unknown): Obj {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) throw new Error('pkce: vector input must be an object');
  for (const k of Object.keys(input)) if (!(FIELDS as readonly string[]).includes(k)) throw new Error(`pkce: unknown input member ${JSON.stringify(k)}`);
  return input as Obj;
}

/** Member k as a string; absent or null is "" (Go's zero value). */
function str(x: Obj, k: string): string {
  const v = x[k];
  if (v === undefined || v === null) return '';
  if (typeof v !== 'string') throw new Error(`pkce: ${k} must be a string`);
  return v;
}

/** Hex to bytes, refusing what Go's hex.DecodeString refuses (odd length, a non-hex digit). */
function hexBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(hex)) throw new Error(`pkce verifier_from_bytes: bytes_hex must be ${PKCE_VERIFIER_BYTES} bytes of hex`);
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = parseInt(hex.slice(2 * i, 2 * i + 2), 16);
  return out;
}

function runPKCE(input: unknown): unknown {
  const x = decode(input);
  const op = str(x, 'op');
  switch (op) {
    case 'challenge': {
      const verifier = str(x, 'verifier');
      if (verifier === '') throw new Error('pkce challenge: verifier is required');
      return { challenge: challengeS256(verifier) };
    }
    case 'verifier_from_bytes': {
      const bytes = hexBytes(str(x, 'bytes_hex'));
      if (bytes.length !== PKCE_VERIFIER_BYTES) throw new Error(`pkce verifier_from_bytes: bytes_hex must be ${PKCE_VERIFIER_BYTES} bytes of hex`);
      return { verifier: verifierFromBytes(bytes) };
    }
    case 'auth_url': {
      const callbackUrl = str(x, 'callback_url');
      const challenge = str(x, 'challenge');
      if (callbackUrl === '' || challenge === '') throw new Error('pkce auth_url: callback_url and challenge are required');
      const label = x.key_label;
      if (label !== undefined && label !== null && typeof label !== 'string') throw new Error('pkce: key_label must be a string');
      return { url: authUrl({ callbackUrl, challenge, keyLabel: typeof label === 'string' ? label : undefined }) };
    }
  }
  throw new Error(`pkce vector op ${JSON.stringify(op)} is not one of challenge, verifier_from_bytes, auth_url`);
}

export const pkceKind: VectorKind = {
  run: runPKCE,
  go: 'all (internal/credentials TestPKCEVectors, stdlib reference)',
};
