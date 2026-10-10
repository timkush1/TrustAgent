/**
 * The static build's Studio plane (D107): what Evaluator Studio can do with no gateway. Owned by
 * WEB-STUDIO (SS-2).
 *
 * - `draftEvaluator`: the browser drafter (lib/studio/drafter.ts, D108) through the tab's own
 *   OpenRouter key and the judge model of this build; the draft is checked as the gateway checks
 *   one (evaluator-draft.v1 and the lints, the built-ins as the registry) and returned, never saved.
 * - `testEvaluator`: each sample through `runGate` (D135) on a one-evaluator route, the unsaved
 *   spec compiled as latest + 1 (gstudio CompileUnsaved), with the tab's decision-model client.
 * - `getStudioSamples`: up to six distinct answers from the replayed recording.
 * - `quickCalibrate`: the TS mirror of `POST /api/studio/calibrate` (free, pure).
 * - `dryRunEvaluator`: unavailable ("Test on past traffic" needs stored traffic and a gateway).
 *
 * Every call to OpenRouter reserves a slot of the tab's caps first (D101); a refusal is an
 * ApiError whose code is the ledger's reason (`cap_reached`, `token_cap`, `rejected_pause`).
 * The transports and caps come from lib/studio/live.ts at the moment an action runs.
 */
import { policy as recordedPolicy } from 'virtual:trustagent/policy';
import { quickCalibrate } from '@/lib/decision';
import { GateRefusedError, runGate } from '@/lib/gate/runner';
import type { PolicyInput } from '@/lib/gate/types';
import { checkSpecDocument, isSavable } from '@/lib/evaluator/checkSpec';
import { draftEvaluator, DraftRefusedError } from '@/lib/studio/drafter';
import { studioLive, type StudioLive } from '@/lib/studio/live';
import { matchesSampleFilter, sampleKey, sampleOfTrace, sampleOrder, STATIC_SAMPLE_LIMIT } from '@/lib/studio/staticSamples';
import {
  combinedCost,
  compileUnsaved,
  errorRow,
  failureReason,
  liveCallCost,
  resultRow,
  sampleRequest,
  studioTestRoute,
  testSummary,
  type CostSource,
  type TestRow,
} from '@/lib/studio/staticTest';
import type { CallCost } from '@/lib/decision/cost';
import type { LedgerRefusal } from '@/lib/openrouter/types';
import type { EvaluatorSpec, FieldError, QuickCalibration, StudioDraftResponse, StudioSample, StudioTestResponse, TraceDetail } from '@/types/api';
import { STUDIO_DRAFT_MAX_CHARS, STUDIO_TEST_MAX_SAMPLES, type StudioPlane } from '../control/studioPlane';
import { rejectNotImplemented } from '../control/shared';
import { ApiError, isApiError } from '../errors';
import { staticRegistry, type StaticRegistry } from './replayEvaluatorReads';

/** What the replay planes may read (fixed: replayDataSource.ts builds it). */
export interface ReplayPlaneDeps {
  /** The text an unavailable action rejects with (it says how to run the stack). */
  message: string;
  /** Every trace id of the tape, oldest first, once the recording has loaded. */
  traceIds: () => Promise<readonly string[]>;
  /** One trace with its body (the recording's traces/<id>.json, as the replay shows it now). */
  trace: (id: string, signal?: AbortSignal) => Promise<TraceDetail>;
}

export interface ReplayStudioOptions {
  /** The tab's transports and caps (default: this build's, lib/studio/live.ts). */
  live?: StudioLive;
  /** The built-in library and the policy (default: the recording's). */
  registry?: StaticRegistry;
  /** The policy whose defaults a test runs with (default: the recording's). */
  policy?: PolicyInput;
  /** For the `random` sample filter (default Math.random). */
  random?: () => number;
}

/** Samples of a test run at the same time, as the gateway runs them in small parallel batches. */
export const TEST_CONCURRENCY = 3;

/** Transport failures that end the whole test: every other sample would fail the same way. */
const FATAL_CODES = new Set(['auth', 'credits', 'data_policy', 'network', 'aborted']);

const costSources = new WeakMap<StudioTestResponse, CostSource>();

/**
 * What a static test's cost is (D101): `reported` when OpenRouter reported every call's cost
 * (vendor), `computed` for the price table's figure (estimate), `unknown` when a call had no price.
 * Undefined for a response this plane did not produce.
 */
export function testCostSource(response: StudioTestResponse): CostSource | undefined {
  return costSources.get(response);
}

function invalid(message: string, ...fieldErrors: FieldError[]): ApiError {
  return new ApiError({ kind: 'http', status: 422, code: 'validation_failed', message, fieldErrors });
}

/** The caps refused before any call (D101): the code is the ledger's reason. */
export function refusedError(reason: LedgerRefusal): ApiError {
  return new ApiError({ kind: 'http', status: 429, code: reason, message: `This tab's limits for calls to OpenRouter refused the request (${reason}).`, details: { reason } });
}

