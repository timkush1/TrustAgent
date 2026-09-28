import { describe, expect, it } from 'vitest';
import { iso, trace } from '@/test/fixtures';
import { REFUND_DOC, s0Decision, s0Trace, s0Traces, traceIdOf } from '@/test/scenarios';
import {
  combineTrafficRows,
  detectorFinding,
  distinctValues,
  hasFilters,
  hasReason,
  matchesFilters,
  reasonLines,
  rowCheckedAgainst,
  rowReason,
  traceListParams,
  TRAFFIC_PAGE_SIZE,
  type TrafficSources,
} from './trafficRows';

const byId = (list: ReturnType<typeof s0Traces>) => Object.fromEntries(list.map((t) => [t.trace_id, t]));
const ids = (list: { trace_id: string }[]) => list.map((t) => t.trace_id);

function sources(over: Partial<TrafficSources> = {}): TrafficSources {
  return { live: [], known: {}, order: [], rest: [], ...over };
}

describe('filters', () => {
  const flagged = s0Trace('refund-contradiction-flag');
  const notChecked = s0Trace('no-evidence-not-assessed');

  it('matches outcome, route, model and reason', () => {
    expect(matchesFilters(flagged, { outcome: 'flagged' })).toBe(true);
    expect(matchesFilters(notChecked, { outcome: 'flagged' })).toBe(false);
    expect(matchesFilters(notChecked, { outcome: 'not_checked' })).toBe(true);
    expect(matchesFilters(flagged, { route: 'support-bot', model: 'gpt-4o-mini' })).toBe(true);
    expect(matchesFilters(flagged, { route: 'default' })).toBe(false);
    expect(matchesFilters(flagged, { model: 'claude-haiku-4-5' })).toBe(false);
    expect(matchesFilters(flagged, { reason: 'det:numbers_not_in_evidence' })).toBe(true);
    expect(matchesFilters(notChecked, { reason: 'det:numbers_not_in_evidence' })).toBe(false);
  });

  it('matches a reason whole or an evaluator at any level', () => {
    expect(hasReason(['contradiction:critical'], 'contradiction')).toBe(true);
    expect(hasReason(['contradiction:critical'], 'contradiction:critical')).toBe(true);
    expect(hasReason(['contradiction:critical'], 'contradiction:warn')).toBe(false);
    expect(hasReason(['contradictionx:warn'], 'contradiction')).toBe(false);
  });

  it('sends every filter but the reason to the API', () => {
    expect(traceListParams({ outcome: 'flagged', route: 'support-bot', model: 'm', reason: 'contradiction' })).toEqual({
      limit: TRAFFIC_PAGE_SIZE,
      outcome: 'flagged',
      route: 'support-bot',
      model: 'm',
    });
    expect(traceListParams({})).toEqual({ limit: TRAFFIC_PAGE_SIZE });
    expect(hasFilters({})).toBe(false);
    expect(hasFilters({ reason: 'x' })).toBe(true);
  });

  it('offers the routes and models seen, plus the active one', () => {
    expect(distinctValues(s0Traces(), 'route')).toEqual(['support-bot']);
    expect(distinctValues(s0Traces(), 'model', 'a-model')).toEqual(['a-model', 'gpt-4o-mini']);
  });
});

