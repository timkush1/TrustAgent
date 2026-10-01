/**
 * Query keys, queryOptions and stream side effects of the evaluator plane (data/control/evaluatorPlane.ts).
 * Owned by WEB-EV after Step 0b; queries.ts re-exports `qkcEvaluators` and calls `evaluatorSideEffects`.
 * (The P3 evaluator list, detail and reliability keys stay in `qkc.evaluators`.)
 */
import type { QueryClient } from '@tanstack/react-query';
import type { CalibrationListParams, DecisionRequestParams, LabelQueueParams } from '../control/evaluatorPlane';
import type { StreamEvent } from '../stream/types';

export const qkcEvaluators = {
  all: ['evaluator-plane'] as const,
  decisionRequest: (traceId: string, params: DecisionRequestParams) => ['evaluator-plane', 'decision-request', traceId, params] as const,
  labelQueue: (params: LabelQueueParams) => ['evaluator-plane', 'label-queue', params] as const,
  calibrations: (params: CalibrationListParams) => ['evaluator-plane', 'calibrations', params] as const,
  calibration: (id: number) => ['evaluator-plane', 'calibration', id] as const,
};

/** Keeps evaluator-plane queries in step with one live event (calibration_updated, …). No-op until WEB-EV EVW-1. */
export function evaluatorSideEffects(_event: StreamEvent, _queryClient: QueryClient): void {}