function unavailable(code: 'no_judge' | 'decision_model_unavailable', message: string): ApiError {
  return new ApiError({ kind: 'http', status: 503, code, message });
}

function fatal(err: unknown): boolean {
  return err instanceof GateRefusedError || (isApiError(err) && FATAL_CODES.has(err.code));
}

/** Runs `work` over every item, at most `limit` at a time, taking no new item once `signal` aborts. */
async function runPool<T>(items: readonly T[], limit: number, signal: AbortSignal, work: (item: T, index: number) => Promise<void>): Promise<void> {
  let next = 0;
  const worker = async () => {
    while (next < items.length && !signal.aborted) {
      const index = next;
      next += 1;
      await work(items[index], index);
    }
  };
  const settled = await Promise.allSettled(Array.from({ length: Math.min(limit, items.length) }, worker));
  const failed = settled.find((s): s is PromiseRejectedResult => s.status === 'rejected');
  if (failed) throw failed.reason;
}

function checkSamples(samples: readonly StudioSample[]): FieldError[] {
  const errors: FieldError[] = [];
  if (samples.length === 0 || samples.length > STUDIO_TEST_MAX_SAMPLES) {
    errors.push({ path: 'samples', message: `1 to ${STUDIO_TEST_MAX_SAMPLES} samples` });
  }
  const ids = new Set<string>();
  samples.forEach((s, i) => {
    if (!s.id || ids.has(s.id)) errors.push({ path: `samples[${i}].id`, message: 'a unique id is required' });
    ids.add(s.id);
    if (typeof s.assistant_response !== 'string' || s.assistant_response.trim() === '') {
      errors.push({ path: `samples[${i}].assistant_response`, message: 'the answer to check is required' });
    }
  });
  return errors;
}

let runs = 0;

