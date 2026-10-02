import { describe, expect, it } from 'vitest';
import { evidenceCoverage, overviewStats, roiSummary } from '@/test/control/fixtures';
import { adminStatus } from '@/test/control/renderPage';
import {
  bucketDescription,
  caughtRows,
  coverageSummary,
  outcomeBuckets,
  savingsLine,
  setupChecklist,
  weakestLabel,
} from './overviewModel';

describe('overview model', () => {
  it('maps the outcome series onto outcome ids', () => {
    const [first] = outcomeBuckets(overviewStats().outcomes_series);
    expect(Object.keys(first.counts).sort()).toEqual(['blocked', 'error', 'flagged', 'not_checked', 'passed', 'replaced', 'would_block', 'would_flag']);
  });

  it('describes the bucket and window', () => {
    expect(bucketDescription({ bucket: '15m', window: '24h' })).toBe('Per 15 minutes, the last 24 hours');
  });

  it('ranks what was caught and words each reason', () => {
    const rows = caughtRows({
      what_was_caught: [
        { reason: 'groundedness:warn', count: 2, latest_trace_id: 'a' },
        { reason: 'contradiction:critical', count: 9, latest_trace_id: 'b' },
      ],
    });
    expect(rows.map((r) => r.label)).toEqual(['Contradicts your documents', "States facts your documents don't contain"]);
    expect(rows[0].latestTraceId).toBe('b');
  });

  it('writes the cost headline per 1,000 answers', () => {
    const line = savingsLine(roiSummary());
    expect(line.headline).toBe('Your cost: $0.80 per 1,000 answers (fast check $0.05 + deep audit $0.76)');
    expect(line.label).toBe('measured');
    expect(line.earlyData).toMatch(/^So far: 540 answers checked for \$0\.43\./);
    expect(line.earlyDataLabel).toBe('estimate');
    expect(line.stopped).toBe(4);
  });

  it('labels everything simulated on a mock stack', () => {
    const line = savingsLine(roiSummary({ simulated: true }));
    expect(line.label).toBe('simulated');
    expect(line.earlyDataLabel).toBe('simulated');
    expect(line.outcomesLabel).toBe('simulated');
  });

  it('takes the weakest label of a sum', () => {
    expect(weakestLabel(['measured', 'estimate'])).toBe('estimate');
    expect(weakestLabel(['measured', 'vendor'])).toBe('vendor');
    expect(weakestLabel(['measured'])).toBe('measured');
  });

  it('summarizes coverage per source and orders the gaps', () => {
    const s = coverageSummary(612, evidenceCoverage());
    expect(s.headline).toBe('Groundedness checked on 85% of requests');
    expect(s.bySource.find((b) => b.source === 'request_system')).toMatchObject({ count: 410, label: 'System prompt documents' });
    expect(s.gaps.map((g) => g.reason)).toEqual(['no_evidence', 'evidence_too_short', 'evidence_packed_away']);
  });

  it('has no rate without traffic', () => {
    expect(coverageSummary(0, { groundedness_assessed: 0, by_source: evidenceCoverage().by_source }).rate).toBeNull();
  });

  it('builds the setup checklist from status and traffic', () => {
    const items = setupChecklist({ status: adminStatus(), requests: 0, firstRequestSeen: false, evidenceRequests: null });
    expect(items.map((i) => [i.id, i.done])).toEqual([
      ['secure', true],
      ['fast_check', true],
      ['judge', true],
      ['database', true],
      ['first_request', false],
    ]);
    const later = setupChecklist({ status: null, requests: 5, firstRequestSeen: false, evidenceRequests: 0 });
    expect(later.map((i) => [i.id, i.done])).toEqual([
      ['first_request', true],
      ['evidence', false],
    ]);
  });

  it('counts the fast check as connected only when it can answer', () => {
    const base = adminStatus();
    const off = setupChecklist({
      status: { ...base, decision_model: { ...base.decision_model, state: 'unauthorized' } },
      requests: 1,
      firstRequestSeen: true,
      evidenceRequests: 1,
    });
    expect(off.find((i) => i.id === 'fast_check')?.done).toBe(false);
  });

  it('asks to secure the dashboard while the setup link is unused, and sends both fixes to the setup wizard', () => {
    const base = adminStatus();
    const items = setupChecklist({ status: { ...base, auth: { ...base.auth, setup_required: true } }, requests: 0, firstRequestSeen: false, evidenceRequests: null });
    const secure = items.find((i) => i.id === 'secure');
    expect(secure).toMatchObject({ done: false, label: 'Secure this dashboard', fix: { to: '/app/setup', search: { step: 'secure' } } });
    expect(items.find((i) => i.id === 'fast_check')?.fix).toEqual({ to: '/app/setup', search: { step: 'connect' }, label: 'Connect OpenRouter' });
  });
});
