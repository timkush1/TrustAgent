/**
 * msw handlers and state for the Studio plane (data/control/studioPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-ST: the state is seeded from the golden fixtures
 * (contracts/fixtures/api), and the dry run streams like the gateway does (SPEC §2.4 #38): a 409
 * `confirmation_required` before the stream when the first call lacks `confirm_cost` (D82), then
 * `estimate`, `progress` and `done` events, or a stream held open so a test can abort it.
 */
import { http, HttpResponse, type HttpHandler } from 'msw';
import confirmFixture from '@contracts/fixtures/api/ApiErrorBody.confirmation-required-dry-run.json';
import dryRunFixture from '@contracts/fixtures/api/DryRunResult.done.json';
import quickCalibrationFixture from '@contracts/fixtures/api/QuickCalibration.unstable.json';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import samplesFixture from '@contracts/fixtures/api/StudioSamplesResponse.flagged.json';
import testFixture from '@contracts/fixtures/api/StudioTestResponse.six.json';
import type { DryRunResult, QuickCalibration, StudioDraftResponse, StudioDryRunEvent, StudioSamplesResponse, StudioTestResponse } from '@/types/api';
import { API } from '../msw';
import { errorReply, isError, reply, type ErrorReply, type HandlerKit, type Reply } from './kit';

type DryRunData<E extends StudioDryRunEvent['event']> = Extract<StudioDryRunEvent, { event: E }>['data'];

export interface StudioState {
  /** POST /api/studio/draft (#34). */
  studioDraft: Reply<StudioDraftResponse>;
  /** POST /api/studio/test (#35). */
  studioTest: Reply<StudioTestResponse>;
  /** POST /api/studio/samples (#36). */
  studioSamples: Reply<StudioSamplesResponse>;
  /** POST /api/studio/calibrate (#37). */
  quickCalibration: Reply<QuickCalibration>;
  /** The `done` result of POST /api/studio/dry-run (#38), or an error answered before the stream. */
  studioDryRun: Reply<DryRunResult>;
  /** What a dry run without `confirm_cost` answers first (D82); null streams at once. */
  studioDryRunConfirm: ErrorReply | null;
  /** The `estimate` event the stream opens with. */
  studioDryRunEstimate: DryRunData<'estimate'>;
  /** `progress` events sent before `done`. */
  studioDryRunProgress: DryRunData<'progress'>[];
  /** Keep the stream open after the progress events (no `done`), so a test can cancel it. */
  studioDryRunHold: boolean;
  /** An `error` event (ApiErrorBody) to end the stream with instead of `done`. */
  studioDryRunStreamError: { error: { code: string; message: string } } | null;
}

/** The 409 the gateway answers a first dry run above STUDIO_CONFIRM_ABOVE_USD (fixture, D82 details). */
export function dryRunConfirmation(): ErrorReply {
  const { code, message, details } = confirmFixture.error;
  return errorReply(409, code, message, undefined, { ...details });
}

export function defaultStudioState(): StudioState {
  const done = structuredClone(dryRunFixture) as DryRunResult;
  return {
    studioDraft: structuredClone(draftFixture) as StudioDraftResponse,
    studioTest: structuredClone(testFixture) as StudioTestResponse,
    studioSamples: structuredClone(samplesFixture) as StudioSamplesResponse,
    quickCalibration: structuredClone(quickCalibrationFixture) as QuickCalibration,
    studioDryRun: done,
    studioDryRunConfirm: dryRunConfirmation(),
    studioDryRunEstimate: { est_cost_usd: 0.0021, sampled: done.sampled, population: done.population },
    studioDryRunProgress: [
      { evaluated: 100, fired: 5, cost_usd: 0.00041 },
      { evaluated: 200, fired: 11, cost_usd: 0.00082 },
    ],
    studioDryRunHold: false,
    studioDryRunStreamError: null,
  };
}

function sse(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

/** The dry run's event stream: the blocks are sent one by one; `hold` leaves it open until cancelled. */
function dryRunStream(state: StudioState, done: DryRunResult): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  const blocks = [sse('estimate', state.studioDryRunEstimate), ...state.studioDryRunProgress.map((p) => sse('progress', p))];
  if (!state.studioDryRunHold) {
    blocks.push(state.studioDryRunStreamError ? sse('error', state.studioDryRunStreamError) : sse('done', done));
  }
  let i = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (i < blocks.length) {
        controller.enqueue(encoder.encode(blocks[i]));
        i += 1;
        return;
      }
      if (!state.studioDryRunHold) {
        controller.close();
        return;
      }
      // Held open: the client cancels the body when it aborts.
      return new Promise<void>(() => {});
    },
  });
}

export function studioHandlers(state: StudioState, kit: HandlerKit): HttpHandler[] {
  return [
    http.post(`${API}/api/studio/draft`, async ({ request }) => {
      await kit.record(request);
      return reply(state.studioDraft);
    }),
    http.post(`${API}/api/studio/test`, async ({ request }) => {
      const { body } = await kit.record(request);
      const samples = (body as { samples?: unknown[] } | undefined)?.samples ?? [];
      if (samples.length > 50) return reply(errorReply(422, 'invalid_request', 'Test at most 50 samples at a time.'));
      return reply(state.studioTest);
    }),
    http.post(`${API}/api/studio/samples`, async ({ request }) => {
      await kit.record(request);
      return reply(state.studioSamples);
    }),
    http.post(`${API}/api/studio/calibrate`, async ({ request }) => {
      await kit.record(request);
      return reply(state.quickCalibration);
    }),
    http.post(`${API}/api/studio/dry-run`, async ({ request }) => {
      const { body } = await kit.record(request);
      const confirmed = (body as { confirm_cost?: unknown } | undefined)?.confirm_cost === true;
      if (state.studioDryRunConfirm && !confirmed) return reply(state.studioDryRunConfirm);
      const done = state.studioDryRun;
      if (done === undefined || isError(done)) return reply(done);
      return new HttpResponse(dryRunStream(state, done), { headers: { 'Content-Type': 'text/event-stream' } });
    }),
  ];
}