export function createReplayStudioPlane(deps: ReplayPlaneDeps, options: ReplayStudioOptions = {}): StudioPlane {
  const live = options.live ?? studioLive();
  const registry = options.registry ?? staticRegistry();
  const policy = options.policy ?? recordedPolicy;
  const random = options.random ?? Math.random;

  return {
    async draftEvaluator(request, signal): Promise<StudioDraftResponse> {
      const description = request.description.trim();
      if (description === '' || Array.from(request.description).length > STUDIO_DRAFT_MAX_CHARS) {
        throw invalid(`Describe what to check in 1 to ${STUDIO_DRAFT_MAX_CHARS} characters.`, { path: 'description', message: `1 to ${STUDIO_DRAFT_MAX_CHARS} characters` });
      }
      const route = request.route ?? '';
      if (route !== '' && !registry.routes().includes(route)) {
        throw invalid(`No route named ${route} in the recording's policy.`, { path: 'route', message: 'unknown route' });
      }
      const chat = live.chat();
      if (!live.drafting || chat === null) throw unavailable('no_judge', 'Draft with AI calls OpenRouter with your key from this tab. Connect or paste a key first.');

      const existingIds = registry.ids();
      let run;
      try {
        run = await draftEvaluator(
          { description, routeName: route, routeDescription: route === '' ? '' : registry.routeDescription(route), existingIds },
          { chat, ledger: live.ledger(), model: live.judgeModel, signal },
        );
      } catch (err) {
        if (err instanceof DraftRefusedError) throw refusedError(err.reason);
        throw err;
      }
      const cost = combinedCost(run.calls.map((c) => liveCallCost(c.usage, live.judgeModel)));
      const out: StudioDraftResponse = { draft: null, errors: [], lints: [], model: run.model, cost_usd: cost.cost_usd, cost_source: cost.cost_source, attempts: run.attempts };
      if (run.document === null) {
        out.errors = run.errors.length > 0 ? run.errors.map((message) => ({ path: '', message })) : [{ path: '', message: 'The drafter returned no draft.' }];
        return out;
      }
      // The gateway's own check of a draft (Go ValidateSpec with Draft): schema errors keep it out.
      const check = checkSpecDocument(run.document, { draft: true, registry: existingIds });
      out.errors = check.errors.map((e) => ({ path: e.path, message: e.message }));
      out.lints = check.lints;
      if (out.errors.length === 0) out.draft = run.document as unknown as StudioDraftResponse['draft'];
      return out;
    },

    async testEvaluator(request, signal): Promise<StudioTestResponse> {
      const sampleErrors = checkSamples(request.samples);
      if (sampleErrors.length > 0) throw invalid('The samples are not valid.', ...sampleErrors);
      let doc: EvaluatorSpec | undefined = request.spec;
      if (doc === undefined && request.ref) {
        const at = request.ref.lastIndexOf('@');
        doc = registry.version(request.ref.slice(0, at), Number(request.ref.slice(at + 1)))?.spec;
        if (doc === undefined) throw invalid(`No saved evaluator version ${request.ref}.`, { path: 'ref', message: 'unknown evaluator version' });
      }
      if (doc === undefined) throw invalid('Give the spec to test or the ref of a saved version.', { path: 'spec', message: 'spec or ref is required' });
      const check = checkSpecDocument(doc, { registry: registry.ids(), thresholds: request.thresholds });
      if (!isSavable(check)) {
        throw invalid('The spec is not valid.', ...check.errors.map((e) => ({ path: e.path, message: e.message })), ...check.lints.filter((l) => l.severity === 'error').map((l) => ({ path: l.path, message: l.message, code: l.code })));
      }
      const jev = live.jev();
      if (!live.checks || jev === null) throw unavailable('decision_model_unavailable', 'Testing a check calls OpenRouter with your key from this tab. Connect or paste a key first.');

      const unsaved = compileUnsaved(doc, registry.latest(doc.id));
      const specs = registry.specs();
      specs.set(unsaved.ref, unsaved.spec);
      const route = studioTestRoute(policy, unsaved.ref, request.thresholds);
      const ledger = live.ledger();
      const stop = new AbortController();
      const onAbort = () => stop.abort(signal?.reason);
      signal?.addEventListener('abort', onAbort, { once: true });
      if (signal?.aborted) onAbort();

      const rows: TestRow[] = new Array<TestRow>(request.samples.length);
      const costs: CallCost[] = [];
      const snapshots: (string | null)[] = new Array<string | null>(request.samples.length).fill(null);
      // The first failure that ends the test (the others it aborts only follow from it).
      let ended: unknown = null;
      try {
        await runPool(request.samples, TEST_CONCURRENCY, stop.signal, async (sample, index) => {
          const started = Date.now();
          try {
            const result = await runGate({
              route,
              specs,
              request: sampleRequest(sample),
              response: sample.assistant_response,
              apiFormat: 'openai_chat',
              call: jev,
              ledger,
              maxTokens: live.maxTokens,
              signal: stop.signal,
            });
            const sampleCosts = result.calls.flatMap((c) => (c === null ? [] : [liveCallCost(c.usage, jev.model)]));
            costs.push(...sampleCosts);
            rows[index] = resultRow(result, unsaved.ref, sample, combinedCost(sampleCosts).cost_usd);
            snapshots[index] = result.snapshot;
          } catch (err) {
            if (!fatal(err)) {
              rows[index] = errorRow(sample, failureReason(err), Math.max(0, Date.now() - started));
              return;
            }
            if (ended === null) ended = err;
            stop.abort(err);
          }
        });
      } finally {
        signal?.removeEventListener('abort', onAbort);
      }
      if (signal?.aborted) throw new ApiError({ kind: 'aborted', status: 0, code: 'aborted', message: 'The test was cancelled.', cause: signal.reason });
      if (ended instanceof GateRefusedError) throw refusedError(ended.reason);
      if (ended !== null) throw ended;

      const total = combinedCost(costs);
      runs += 1;
      const response: StudioTestResponse = {
        run_id: `static_test_${runs}`,
        results: rows,
        summary: testSummary(rows, total.cost_usd, snapshots.filter((s): s is string => s !== null).at(-1) ?? null, jev.backend),
      };
      costSources.set(response, total.cost_source);
      return response;
    },

    async getStudioSamples(request, signal) {
      const limit = request.limit ?? STATIC_SAMPLE_LIMIT;
      if (!Number.isInteger(limit) || limit < 1 || limit > 50) throw invalid('The samples request is not valid.', { path: 'limit', message: 'must be between 1 and 50' });
      const route = request.route ?? '';
      if (route !== '' && !registry.routes().includes(route)) throw invalid(`No route named ${route} in the recording's policy.`, { path: 'route', message: 'unknown route' });
      const wanted = Math.min(limit, STATIC_SAMPLE_LIMIT);
      const samples: StudioSample[] = [];
      const seen = new Set<string>();
      for (const id of sampleOrder(await deps.traceIds(), request.filter, random)) {
        if (samples.length >= wanted) break;
        signal?.throwIfAborted();
        let detail: TraceDetail;
        try {
          detail = await deps.trace(id, signal);
        } catch (err) {
          if (isApiError(err) && err.code === 'not_found') continue;
          throw err;
        }
        if ((route !== '' && detail.route !== route) || !matchesSampleFilter(detail, request.filter)) continue;
        const sample = sampleOfTrace(detail);
        if (sample === null) continue;
        const key = sampleKey(sample);
        if (seen.has(key)) continue;
        seen.add(key);
        samples.push(sample);
      }
      return { samples };
    },

    async quickCalibrate(request): Promise<QuickCalibration> {
      try {
        return quickCalibrate(request) as QuickCalibration;
      } catch (err) {
        throw invalid(`The labels are not valid: ${(err as Error).message}`, { path: 'labeled', message: (err as Error).message });
      }
    },

    dryRunEvaluator: rejectNotImplemented(deps.message),
  };
}

export function replayStudioPlane(deps: ReplayPlaneDeps): StudioPlane {
  return createReplayStudioPlane(deps);
}
