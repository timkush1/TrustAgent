/**
 * The Traffic list model (SPEC §3.3), as pure functions over wire objects:
 * - which traces match the filters in the URL (outcome, route, model, reason);
 * - how REST pages and live traces combine into one newest-first list;
 * - what each row says (headline reason, what it was checked against).
 */
import { mergeTraceSummary } from '@/data/normalize';
import type { TraceListParams } from '@/data/source';
import {
  checkedAgainst,
  evidenceSourceLabel,
  headlineReason,
  outcomeOf,
  reasonCopy,
  statusReason,
  statusReasonKey,
  type OutcomeId,
} from '@/domain/presentation';
import type { DetectorResult, GateDecisionView, TraceId, TraceSummary } from '@/types/api';
import type { TrafficSearchParams } from './search';

export type TrafficFilters = Pick<TrafficSearchParams, 'outcome' | 'route' | 'model' | 'reason'>;

export const TRAFFIC_PAGE_SIZE = 50;

export function hasFilters(f: TrafficFilters): boolean {
  return Boolean(f.outcome || f.route || f.model || f.reason);
}

/**
 * `reason` matches a whole reason (`contradiction:critical`, `det:numbers_not_in_evidence`) or an
 * evaluator at any level (`contradiction`).
 */
export function hasReason(reasons: readonly string[], reason: string): boolean {
  return reasons.some((r) => r === reason || r.startsWith(`${reason}:`));
}

export function matchesFilters(t: TraceSummary, f: TrafficFilters): boolean {
  if (f.outcome && outcomeOf(t) !== f.outcome) return false;
  if (f.route && t.route !== f.route) return false;
  if (f.model && t.model !== f.model) return false;
  if (f.reason && !hasReason(t.reasons, f.reason)) return false;
  return true;
}

/** GET /api/traces parameters for the filters. The API has no reason filter: that one is local. */
export function traceListParams(f: TrafficFilters): TraceListParams {
  const params: TraceListParams = { limit: TRAFFIC_PAGE_SIZE };
  if (f.outcome) params.outcome = f.outcome;
  if (f.route) params.route = f.route;
  if (f.model) params.model = f.model;
  return params;
}

function createdAt(t: TraceSummary): number {
  const v = Date.parse(t.created_at);
  return Number.isNaN(v) ? 0 : v;
}

export interface TrafficSources {
  /** Live traces in list order (streamStore.visibleOrder: frozen while paused). */
  live: readonly TraceSummary[];
  /** Every trace the stream knows, for updating REST rows in place (also while paused). */
  known: Readonly<Record<TraceId, TraceSummary>>;
  /** Live trace ids, newest first (streamStore.order), to count what a pause is holding back. */
  order: readonly TraceId[];
  /** REST pages, flattened, newest first. */
  rest: readonly TraceSummary[];
}

export interface TrafficRows {
  rows: TraceSummary[];
  /** Matching traces that arrived while paused and show on Resume. */
  held: number;
}

/**
 * Combines REST pages with the live stream. REST rows take their live version when there is one
 * (merged, so a decided trace never goes back to pending); live traces newer than the newest REST
 * row go on top. Older live-only traces are left to the next REST page. Filters apply to both.
 */
export function combineTrafficRows(src: TrafficSources, f: TrafficFilters): TrafficRows {
  const restIds = new Set<TraceId>();
  let newestRest = 0;
  for (const t of src.rest) {
    restIds.add(t.trace_id);
    newestRest = Math.max(newestRest, createdAt(t));
  }
  const onTop = (t: TraceSummary) => !restIds.has(t.trace_id) && createdAt(t) >= newestRest;

  const rows: TraceSummary[] = [];
  const shown = new Set<TraceId>();
  for (const t of src.live) {
    if (onTop(t) && !shown.has(t.trace_id) && matchesFilters(t, f)) {
      rows.push(t);
      shown.add(t.trace_id);
    }
  }
  for (const t of src.rest) {
    if (shown.has(t.trace_id)) continue;
    const live = src.known[t.trace_id];
    const merged = live ? mergeTraceSummary(t, live) : t;
    if (matchesFilters(merged, f)) {
      rows.push(merged);
      shown.add(t.trace_id);
    }
  }

  let held = 0;
  if (src.order.length !== src.live.length) {
    const visible = new Set(src.live.map((t) => t.trace_id));
    for (const id of src.order) {
      const t = src.known[id];
      if (t && !visible.has(id) && onTop(t) && matchesFilters(t, f)) held += 1;
    }
  }
  return { rows, held };
}

/** Distinct values of a field across rows (plus the active filter), sorted, for filter menus. */
export function distinctValues(rows: readonly TraceSummary[], field: 'route' | 'model', active?: string): string[] {
  const values = new Set<string>();
  for (const t of rows) {
    const v = t[field];
    if (v) values.add(v);
  }
  if (active) values.add(active);
  return [...values].sort((a, b) => a.localeCompare(b));
}

/** Detector findings for `det:<id>` reasons ("60" for numbers_not_in_evidence). */
export function detectorFinding(reason: string, detectors: readonly DetectorResult[] | undefined): string | null {
  if (!reason.startsWith('det:') || !detectors) return null;
  const hit = detectors.find((d) => `det:${d.id}` === reason);
  const texts = [...new Set(hit?.findings.map((f) => f.text).filter(Boolean) ?? [])];
  return texts.length > 0 ? texts.join(', ') : null;
}

/** Every reason of a decision in plain words, with detector findings filled in. */
export function reasonLines(reasons: readonly string[], decision?: Pick<GateDecisionView, 'detectors' | 'degraded'> | null): string[] {
  return reasons.map((r) =>
    reasonCopy(r, { finding: detectorFinding(r, decision?.detectors), degraded: decision?.degraded ?? null }),
  );
}

const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/**
 * The row's "why": the first reason in plain words or, for a trace that was not checked or whose
 * check failed, the status reason ("No documents in this request"). Null when there is nothing to
 * say (a pass, or still checking).
 */
export function rowReason(t: TraceSummary, decision?: Pick<GateDecisionView, 'detectors' | 'degraded'> | null): string | null {
  if (t.reasons.length > 0) {
    const first = t.reasons[0];
    return headlineReason(t.reasons, {
      finding: detectorFinding(first, decision?.detectors),
      degraded: decision?.degraded ?? null,
    });
  }
  const outcome: OutcomeId = outcomeOf(t);
  if (outcome !== 'not_checked' && outcome !== 'error') return null;
  const reason = statusReason(statusReasonKey({ ...t, degraded: decision?.degraded ?? null }));
  return reason ? sentence(reason) : null;
}

/**
 * "refund-policy.md" or "No documents" for the row; null while the check is still running. The
 * decision's evidence summary stands in when the trace row does not carry one.
 */
export function rowCheckedAgainst(
  t: TraceSummary,
  decision?: Pick<GateDecisionView, 'evidence_summary'> | null,
): { text: string; none: boolean } | null {
  if (t.gate_status === 'pending') return null;
  const c = checkedAgainst(t.checked_against ?? decision?.evidence_summary);
  return { text: c.none ? evidenceSourceLabel('none') : c.items.join(' · '), none: c.none };
}
