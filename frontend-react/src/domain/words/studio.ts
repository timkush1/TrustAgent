/**
 * Words, icons and tones for the P4 studio server values (Studio steps, sample filters, lint codes,
 * test-bench rows and dry-run states). Owned by WEB-ST. presentation.ts re-exports this module with
 * `export *`, so every export here starts with `studio` (P4-PLAN D86) and can never collide with
 * another plane's words. Contract text comes from contracts/ui/copy.v1.json (`copy.studio`).
 */
import { CircleCheck, CircleDashed, CircleX, Flame, MinusCircle, type LucideIcon } from 'lucide-react';
import type { CostRecord, DryRunResult, EvaluatorResultStatus, StudioSamplesRequest, ValueLabel } from '@/types/api';
import { copy, type Tone } from '../contracts';
import { formatCount, formatUsd } from '../format';
import { fillTemplate, humanizeId } from '../presentation';

const humanize = (id: string) => humanizeId(id.replace(/^det:/, ''));

/** The short title of a lint or hard-rule code (copy.studio.lint), or the code in words. */
export function studioLintTitle(code: string): string {
  const titles: Record<string, string> = copy.studio.lint;
  if (code === 'schema') return "Doesn't match the evaluator format";
  if (code === 'syntax') return 'Not valid JSON';
  return Object.hasOwn(titles, code) ? titles[code] : humanize(code);
}

export type StudioSampleFilter = StudioSamplesRequest['filter'];

const SAMPLE_FILTERS: Record<StudioSampleFilter, { label: string; description: string }> = {
  flagged: { label: 'Flagged answers', description: 'Answers the fast check flagged or blocked' },
  random: { label: 'Random answers', description: 'A random sample of recent answers' },
  disagreement: { label: 'Disagreements', description: 'Answers where the fast check and the deep audit disagreed' },
};

export const STUDIO_SAMPLE_FILTERS = Object.keys(SAMPLE_FILTERS) as StudioSampleFilter[];

export function studioSampleFilterWords(filter: StudioSampleFilter): { label: string; description: string } {
  return SAMPLE_FILTERS[filter];
}

const DETECTORS: Record<string, string> = {
  numbers_not_in_evidence: 'Number check',
  dates_not_in_evidence: 'Date check',
  urls_not_in_evidence: 'Link check',
  quotes_not_in_evidence: 'Quote check',
  citation_out_of_range: 'Citation check',
  secret_pattern: 'Secret check',
};

/**
 * A corroborator's name: an instant check ("det:numbers_not_in_evidence" → "Number check") or an
 * evaluator, by its library name when known.
 */
export function studioCorroboratorName(ref: string, names: ReadonlyMap<string, string> = new Map()): string {
  if (ref.startsWith('det:')) {
    const id = ref.slice(4);
    return DETECTORS[id] ?? humanize(id);
  }
  return names.get(ref) ?? humanize(ref);
}

export interface StudioRowView {
  label: string;
  icon: LucideIcon;
  tone: Tone;
}

/** "Fires" / "Doesn't fire" for a test row, or why it was not assessed. */
export function studioFireWords(fires: boolean | null, status: EvaluatorResultStatus): StudioRowView {
  if (fires === true) return { label: 'Fires', icon: Flame, tone: 'warning' };
  if (fires === false) return { label: "Doesn't fire", icon: CircleDashed, tone: 'neutral' };
  if (status === 'not_assessed') return { label: 'Not checked', icon: MinusCircle, tone: 'neutral' };
  return { label: "Couldn't check", icon: CircleX, tone: 'neutral' };
}

/** "Matches your label" / "Doesn't match", or nothing for an unlabelled sample. */
export function studioAgreementWords(agrees: boolean | null): StudioRowView | null {
  if (agrees === null) return null;
  return agrees
    ? { label: 'Matches your label', icon: CircleCheck, tone: 'success' }
    : { label: "Doesn't match", icon: CircleX, tone: 'danger' };
}

/** A label the viewer gives a sample. */
export function studioExpectedWords(expected: 'fire' | 'no_fire' | undefined | null): string {
  if (expected === 'fire') return 'Should fire';
  if (expected === 'no_fire') return "Shouldn't fire";
  return 'Not labelled';
}

const pct = (p: number) => `${(Math.floor(p * 1000 + 0.5) / 10).toFixed(1)}%`;

const WINDOWS: Record<string, string> = { '1h': 'hour', '24h': '24 h', '7d': '7 days', '30d': '30 days' };

/** copy.studio.dry_run_result filled from a result. */
export function studioDryRunResultText(r: Pick<DryRunResult, 'fired' | 'fire_rate' | 'fire_rate_ci95' | 'window' | 'cost_usd'>): string {
  return fillTemplate(copy.studio.dry_run_result, {
    fired: formatCount(r.fired),
    rate: pct(r.fire_rate),
    lo: pct(r.fire_rate_ci95[0]),
    hi: pct(r.fire_rate_ci95[1]),
    window: WINDOWS[r.window] ?? r.window,
    cost: formatUsd(r.cost_usd),
  });
}

/** copy.studio.dry_run_confirm filled from the 409's details (D82). */
export function studioDryRunConfirmText(d: { estimate_usd: number; samples: number; population: number; decision_budget_left_usd: number | null }): string {
  return fillTemplate(copy.studio.dry_run_confirm, {
    estimate: formatUsd(d.estimate_usd),
    samples: formatCount(d.samples),
    population: formatCount(d.population),
    budget_left: d.decision_budget_left_usd === null ? 'Unknown' : formatUsd(d.decision_budget_left_usd),
  });
}

/** copy.studio.dry_run_cancelled with what the checks that ran cost. */
export function studioDryRunCancelledText(spentUsd: number): string {
  return fillTemplate(copy.studio.dry_run_cancelled, { cost: `≈ ${formatUsd(spentUsd)}` });
}

/** copy.studio.save_and_watch for a route. */
export function studioSaveAndWatchLabel(route: string): string {
  return fillTemplate(copy.studio.save_and_watch, { route });
}

/** The value label of a cost the server reported with its source (D82: a mock's cost is Simulated); null when unknown. */
export function studioCostLabel(source: CostRecord['cost_source']): { label: ValueLabel; basis: string } | null {
  switch (source) {
    case 'reported':
      return { label: 'vendor', basis: 'As the provider reported it for this call.' };
    case 'computed':
      return { label: 'estimate', basis: "Tokens × the price table's price for this model." };
    case 'simulated':
      return { label: 'simulated', basis: 'A mock model answered; nothing was billed.' };
    case 'free_local':
      return { label: 'estimate', basis: 'A local model: no API fee (compute is not priced).' };
    default:
      return null;
  }
}
