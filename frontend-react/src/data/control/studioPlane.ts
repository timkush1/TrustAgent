/**
 * The Studio plane of the control plane (SPEC §2.4 #34-#38, §7.3; D58, D82): draft with
 * AI, the test bench, samples from past traffic, quick calibration and "Test on past traffic"
 * (an SSE dry run that is cancelled by aborting the request).
 *
 * Step 0b declared the signatures (fixed); WEB-ST owns the bodies and
 * STUDIO_SHAPES. Every response is shape-checked before a page sees it, and the dry run's events
 * are read with the shared SSE reader (data/sse.ts), the same one the Playground's stream uses.
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
import { ApiError, apiErrorFromResponse, isP4ErrorCode, P4_ERROR_CODES } from '../errors';
import type { HttpClient } from '../http';
import { normalizeShaped, type NestedShape } from '../normalize';
import { readSse } from '../sse';
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

/** At most this many samples per test (SPEC §2.4 #35); the server refuses more with 422. */
export const STUDIO_TEST_MAX_SAMPLES = 50;
/** At most this many samples per "Test on past traffic" (TRUTHTABLE_DRYRUN_MAX_SAMPLES default, SPEC §7.3). */
export const STUDIO_DRY_RUN_MAX_SAMPLES = 200;
/** At most this many characters of description for a draft (SPEC §2.4 #34). */
export const STUDIO_DRAFT_MAX_CHARS = 2000;

/** A draft is one judge call plus at most one repair turn. */
const DRAFT_TIMEOUT_MS = 120_000;
/** Up to 50 fast-check calls, in parallel batches on the server. */
const TEST_TIMEOUT_MS = 120_000;
/** The dry run's headers come once the server has its estimate (the 409 is decided before the stream). */
const DRY_RUN_HEADERS_TIMEOUT_MS = 60_000;

const WHAT = {
  draft: 'POST /api/studio/draft',
  test: 'POST /api/studio/test',
  samples: 'POST /api/studio/samples',
  calibrate: 'POST /api/studio/calibrate',
  dryRun: 'POST /api/studio/dry-run',
} as const;

function invalidResponse(what: string, message: string): ApiError {
  return new ApiError({ kind: 'invalid_response', status: 200, code: 'invalid_response', message: `${what}: ${message}` });
}

function cancelled(cause?: unknown): ApiError {
  return new ApiError({ kind: 'aborted', status: 0, code: 'aborted', message: 'The test was cancelled.', cause });
}

/** The event's data with every listed key a finite number, or an invalid_response error. */
function numbers<T>(payload: unknown, keys: readonly string[], what: string, event: string): T {
  const ok =
    typeof payload === 'object' &&
    payload !== null &&
    keys.every((k) => typeof (payload as Record<string, unknown>)[k] === 'number' && Number.isFinite((payload as Record<string, number>)[k]));
  if (!ok) throw invalidResponse(what, `the ${event} event is missing ${keys.join(', ')}.`);
  return payload as T;
}

/** The HTTP status an `error` event's code has on the REST API (`budget_exhausted` → 503), else 502. */
function errorEventStatus(payload: unknown): number {
  const error = typeof payload === 'object' && payload !== null ? (payload as { error?: unknown }).error : undefined;
  const code = typeof error === 'object' && error !== null ? (error as { code?: unknown }).code : undefined;
  return typeof code === 'string' && isP4ErrorCode(code) ? P4_ERROR_CODES[code].status : 502;
}

/** Reads a dry-run stream to its `done` event, reporting `estimate` and `progress` on the way. */
async function readDryRun(res: Response, handlers: StudioDryRunHandlers, signal: AbortSignal | undefined): Promise<DryRunResult> {
  const what = WHAT.dryRun;
  // A proxy that ran the whole test before answering may send the result as one JSON body.
  if (!(res.headers.get('content-type') ?? '').includes('text/event-stream') || !res.body) {
    let raw: unknown;
    try {
      raw = (await res.json()) as unknown;
    } catch {
      raw = undefined;
    }
    return normalizeShaped(raw, what, STUDIO_SHAPES.dryRun);
  }
  try {
    for await (const item of readSse(res.body, signal)) {
      if (item.kind === 'comment') continue;
      const { event, data } = item.message;
      let payload: unknown;
      try {
        payload = JSON.parse(data);
      } catch {
        throw invalidResponse(what, `the ${event} event is not JSON.`);
      }
      switch (event) {
        case 'estimate':
          handlers.onEstimate?.(numbers(payload, ['est_cost_usd', 'sampled', 'population'], what, event));
          break;
        case 'progress':
          handlers.onProgress?.(numbers(payload, ['evaluated', 'fired', 'cost_usd'], what, event));
          break;
        case 'done':
          return normalizeShaped(payload, what, STUDIO_SHAPES.dryRun);
        case 'error':
          // The body of an `error` event is the REST error body (ApiErrorBody). The stream itself was a
          // 200, so the error takes the status its code has on the REST API (502 for any other code).
          throw apiErrorFromResponse(errorEventStatus(payload), payload);
        default:
          break;
      }
    }
  } catch (err) {
    // Aborting cancels the body under the reader, which surfaces as a DOMException.
    if (signal?.aborted && !(err instanceof ApiError)) throw cancelled(err);
    throw err;
  }
  if (signal?.aborted) throw cancelled();
  throw invalidResponse(what, 'the stream ended without a result. The proxy may be an older version.');
}

/** The Studio plane over HTTP. */
export function liveStudioPlane(http: HttpClient): StudioPlane {
  return {
    async draftEvaluator(request, signal) {
      const raw = await http.request<unknown>('/api/studio/draft', { method: 'POST', body: request, signal, timeoutMs: DRAFT_TIMEOUT_MS });
      return normalizeShaped<StudioDraftResponse>(raw, WHAT.draft, STUDIO_SHAPES.draft);
    },
    async testEvaluator(request, signal) {
      const raw = await http.request<unknown>('/api/studio/test', { method: 'POST', body: request, signal, timeoutMs: TEST_TIMEOUT_MS });
      return normalizeShaped<StudioTestResponse>(raw, WHAT.test, STUDIO_SHAPES.test);
    },
    async getStudioSamples(request, signal) {
      const raw = await http.request<unknown>('/api/studio/samples', { method: 'POST', body: request, signal });
      return normalizeShaped<StudioSamplesResponse>(raw, WHAT.samples, STUDIO_SHAPES.samples);
    },
    async quickCalibrate(request, signal) {
      const raw = await http.request<unknown>('/api/studio/calibrate', { method: 'POST', body: request, signal });
      return normalizeShaped<QuickCalibration>(raw, WHAT.calibrate, STUDIO_SHAPES.quickCalibration);
    },
    async dryRunEvaluator(request, handlers, signal) {
      // The 409 `confirmation_required` and 503 `budget_exhausted` arrive before the stream, as ApiErrors.
      const res = await http.stream('/api/studio/dry-run', { method: 'POST', body: request, signal, timeoutMs: DRY_RUN_HEADERS_TIMEOUT_MS });
      return readDryRun(res, handlers, signal);
    },
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
