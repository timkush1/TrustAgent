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
  /**
   * A published measured figure's 95% interval with n per class (D212), in the words of the
   * README's measured block; null when the run gives none (or the claim is not measured).
   */
  interval?: string | null;
}

interface MeasuredInterval {
  lo?: unknown;
  hi?: unknown;
  method?: unknown;
  resamples?: unknown;
  n_pos?: unknown;
  n_neg?: unknown;
}

const entries = new Map((claimsDoc.claims as ClaimEntry[]).map((c) => [c.id, c]));
const measuredValues = measuredDoc.values as Record<string, { value: number | null }>;
const measuredRun = measuredDoc.status === 'measured' && measuredDoc.mock === false;
const measuredIntervals = (measuredDoc as { intervals?: Record<string, MeasuredInterval> }).intervals ?? {};
const measuredSources = (measuredDoc as { sources?: Record<string, { conditions?: unknown } | undefined> }).sources ?? {};
const measuredUnits = measuredDoc.values as Record<string, { unit?: string; system?: string | null } | undefined>;

/**
 * The measured.json sources key whose conditions fill a caveat's `{cost_conditions}` (D229): a
 * cost per 1,000 answers its own, the same-checks ratio the LLM judge's cost (which names the
 * judge). The same rule as scripts/check-claims.mjs costConditionsKey.
 */
export function costConditionsKey(ref: string | undefined): string | null {
  if (!ref) return null;
  if (ref === 'same_checks_cost_ratio') return 'llm_same_checks_usd_per_1k';
  const entry = measuredUnits[ref];
  return entry?.unit === 'usd_per_1k' && entry.system !== 'proxy' ? ref : null;
}

function sourceConditions(key: string | null): unknown {
  return measuredRun && key ? measuredSources[key]?.conditions : undefined;
}

const PLACEHOLDERS = ['{gate_conditions}', '{cost_conditions}'] as const;

/**
 * A caveat as the page shows it, filled from measured.json as scripts/check-claims.mjs fills it
 * for the README: `{gate_conditions}` from sources.gate_latency.conditions (the connection,
 * requests at a time, where the run ran, the snapshot and n; D205), `{cost_conditions}` from the
 * cost's sources.<key>.conditions (the dataset, n, the input tokens per answer and the judge;
 * D229). A value not measured yet has no conditions, so its caveat is dropped; a placeholder is
 * never shown raw.
 */
export function fillCaveat(
  caveat: string | null,
  hasValue: boolean,
  conditions: unknown = sourceConditions('gate_latency'),
  costConditions: unknown = undefined,
): string | null {
  if (!caveat || !PLACEHOLDERS.some((p) => caveat.includes(p))) return caveat;
  if (!hasValue) return null;
  const words = (value: unknown) => (typeof value === 'string' && value.trim() ? value.trim() : 'conditions not recorded');
  return caveat.replace('{gate_conditions}', words(conditions)).replace('{cost_conditions}', words(costConditions));
}
const INTERVAL_METHODS: Record<string, string> = { wilson: 'Wilson', bootstrap: 'bootstrap' };

export const LABEL_TEXT: Record<ClaimLabel, string> = copy.value_labels;

function formatValue(value: number, unit: string): string {
  if (unit === 'fraction') return `${Math.round(value * 1000) / 10}%`;
  if (unit === 'usd_per_1k') return value >= 1 ? value.toFixed(2) : value.toFixed(3);
  return String(Math.round(value * 100) / 100);
}

/** The interval words of scripts/check-claims.mjs (measuredNote), so the page and the README agree. */
export function intervalText(iv: MeasuredInterval | undefined, unit: string): string | null {
  if (!iv || typeof iv.lo !== 'number' || typeof iv.hi !== 'number') return null;
  const method = typeof iv.method === 'string' ? (INTERVAL_METHODS[iv.method] ?? iv.method) : '';
  const how = iv.method === 'bootstrap' && typeof iv.resamples === 'number' ? `${method}, ${iv.resamples} resamples` : method;
  let text = `95% interval ${formatValue(iv.lo, unit)} to ${formatValue(iv.hi, unit)}${how ? ` (${how})` : ''}`;
  if (Number.isInteger(iv.n_pos) && Number.isInteger(iv.n_neg)) text += `; ${String(iv.n_pos)} answers with a hallucination and ${String(iv.n_neg)} without`;
  return text;
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
    caveat: fillCaveat(c.caveat, hasValue, sourceConditions('gate_latency'), sourceConditions(costConditionsKey(c.measured_ref))),
    interval: hasValue && label === 'measured' && measuredRun && c.measured_ref ? intervalText(measuredIntervals[c.measured_ref], c.unit) : null,
  };
}

/**
 * A claim the page shows only while the contract has it (a figure whose source is being
 * re-checked may be withdrawn): null when the id is gone, never a number without a source.
 */
export function optionalClaim(id: string): ClaimView | null {
  return entries.has(id) ? claim(id) : null;
}

/** The claims written for the landing page, in contract order (the footer's source list). */
export function landingClaims(): ClaimView[] {
  return (claimsDoc.claims as ClaimEntry[]).filter((c) => c.surfaces.includes('landing')).map((c) => claim(c.id));
}

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
