/**
 * Every number on the landing page comes from contracts/claims/value-claims.v1.json (SPEC §8.1),
 * with its label: Measured, Estimate, Third-party, Vendor or Simulated. A `measured` claim reads
 * its value from contracts/claims/measured.json and says "not yet measured" until a benchmark
 * run publishes one; it is never filled with an estimate.
 */
import claimsDoc from '@contracts/claims/value-claims.v1.json';
import measuredDoc from '@contracts/claims/measured.json';
import { copy } from './content';

export type ClaimLabel = 'measured' | 'estimate' | 'third_party' | 'vendor' | 'simulated';

interface ClaimEntry {
  id: string;
  text: string;
  value: number | null;
  unit: string;
  display: string;
  empty_display?: string;
  label: string;
  basis: string;
  source_url: string | null;
  as_of: string | null;
  caveat: string | null;
  measured_ref?: string;
  surfaces: string[];
}

export interface ClaimView {
  id: string;
  text: string;
  /** What the page prints: the display string, or the empty display for an unmeasured claim. */
  display: string;
  /** False for a measured claim without a published value ("not yet measured"). */
  hasValue: boolean;
  value: number | null;
  unit: string;
  label: ClaimLabel;
  labelText: string;
  basis: string;
  sourceUrl: string | null;
  asOf: string | null;
  caveat: string | null;
}

const entries = new Map((claimsDoc.claims as ClaimEntry[]).map((c) => [c.id, c]));
const measuredValues = measuredDoc.values as Record<string, { value: number | null }>;

export const LABEL_TEXT: Record<ClaimLabel, string> = copy.value_labels;

function formatValue(value: number, unit: string): string {
  if (unit === 'fraction') return `${Math.round(value * 1000) / 10}%`;
  if (unit === 'usd_per_1k') return value >= 1 ? value.toFixed(2) : value.toFixed(3);
  return String(Math.round(value * 100) / 100);
}

export function claim(id: string): ClaimView {
  const c = entries.get(id);
  if (!c) throw new Error(`contracts/claims/value-claims.v1.json has no claim ${id}`);
  const label = c.label as ClaimLabel;
  let value = c.value;
  if (value === null && c.measured_ref) value = measuredValues[c.measured_ref]?.value ?? null;
  const hasValue = value !== null;
  const display = hasValue
    ? c.display.replace('{value}', formatValue(value as number, c.unit))
    : (c.empty_display ?? 'not yet measured');
  return {
    id: c.id,
    text: c.text,
    display,
    hasValue,
    value,
    unit: c.unit,
    label,
    labelText: LABEL_TEXT[label],
    basis: c.basis,
    sourceUrl: c.source_url,
    asOf: c.as_of,
    caveat: c.caveat,
  };
}

/** The claims written for the landing page, in contract order (the footer's source list). */
export function landingClaims(): ClaimView[] {
  return (claimsDoc.claims as ClaimEntry[]).filter((c) => c.surfaces.includes('landing')).map((c) => claim(c.id));
}

export const CLAIMS_AS_OF: string = claimsDoc.as_of;

/** The anchor of a claim's entry in the footer's source list. */
export function sourceAnchor(id: string): string {
  return `source-${id}`;
}

/**
 * Splits contract copy with `{claim:id}` placeholders into text and claims. A literal
 * "[estimate]"-style tag after a claim is dropped: the claim's own label chip says it.
 */
export function splitClaims(template: string): ({ text: string } | { claim: ClaimView })[] {
  const out: ({ text: string } | { claim: ClaimView })[] = [];
  const re = /\{claim:([a-z0-9_]+)\}(\s*\[[a-z_ -]+\])?/g;
  let last = 0;
  for (let m = re.exec(template); m; m = re.exec(template)) {
    if (m.index > last) out.push({ text: template.slice(last, m.index) });
    out.push({ claim: claim(m[1]) });
    last = m.index + m[0].length;
  }
  if (last < template.length) out.push({ text: template.slice(last) });
  return out;
}
