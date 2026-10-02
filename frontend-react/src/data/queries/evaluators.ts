/**
 * Query keys, queryOptions and stream side effects of the evaluator plane (data/control/evaluatorPlane.ts).
 * Owned by WEB-EV; queries.ts re-exports this module and calls `evaluatorSideEffects`.
 * (The P3 evaluator list, detail and reliability keys stay in `qkc.evaluators`.)
 *
 * Labels have no live event (P4-PLAN D61): after a label is written or removed, the page that wrote
 * it calls `invalidateAfterLabel`, which refreshes the label queue, the evaluator's calibration
 * progress and reliability, and the trace. A new or changed calibration arrives as
 * `calibration_updated` and refreshes the calibration records.
 */
import { queryOptions, type QueryClient } from '@tanstack/react-query';
import type { CalibrationProgress } from '@/types/api';
import type { CalibrationListParams, DecisionRequestParams, LabelQueueParams } from '../control/evaluatorPlane';
import { qk, qkc } from '../queries';
import type { DataSource } from '../source';
import type { StreamEvent } from '../stream/types';

export const qkcEvaluators = {
  all: ['evaluator-plane'] as const,
  decisionRequest: (traceId: string, params: DecisionRequestParams) => ['evaluator-plane', 'decision-request', traceId, params] as const,
  labelQueues: ['evaluator-plane', 'label-queue'] as const,
  labelQueue: (params: LabelQueueParams) => ['evaluator-plane', 'label-queue', params] as const,
  calibrationLists: ['evaluator-plane', 'calibrations'] as const,
  calibrations: (params: CalibrationListParams) => ['evaluator-plane', 'calibrations', params] as const,
  calibrationRecords: ['evaluator-plane', 'calibration'] as const,
  calibration: (id: number) => ['evaluator-plane', 'calibration', id] as const,
};

/** The label queue's default window and size (#39, D60: window 7 d, limit at most 50). */
export const LABEL_QUEUE_DEFAULTS = { window: '7d', limit: 50 } as const satisfies LabelQueueParams;

/** GET /api/traces/:id/decision-request (#12): a deterministic recompile, so it never goes stale. */
export function decisionRequestQuery(ds: DataSource, traceId: string, params: DecisionRequestParams) {
  return queryOptions({
    queryKey: qkcEvaluators.decisionRequest(traceId, params),
    queryFn: ({ signal }) => ds.getDecisionRequest(traceId, params, signal),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/**
 * GET /api/labels/queue (#39, admin). Labelling changes the queue under the viewer, so it is read
 * once per visit and only refreshed on request (or after an Undo).
 */
export function labelQueueQuery(ds: DataSource, params: LabelQueueParams) {
  return queryOptions({
    queryKey: qkcEvaluators.labelQueue(params),
    queryFn: ({ signal }) => ds.getLabelQueue(params, signal),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
    refetchOnWindowFocus: false,
  });
}

/** GET /api/calibration (#42). */
export function calibrationsQuery(ds: DataSource, params: CalibrationListParams = {}) {
  return queryOptions({
    queryKey: qkcEvaluators.calibrations(params),
    queryFn: ({ signal }) => ds.listCalibrations(params, signal),
    retry: false,
  });
}

/** GET /api/calibration/:id (#43): a record never changes except for `active`. */
export function calibrationQuery(ds: DataSource, id: number) {
  return queryOptions({
    queryKey: qkcEvaluators.calibration(id),
    queryFn: ({ signal }) => ds.getCalibration(id, signal),
    retry: false,
  });
}

/**
 * After a label is written or removed (D61: no live event): the queue, the calibration progress and
 * reliability of each evaluator it touched, and the trace it belongs to.
 */
export function invalidateAfterLabel(queryClient: QueryClient, change: { traceId: string; evaluatorIds: readonly string[] }): void {
  void queryClient.invalidateQueries({ queryKey: qkcEvaluators.labelQueues, refetchType: 'none' });
  void queryClient.invalidateQueries({ queryKey: qk.traces.detail(change.traceId) });
  for (const id of new Set(change.evaluatorIds)) {
    void queryClient.invalidateQueries({ queryKey: qkc.evaluators.detail(id) });
  }
  void queryClient.invalidateQueries({ queryKey: ['evaluators', 'reliability'] });
}

/** One label's effect on calibration progress, applied before the server answers (and undone on failure). */
export function bumpProgress(progress: CalibrationProgress, label: 'should_fire' | 'should_not_fire', by: 1 | -1): CalibrationProgress {
  const n = Math.max(0, progress.human_labels.n + by);
  const pos = Math.max(0, progress.human_labels.pos + (label === 'should_fire' ? by : 0));
  const neg = Math.max(0, progress.human_labels.neg + (label === 'should_not_fire' ? by : 0));
  return { ...progress, human_labels: { n, pos, neg } };
}

/** Keeps evaluator-plane queries in step with one live event: a calibration change refreshes the records. */
export function evaluatorSideEffects(event: StreamEvent, queryClient: QueryClient): void {
  switch (event.type) {
    case 'calibration_updated':
    case 'decision_model_changed':
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists, refetchType: 'active' });
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationRecords, refetchType: 'active' });
      break;
    case 'hello':
    case 'resync':
      // A reconnect may have missed calibration changes.
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists, refetchType: 'active' });
      break;
    default:
      break;
  }
}
