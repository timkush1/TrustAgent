/**
 * Token estimates (SPEC §2.9 Tokens): est(x) = ceil(n_narrow/cpt + n_wide) over the code points of
 * JCS(x), where a code point >= U+3000 is wide. Port of backend-go/internal/decision/tokens.go;
 * pinned by contracts/vectors/tokens. The runtime cpt update is Go only (SPEC §2.9).
 */
import { canonicalize } from './jcs';

/** Fixed per-request token overhead (OVERHEAD). */
export const OVERHEAD = 275;
/** Default characters-per-token ratio (TRUTHTABLE_JEV_CHARS_PER_TOKEN). */
export const DEFAULT_CPT = 4;
/** Bounds of the runtime cpt estimate. */
export const MIN_CPT = 2.5;
export const MAX_CPT = 6;
/** First code point counted as a whole token (CJK and above). */
const WIDE_FROM = 0x3000;

/** The breakdown of an estimate: narrow and wide code points of the canonical JSON, and est. */
export interface TokenCount {
  narrow: number;
  wide: number;
  est: number;
}

/** Throws unless cpt is a positive finite number. */
export function checkCPT(cpt: number): void {
  if (!(cpt > 0) || !Number.isFinite(cpt)) throw new RangeError(`decision: cpt must be a positive finite number, got ${cpt}`);
}

/** est() with the narrow and wide code point counts. A string is counted with its quotes. */
export function countTokens(value: unknown, cpt: number): TokenCount {
  checkCPT(cpt);
  return countCanonical(canonicalize(value), cpt);
}

/** est(value) at cpt. */
export function est(value: unknown, cpt: number): number {
  return countTokens(value, cpt).est;
}

function countCanonical(text: string, cpt: number): TokenCount {
  let narrow = 0;
  let wide = 0;
  for (const ch of text) {
    if ((ch.codePointAt(0) ?? 0) >= WIDE_FROM) wide++;
    else narrow++;
  }
  return { narrow, wide, est: Math.ceil(narrow / cpt + wide) };
}
