/**
 * The Studio plane of the control plane (SPEC §2.4 #34-#38, §7.3; P4-PLAN D58, D82): draft with
 * AI, the test bench, samples from past traffic, quick calibration and "Test on past traffic"
 * (an SSE dry run that is cancelled by aborting the request).
 *
 * Step 0b declares the signatures; every method rejects with 404 `not_implemented` until WEB-ST
 * lands its body. The signatures are fixed (P4-PLAN §0 rule 3); the bodies and STUDIO_SHAPES belong to WEB-ST.
 */
import type {
  DryRunResult,
  QuickCalibration,
  QuickCalibrationRequest,
  StudioDraftRequest,
  StudioDraftResponse,
  StudioDryRunEvent,
  StudioDryRunRequest,
  StudioSamplesRequest,
  StudioSamplesResponse,
  StudioTestRequest,
  StudioTestResponse,
} from '@/types/api';
import type { HttpClient } from '../http';
import type { NestedShape } from '../normalize';
import { rejectNotImplemented } from './shared';

type DryRunData<E extends StudioDryRunEvent['event']> = Extract<StudioDryRunEvent, { event: E }>['data'];

/** Events of a Studio dry run before its `done` result (SSE `estimate`, `progress`). */
export interface StudioDryRunHandlers {
  /** The server's cost estimate, sent first (D82). */
  onEstimate?: (data: DryRunData<'estimate'>) => void;
  onProgress?: (data: DryRunData<'progress'>) => void;
}

export interface StudioPlane {
  /** POST /api/studio/draft (#34): 503 `no_judge`, `budget_exhausted`; `cost_source` labels a mock draft Simulated. */
  draftEvaluator(request: StudioDraftRequest, signal?: AbortSignal): Promise<StudioDraftResponse>;
  /** POST /api/studio/test (#35, ≤ 50 samples): 503 `decision_model_unavailable` | `budget_exhausted`. */
  testEvaluator(request: StudioTestRequest, signal?: AbortSignal): Promise<StudioTestResponse>;
  /** POST /api/studio/samples (#36): samples from stored bodies. */
  getStudioSamples(request: StudioSamplesRequest, signal?: AbortSignal): Promise<StudioSamplesResponse>;
  /** POST /api/studio/calibrate (#37): pure, free; never says "calibrated". */
  quickCalibrate(request: QuickCalibrationRequest, signal?: AbortSignal): Promise<QuickCalibration>;
  /**
   * POST /api/studio/dry-run (#38): resolves with the `done` result. The first call never sends
   * `confirm_cost`; a 409 `confirmation_required` carries the estimate (D82). Abort `signal` to cancel.
   */
  dryRunEvaluator(request: StudioDryRunRequest, handlers: StudioDryRunHandlers, signal?: AbortSignal): Promise<DryRunResult>;
}

/** What each Studio response must carry (see normalizeShaped). */
export const STUDIO_SHAPES = {
  draft: { strings: ['model'], lists: ['errors', 'lints'] },
  test: { strings: ['run_id'], lists: ['results'], records: ['summary'] },
  samples: { lists: ['samples'] },
  quickCalibration: { strings: ['message'], lists: ['grid'], records: ['mapping'] },
  dryRun: { strings: ['run_id', 'evaluator_ref', 'route'], lists: ['overlap', 'examples'] },
} as const satisfies Record<string, NestedShape>;

/** The Studio plane over HTTP. */
export function liveStudioPlane(_http: HttpClient): StudioPlane {
  return {
    draftEvaluator: rejectNotImplemented(),
    testEvaluator: rejectNotImplemented(),
    getStudioSamples: rejectNotImplemented(),
    quickCalibrate: rejectNotImplemented(),
    dryRunEvaluator: rejectNotImplemented(),
  };
}

/** A Studio plane that serves nothing (the static build, tests): every call rejects with 404 `not_implemented`. */
export function unavailableStudioPlane(message?: string): StudioPlane {
  const reject = rejectNotImplemented(message);
  return {
    draftEvaluator: reject,
    testEvaluator: reject,
    getStudioSamples: reject,
    quickCalibrate: reject,
    dryRunEvaluator: reject,
  };
}
