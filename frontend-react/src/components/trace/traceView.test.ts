import { describe, expect, it } from 'vitest';
import { decision } from '@/test/fixtures';
import { s0Audit, s0Decision, s0Detail, s0Trace, scenario } from '@/test/scenarios';
import type { EvaluatorResult } from '@/types/api';
import {
  buildTraceView,
  checkRows,
  claimsSummary,
  disagreementLine,
  evaluatorName,
  latestDecision,
  orderedClaims,
  pickAudit,
} from './traceView';

const REFUND = 'refund-contradiction-flag' as const;

describe('buildTraceView', () => {
  it('returns null until some source knows the trace', () => {
    expect(buildTraceView({})).toBeNull();
  });

  it('renders from the live stream alone, with the preview as the answer', () => {
    const view = buildTraceView({ live: s0Trace(REFUND, 'decided'), liveDecision: s0Decision(REFUND) });
    expect(view?.summary.action).toBe('flag');
    expect(view?.decision?.reasons).toContain('det:numbers_not_in_evidence');
    expect(view?.answer).toEqual({ text: scenario(REFUND).response, preview: true });
    expect(view?.question).toBe(scenario(REFUND).request.messages[1].content);
  });

  it('prefers the stored body and moves forward whichever source is newer', () => {
    // REST fetched while pending; the live decision arrived after.
    const view = buildTraceView({
      detail: s0Detail(REFUND, 'started'),
      live: s0Trace(REFUND, 'decided'),
      liveDecision: s0Decision(REFUND),
    });
    expect(view?.summary.gate_status).toBe('ok');
    expect(view?.answer).toEqual({ text: scenario(REFUND).response, preview: false });
    expect(view?.body?.evidence[0].id).toBe('refund-policy.md');

    // A stale live copy never undoes a stored decision.
    const back = buildTraceView({ detail: s0Detail(REFUND, 'decided'), live: s0Trace(REFUND, 'started') });
    expect(back?.summary.gate_status).toBe('ok');
  });

  it('folds a completed audit from any source into the trace', () => {
    const view = buildTraceView({ detail: s0Detail(REFUND, 'decided'), fetchedAudit: s0Audit(REFUND) });
    expect(view?.audit?.verdict).toBe('HALLUCINATED');
    expect(view?.summary.audit).toMatchObject({ status: 'completed', verdict: 'HALLUCINATED' });
  });
});

describe('latestDecision and pickAudit', () => {
  it('keeps the highest attempt and a late decision over an on-time one', () => {
    const first = decision({ attempt: 0 });
    const late = decision({ attempt: 0, late: true, action: 'pass' });
    const retry = decision({ attempt: 1 });
    expect(latestDecision([first, null, late])).toBe(late);
    expect(latestDecision([retry, late])).toBe(retry);
    expect(latestDecision([])).toBeNull();
  });

  it('picks the audit the trace points at, else the most complete one', () => {
    const summary = s0Trace(REFUND, 'decided');
    const done = s0Audit(REFUND);
    const running = { ...done, status: 'running' as const, claims: [] };
    const other = { ...done, audit_id: 'another-audit' };
    expect(pickAudit(summary, [running, done])).toBe(done);
    expect(pickAudit(summary, [other, running])).toBe(running);
    expect(pickAudit(summary, [{ ...done, trace_id: 'someone-else' }])).toBeNull();
  });
});

describe('checks', () => {
  it('gives one row per evaluator in route order with thresholds', () => {
    const rows = checkRows(s0Decision(REFUND));
    expect(rows.map((r) => r.name)).toEqual(['Groundedness', 'Contradiction']);
    expect(rows[0]).toMatchObject({ kind: 'assessed', level: 'critical', risk: 0.96, warnAt: 0.8, criticalAt: 0.95 });
    expect(rows[1]).toMatchObject({ kind: 'assessed', level: 'critical', risk: 0.98, warnAt: 0.7, criticalAt: 0.95 });
  });

  it('says "Not checked — no documents" when there was nothing to check against', () => {
    const rows = checkRows(s0Decision('no-evidence-not-assessed'));
    expect(rows.map((r) => [r.kind, r.note])).toEqual([
      ['no_documents', 'Not checked — no documents'],
      ['no_documents', 'Not checked — no documents'],
    ]);
  });

  it('explains other exclusions and errors, and marks shadow evaluators', () => {
    const base = s0Decision('grounded-pass').evaluators['groundedness@1'];
    const make = (over: Partial<EvaluatorResult>) => decision({ evaluators: { x: { ...base, ...over } } });
    expect(checkRows(make({ status: 'not_assessed', reason: 'too_long', risk: null, level: null }))[0].note).toBe(
      'Not checked — the answer is too long to check',
    );
    expect(checkRows(make({ status: 'error', reason: 'timeout', risk: null, level: null }))[0].note).toBe(
      'Check failed — the check timed out',
    );
    expect(checkRows(make({ status: 'error', reason: 'unexpected_option', risk: null, level: null }))[0].note).toBe(
      'Check failed — unexpected option',
    );
    expect(checkRows(make({ stage: 'shadow' }))[0].shadow).toBe(true);
    expect(checkRows(null)).toEqual([]);
    expect(evaluatorName('pii_secret_leak@1')).toBe('Pii secret leak');
  });
});

describe('claims', () => {
  it('orders claims and summarises the verdicts', () => {
    const audit = s0Audit('grounded-pass');
    expect(orderedClaims({ ...audit, claims: [...audit.claims].reverse() }).map((c) => c.index)).toEqual([0, 1]);
    expect(claimsSummary(audit)).toBe('2 of 2 claims supported · 0 contradicted');
    expect(claimsSummary(s0Audit(REFUND))).toBe('0 of 1 claims supported · 1 contradicted');
    expect(claimsSummary(null)).toBeNull();
  });

  it('reports a disagreement only when the fast check passed a contradicted answer', () => {
    const refund = s0Audit(REFUND);
    expect(disagreementLine({ summary: s0Trace(REFUND), audit: refund })).toBeNull();
    expect(disagreementLine({ summary: s0Trace('grounded-pass'), audit: refund })).toBe(
      'Fast check passed it; deep audit found 1 contradicted claim(s).',
    );
    expect(disagreementLine({ summary: s0Trace('grounded-pass'), audit: s0Audit('grounded-pass') })).toBeNull();
  });
});
