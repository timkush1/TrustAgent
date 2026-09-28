/**
 * Chart colors. Color means a verdict or an outcome tone (DESIGN.md), so every series color comes
 * from here, and only from the verdict and tone tokens or the neutral inks; there is no
 * categorical palette. Text in charts never wears these colors: labels, values and legends use
 * the text tokens, and a colored mark beside them carries the identity.
 *
 * Two series of the same tone (Flagged and Replaced are both "warning") are told apart by shade:
 * `solid` is the token itself, `light` is the token mixed 70% toward the surface. Both clear 3:1
 * against the surface in both themes (tokenContrast.test.ts), and the 2px surface gap between
 * stacked segments separates neighbours.
 */
import type { ClaimVerdict } from '@/types/api';
import { claimVerdictView, outcomeView, type OutcomeId, type Tone } from '@/domain/presentation';

export type ChartShade = 'solid' | 'light';

/** A tone, or the neutral ink for series that carry no verdict (latency, counts, cost). */
export type ChartTone = Tone | 'ink';

export interface SeriesStyle {
  tone: ChartTone;
  shade: ChartShade;
}

/** Share of the tone kept in the `light` shade (the rest is the surface). */
export const LIGHT_SHADE_PERCENT = 70;

function mixToSurface(color: string): string {
  return `color-mix(in oklab, ${color} ${LIGHT_SHADE_PERCENT}%, var(--surface))`;
}

/** The CSS color of a mark in a tone and shade. */
export function seriesColor({ tone, shade }: SeriesStyle): string {
  if (tone === 'ink') return shade === 'solid' ? 'var(--fg-muted)' : 'var(--fg-subtle)';
  const base = `var(--tone-${tone})`;
  return shade === 'solid' ? base : mixToSurface(base);
}

/**
 * Outcome series (tone from ui/outcomes.v1.json, shade from the pill's emphasis): the outcome
 * that stopped or acted on the answer is solid, its milder sibling of the same tone is light.
 */
const OUTCOME_SHADE: Record<OutcomeId, ChartShade> = {
  passed: 'solid',
  flagged: 'solid',
  replaced: 'light',
  blocked: 'solid',
  error: 'light',
  would_block: 'solid',
  would_flag: 'light',
  not_checked: 'solid',
  checking: 'light',
};

/** Stacking order, bottom to top: passed at the base, then observe-only, acted-on, failures. */
export const OUTCOME_STACK_ORDER: readonly OutcomeId[] = [
  'passed',
  'would_flag',
  'would_block',
  'flagged',
  'replaced',
  'blocked',
  'error',
  'not_checked',
  'checking',
];

export function outcomeSeriesStyle(id: OutcomeId): SeriesStyle {
  return { tone: outcomeView(id).tone, shade: OUTCOME_SHADE[id] };
}

/** Claim verdict series use the verdict tokens; Not checked is the light shade of Unknown's grey. */
export function verdictSeriesColor(verdict: ClaimVerdict): string {
  const view = claimVerdictView(verdict);
  const base = `var(${view.colorVar})`;
  return verdict === 'NOT_CHECKED' ? mixToSurface('var(--v-unknown)') : base;
}
