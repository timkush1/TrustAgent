/**
 * Words, icons and tones for the values only Studio shows (SPEC §7.3): sample filters, instant-check
 * names, test-bench rows, the dry-run texts filled from contracts/ui copy.studio, and the label of a
 * cost by its source (D82). They load with Studio's route, not with the shared presentation chunk
 * the landing page also loads (see domain/words/studio.ts).
 */
import { CircleCheck, CircleDashed, CircleX, Flame, MinusCircle, type LucideIcon } from 'lucide-react';
import type { Tone } from '@/domain/contracts';
import { formatCount, formatUsd } from '@/domain/format';
import { copy, fillTemplate, humanizeId } from '@/domain/presentation';
import type { CostRecord, DryRunResult, EvaluatorResultStatus, StudioSamplesRequest, ValueLabel } from '@/types/api';

const humanize = (id: string) => humanizeId(id.replace(/^det:/, ''));

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


const WINDOWS: Record<string, string> = { '1h': 'hour', '24h': '24 h', '7d': '7 days', '30d': '30 days' };

// ---------------------------------------------------------------------------- readable question

/** What each part of the request a question can name is, in the Simple tab's words. */
const STATE_WORDS: Record<string, string> = {
  assistant_response: 'your model’s answer',
  user_request: 'the user’s message',
  evidence: 'your documents',
  system_instructions: 'your system prompt',
  rules: 'the check’s settings',
};

/** A question split into its words and the names it points at, each name with its readable label. */
export type StudioQuestionPart = { kind: 'text'; text: string } | { kind: 'name'; raw: string; label: string };

/**
 * A question as the Simple tab shows it: every name in backticks becomes its readable label
 * ("`assistant_response`" → "your model’s answer", "`rules.competitors`" → the setting's own label,
 * such as "Competitor names"). The raw names stay in the text being edited and in Advanced and JSON.
 */
export function studioQuestionParts(text: string, params?: Readonly<Record<string, { label?: string | null } | undefined>> | null): StudioQuestionPart[] {
  const parts: StudioQuestionPart[] = [];
  const pattern = /`([^`\n]+)`/g;
  let last = 0;
  for (let m = pattern.exec(text); m !== null; m = pattern.exec(text)) {
    if (m.index > last) parts.push({ kind: 'text', text: text.slice(last, m.index) });
    const raw = m[1].trim();
    const rule = /^rules\.(.+)$/.exec(raw);
    const label = rule ? (params?.[rule[1]]?.label || humanizeId(rule[1])) : (STATE_WORDS[raw] ?? humanizeId(raw));
    parts.push({ kind: 'name', raw, label });
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push({ kind: 'text', text: text.slice(last) });
  return parts;
}

/** Answers a dry run would have fired on, with the same text shown once: the first (highest risk) and how many. */
export interface StudioExampleGroup<E> {
  example: E;
  /** How many of the examples have this text (1 for one whose text is unknown). */
  count: number;
  text: string | null;
}

/**
 * Groups a dry run's examples by their answer text, in the order they came (the server sends the
 * highest risk first), so six identical answers read as one row "×6". An example whose text is not
 * known (not loaded yet, hidden or expired) stays a row of its own.
 */
export function studioGroupExamples<E>(examples: readonly E[], texts: readonly (string | null)[]): StudioExampleGroup<E>[] {
  const groups: StudioExampleGroup<E>[] = [];
  const byText = new Map<string, StudioExampleGroup<E>>();
  examples.forEach((example, i) => {
    const text = texts[i] || null;
    const same = text === null ? undefined : byText.get(text);
    if (same) {
      same.count += 1;
      return;
    }
    const group = { example, count: 1, text };
    groups.push(group);
    if (text !== null) byText.set(text, group);
  });
  return groups;
}

/** Whether a dry run tested only part of its window, so its count for the window is extrapolated. */
export function studioDryRunExtrapolated(r: Pick<DryRunResult, 'sampled' | 'population'>): boolean {
  return r.sampled < r.population;
}

/** A rate in plain words: "40%" from 10% up, "5.9%" below (never "0%" for a small one). */
const plainPct = (p: number) => {
  const v = p * 100;
  if (v >= 10) return `${Math.round(v)}%`;
  return `${(Math.round(v * 10) / 10).toLocaleString('en-US', { maximumFractionDigits: 1 })}%`;
};

/**
 * copy.studio.dry_run_result filled from a result. The sentence is about the whole window ("in the
 * last 7 days"), so when only a sample of it was tested the count is the extrapolated one, written
 * "≈ 108" (an estimate); when every answer was tested it is the count itself. The 95% interval is
 * said in plain words ("likely between 3.3% and 10%"); the cost is not in it, as it has its own
 * labelled row.
 */
export function studioDryRunResultText(r: Pick<DryRunResult, 'fired' | 'extrapolated_fires' | 'sampled' | 'population' | 'fire_rate' | 'fire_rate_ci95' | 'window'>): string {
  return fillTemplate(copy.studio.dry_run_result, {
    fired: studioDryRunExtrapolated(r) ? `≈ ${formatCount(r.extrapolated_fires)}` : formatCount(r.fired),
    rate: plainPct(r.fire_rate),
    lo: plainPct(r.fire_rate_ci95[0]),
    hi: plainPct(r.fire_rate_ci95[1]),
    window: studioWindowWords(r.window),
  });
}

/**
 * copy.studio.dry_run_confirm filled from the 409's details (D82). The gateway sends no budget left
 * (null) when no daily fast-check budget is set, so the budget sentence then says that instead.
 */
export function studioDryRunConfirmText(d: { estimate_usd: number; samples: number; population: number; decision_budget_left_usd: number | null }): string {
  const values = { estimate: formatUsd(d.estimate_usd), samples: formatCount(d.samples), population: formatCount(d.population) };
  if (d.decision_budget_left_usd !== null) return fillTemplate(copy.studio.dry_run_confirm, { ...values, budget_left: formatUsd(d.decision_budget_left_usd) });
  const sentences = copy.studio.dry_run_confirm.split(/(?<=\.)\s+/).filter((sentence) => !sentence.includes('{budget_left}'));
  return `${fillTemplate(sentences.join(' '), values)} No daily fast-check budget is set.`;
}

/** The window of a dry run in words ("24 h", "7 days"). */
export function studioWindowWords(window: string): string {
  return WINDOWS[window] ?? window;
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

/** What a test bench run on the mock fast check can and cannot tell. */
export const STUDIO_SIMULATED_TEST =
  "Simulated: the demo's mock fast check knows only the drafted example's own samples. It answers your own samples and edited questions arbitrarily, so those matches say nothing about your check. Connect a key to test it for real.";

/** What a dry run on the mock fast check can and cannot tell. */
export const STUDIO_SIMULATED_DRY_RUN =
  "Simulated: the demo's mock fast check answers questions about past traffic arbitrarily, so this count says nothing about your check. Connect a key to test it for real.";

/** copy.studio.dry_run on a mock fast check, where nothing is billed. */
export const STUDIO_DRY_RUN_SIMULATED_TITLE = 'Test on past traffic (simulated)';
