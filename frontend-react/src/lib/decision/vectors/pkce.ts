/**
 * The `pkce` vector kind's TS runner (contracts/vectors/pkce: verifier_from_bytes, challenge_s256,
 * auth_url; D103) over lib/openrouter/pkce.ts. Owned by WEB-LIVE: LV-4 replaces the skip.
 */
import type { VectorKind } from './kinds';

export const pkceKind: VectorKind = {
  skip: 'src/lib/openrouter/pkce.ts (LV-4, D103)',
  go: 'all (internal/credentials TestPKCEVectors, stdlib reference)',
};
