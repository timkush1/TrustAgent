/**
 * The glass box's numbers (JevRequestPanel): the summary line, how many questions were asked, and
 * whether a recompile of the request matches what was recorded with the decision (P4-PLAN D63).
 */
import { formatUsd } from '@/domain/format';
import { copy, costSourceLabel, fillTemplate } from '@/domain/presentation';
import type { DecisionRequestView, GateDecisionView } from '@/types/api';

/** "1 Jev call · 4 questions · 212 ms · $0.0002 (Vendor) · typesafe/jev-1.13-20260917". */
export function jevRequestSummary(decision: GateDecisionView, questions: number): string {
  const usage = decision.decision_model;
  const calls = usage?.calls ?? 1;
  const label = usage ? costSourceLabel(usage.cost_source) : null;
  const cost = usage && usage.cost_usd !== null && label !== null ? `${formatUsd(usage.cost_usd)} (${label})` : 'cost not reported';
  const text = fillTemplate(copy.trace.jev_request_summary, {
    calls,
    questions,
    latency_ms: usage?.latency_ms ?? '—',
    cost,
    snapshot: usage?.snapshot ?? usage?.model_requested ?? 'version not reported',
  });
  // The contract sentence is written for one call and several questions.
  return text.replace(`${calls} Jev call `, calls === 1 ? '1 Jev call ' : `${calls} Jev calls `).replace(/(^|· )1 questions/, (_m, lead: string) => `${lead}1 question`);
}

/** Questions the fast check was asked: those assessed or failed (a check without documents is never asked). */
export function askedCount(decision: GateDecisionView): number {
  return Object.values(decision.evaluators).filter((r) => r.status !== 'not_assessed').length;
}

/** The recompile matches when the server says so and its fingerprints are the ones recorded with the decision. */
export function recompileMatches(view: DecisionRequestView, decision: GateDecisionView): boolean {
  if (!view.matches_recorded) return false;
  const recorded = decision.compile?.request_sha256;
  if (!recorded) return true;
  return recorded.length === view.request_sha256.length && recorded.every((h, i) => h === view.request_sha256[i]);
}

export function questionsIn(view: DecisionRequestView): number {
  return view.requests.reduce((n, r) => n + Object.keys(r.questions ?? {}).length, 0);
}

