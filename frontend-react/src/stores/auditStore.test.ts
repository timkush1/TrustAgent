import { describe, it, expect, beforeEach } from 'vitest';
import { useAuditStore } from './auditStore';
import type { AuditResult } from '../types/audit';

function makeAudit(overrides: Partial<AuditResult> = {}): AuditResult {
  return {
    audit_id: 'audit-1',
    request_id: 'req-1',
    user_query: 'What is the capital of France?',
    llm_response: 'Paris is the capital of France.',
    faithfulness_score: 1,
    relevancy_score: 1,
    overall_score: 1,
    hallucination_detected: false,
    claims: [],
    reasoning_trace: '',
    processing_time_ms: 100,
    timestamp: new Date().toISOString(),
    ...overrides,
  };
}

beforeEach(() => {
  localStorage.clear();
  useAuditStore.setState({ audits: [], selectedAudit: null });
});

describe('auditStore', () => {
  it('prepends new audits', () => {
    useAuditStore.getState().addAudit(makeAudit({ audit_id: 'a' }));
    useAuditStore.getState().addAudit(makeAudit({ audit_id: 'b' }));

    const ids = useAuditStore.getState().audits.map((a) => a.audit_id);
    expect(ids).toEqual(['b', 'a']);
  });

  it('caps the feed at 100 audits', () => {
    for (let i = 0; i < 110; i++) {
      useAuditStore.getState().addAudit(makeAudit({ audit_id: `a-${i}` }));
    }

    const { audits } = useAuditStore.getState();
    expect(audits).toHaveLength(100);
    expect(audits[0].audit_id).toBe('a-109'); // newest kept
  });

  it('selects an audit by id and clears selection with null', () => {
    useAuditStore.getState().addAudit(makeAudit({ audit_id: 'a' }));

    useAuditStore.getState().selectAudit('a');
    expect(useAuditStore.getState().selectedAudit?.audit_id).toBe('a');

    useAuditStore.getState().selectAudit(null);
    expect(useAuditStore.getState().selectedAudit).toBeNull();
  });

  it('returns null selection for an unknown id', () => {
    useAuditStore.getState().selectAudit('does-not-exist');
    expect(useAuditStore.getState().selectedAudit).toBeNull();
  });

  it('clearAudits resets audits and selection', () => {
    useAuditStore.getState().addAudit(makeAudit());
    useAuditStore.getState().selectAudit('audit-1');

    useAuditStore.getState().clearAudits();

    expect(useAuditStore.getState().audits).toHaveLength(0);
    expect(useAuditStore.getState().selectedAudit).toBeNull();
  });

  it('getStats returns zeros for an empty store', () => {
    expect(useAuditStore.getState().getStats()).toEqual({
      total: 0,
      avgScore: 0,
      hallucinationCount: 0,
      hallucinationRate: 0,
    });
  });

  it('getStats computes average score and hallucination rate', () => {
    useAuditStore.getState().addAudit(makeAudit({ audit_id: 'a', overall_score: 1 }));
    useAuditStore
      .getState()
      .addAudit(makeAudit({ audit_id: 'b', overall_score: 0, hallucination_detected: true }));

    const stats = useAuditStore.getState().getStats();
    expect(stats.total).toBe(2);
    expect(stats.avgScore).toBeCloseTo(0.5);
    expect(stats.hallucinationCount).toBe(1);
    expect(stats.hallucinationRate).toBeCloseTo(0.5);
  });

  it('persists audits (but not selection) to localStorage', () => {
    useAuditStore.getState().addAudit(makeAudit({ audit_id: 'persisted' }));
    useAuditStore.getState().selectAudit('persisted');

    const raw = localStorage.getItem('trustagent-audit-storage');
    expect(raw).not.toBeNull();
    const stored = JSON.parse(raw!);
    expect(stored.state.audits[0].audit_id).toBe('persisted');
    expect(stored.state.selectedAudit).toBeUndefined();
  });
});
