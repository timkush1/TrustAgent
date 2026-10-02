import { QueryClient } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import { applyControlSideEffects, qkc } from './queries';
import type { StreamEvent } from './stream/types';

const ev = (type: string): StreamEvent => ({ v: 2, seq: 1, epoch: 'e', ts: 't', type, data: {} }) as unknown as StreamEvent;

function keysInvalidated(batch: StreamEvent[]): string[] {
  const qc = new QueryClient();
  const spy = vi.spyOn(qc, 'invalidateQueries');
  applyControlSideEffects(qc, batch);
  return spy.mock.calls.map(([filters]) => String((filters as { queryKey: readonly string[] }).queryKey[0])).sort();
}

describe('control-plane side effects of stream events', () => {
  it('refetches policies and evaluators when the policy changes', () => {
    expect(keysInvalidated([ev('policy_updated')])).toEqual(['evaluators', 'policies']);
  });

  it('refetches evaluators and block readiness on calibration and snapshot changes', () => {
    // The evaluator plane also refreshes its calibration lists and records (EVW-1).
    expect(keysInvalidated([ev('calibration_updated')])).toEqual(['evaluator-plane', 'evaluator-plane', 'evaluators', 'policies']);
    expect(keysInvalidated([ev('decision_model_changed')])).toEqual(['evaluator-plane', 'evaluator-plane', 'evaluators', 'policies']);
  });

  it('refreshes the overview, its evidence coverage and the policy after a (re)connect or resync', () => {
    expect(keysInvalidated([ev('hello')])).toEqual(['evidence', 'overview', 'policies']);
    expect(keysInvalidated([ev('resync')])).toEqual(['evidence', 'overview', 'policies']);
  });

  it('does nothing for per-trace events', () => {
    expect(keysInvalidated([ev('gate_decision'), ev('audit_completed')])).toEqual([]);
  });

  it('uses the page query keys', () => {
    expect(qkc.policies.all).toEqual(['policies']);
    expect(qkc.overview.stats({ window: '24h' })[0]).toBe('overview');
  });
});