describe('combineTrafficRows', () => {
  it('shows REST rows newest first when there is no live traffic', () => {
    const rest = s0Traces();
    expect(ids(combineTrafficRows(sources({ rest }), {}).rows)).toEqual(ids(rest));
  });

  it('puts live traces newer than the REST page on top and leaves older ones to paging', () => {
    const rest = s0Traces();
    const newer = trace({ trace_id: 'live-new', created_at: iso(10_000), updated_at: iso(10_000) });
    const older = trace({ trace_id: 'live-old', created_at: iso(-10_000), updated_at: iso(-10_000) });
    const live = [newer, older];
    const { rows } = combineTrafficRows(sources({ live, known: byId(live), order: ids(live), rest }), {});
    expect(ids(rows)).toEqual(['live-new', ...ids(rest)]);
  });

  it('updates a REST row with its live version, never moving it backwards', () => {
    const restPending = [s0Trace('refund-contradiction-flag', 'started')];
    const liveDecided = s0Trace('refund-contradiction-flag', 'decided');
    const { rows } = combineTrafficRows(
      sources({ rest: restPending, known: byId([liveDecided]), live: [liveDecided], order: [liveDecided.trace_id] }),
      {},
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ gate_status: 'ok', action: 'flag' });

    // A stale live copy (still pending) cannot undo the REST decision.
    const { rows: again } = combineTrafficRows(
      sources({ rest: [liveDecided], known: byId([s0Trace('refund-contradiction-flag', 'started')]) }),
      {},
    );
    expect(again[0].gate_status).toBe('ok');
  });

  it('applies the filters to live and REST rows alike', () => {
    const rest = s0Traces();
    const liveFlag = trace({
      trace_id: 'live-flag',
      created_at: iso(10_000),
      updated_at: iso(10_000),
      gate_status: 'ok',
      action: 'flag',
      would_action: 'flag',
    });
    const { rows } = combineTrafficRows(
      sources({ live: [liveFlag], known: byId([liveFlag]), order: ['live-flag'], rest }),
      { outcome: 'flagged' },
    );
    expect(ids(rows)).toEqual(['live-flag', traceIdOf('refund-contradiction-flag')]);
  });

  it('counts the matching traces a pause is holding back', () => {
    const rest = s0Traces();
    const shown = trace({ trace_id: 'shown', created_at: iso(10_000), updated_at: iso(10_000) });
    const heldPass = trace({ trace_id: 'held-1', created_at: iso(20_000), updated_at: iso(20_000) });
    const heldFlag = trace({
      trace_id: 'held-2',
      created_at: iso(21_000),
      updated_at: iso(21_000),
      gate_status: 'ok',
      action: 'flag',
    });
    const known = byId([shown, heldPass, heldFlag]);
    const src = sources({ live: [shown], known, order: ['held-2', 'held-1', 'shown'], rest });
    const all = combineTrafficRows(src, {});
    expect(ids(all.rows)).toEqual(['shown', ...ids(rest)]);
    expect(all.held).toBe(2);
    expect(combineTrafficRows(src, { outcome: 'flagged' }).held).toBe(1);
  });
});

describe('row words', () => {
  it('uses the first reason in plain words', () => {
    const flagged = s0Trace('refund-contradiction-flag');
    expect(rowReason(flagged, s0Decision('refund-contradiction-flag'))).toBe('Contradicts your documents');
  });

  it('says why a trace was not checked', () => {
    expect(rowReason(s0Trace('no-evidence-not-assessed'))).toBe('No documents in this request');
    expect(rowReason(trace({ gate_status: 'skipped', skip_reason: 'budget', action: 'pass' }))).toBe(
      "Today's fast-check budget is used up",
    );
  });

  it('has nothing to say for a pass or a trace still being checked', () => {
    expect(rowReason(s0Trace('grounded-pass'))).toBeNull();
    expect(rowReason(s0Trace('grounded-pass', 'started'))).toBeNull();
  });

  it('names the documents an answer was checked against', () => {
    expect(rowCheckedAgainst(s0Trace('refund-contradiction-flag'))).toEqual({ text: REFUND_DOC, none: false });
    expect(rowCheckedAgainst(s0Trace('no-evidence-not-assessed'))).toEqual({ text: 'No documents', none: true });
    expect(rowCheckedAgainst(s0Trace('grounded-pass', 'started'))).toBeNull();
    // A row without its own summary uses the decision's.
    const bare = { ...s0Trace('refund-contradiction-flag'), checked_against: null };
    expect(rowCheckedAgainst(bare, s0Decision('refund-contradiction-flag'))).toEqual({ text: REFUND_DOC, none: false });
  });

  it('fills detector findings into det: reasons', () => {
    const d = s0Decision('refund-contradiction-flag');
    expect(detectorFinding('det:numbers_not_in_evidence', d.detectors)).toBe('60');
    expect(detectorFinding('contradiction:critical', d.detectors)).toBeNull();
    expect(reasonLines(d.reasons, d)).toEqual([
      'Contradicts your documents',
      "States facts your documents don't contain",
      'Mentions a number not in your documents: 60',
    ]);
    expect(reasonLines(['det:numbers_not_in_evidence'], null)).toEqual(['Mentions a number not in your documents']);
  });
});
