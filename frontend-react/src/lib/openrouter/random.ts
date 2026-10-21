/**
 * The random part of PKCE (D103): a fresh code verifier from the browser's CSPRNG
 * (`crypto.getRandomValues`, available in insecure contexts too). Kept apart from pkce.ts so that
 * module stays pure and the vectors pin it. Owned by WEB-LIVE (LV-4).
 */
import { PKCE_VERIFIER_BYTES, verifierFromBytes } from './pkce';

/** Fills a byte array with cryptographically strong random values. */
export type RandomFill = (bytes: Uint8Array<ArrayBuffer>) => Uint8Array<ArrayBuffer>;

const cryptoFill: RandomFill = (bytes) => crypto.getRandomValues(bytes);

/** A new code verifier: 32 random bytes as 43 base64url characters. */
export function createVerifier(fill: RandomFill = cryptoFill): string {
  return verifierFromBytes(fill(new Uint8Array(PKCE_VERIFIER_BYTES)));
}
