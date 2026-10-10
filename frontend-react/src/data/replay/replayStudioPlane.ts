/**
 * The static build's Studio plane (D107), owned by WEB-STUDIO: drafting through the tab's own
 * OpenRouter key, the test bench through the browser gate (runGate), samples from the tape and the
 * local quick calibration; the dry run stays unavailable. The work lives in
 * lib/studio/staticPlane.ts, loaded the first time a Studio action runs, so the drafter, its prompt,
 * the validators and the gate never weigh on the replay's own chunk.
 */
import type { TraceDetail } from '@/types/api';
import type { StudioPlane } from '../control/studioPlane';

/** What the replay planes may read (fixed: replayDataSource.ts builds it). */
export interface ReplayPlaneDeps {
  /** The text an unavailable action rejects with (it says how to run the stack). */
  message: string;
  /** Every trace id of the tape, oldest first, once the recording has loaded. */
  traceIds: () => Promise<readonly string[]>;
  /** One trace with its body (the recording's traces/<id>.json, as the replay shows it now). */
  trace: (id: string, signal?: AbortSignal) => Promise<TraceDetail>;
}

export function replayStudioPlane(deps: ReplayPlaneDeps): StudioPlane {
  let plane: Promise<StudioPlane> | null = null;
  const load = () => (plane ??= import('@/lib/studio/staticPlane').then((m) => m.createStaticStudioPlane(deps)));
  return {
    draftEvaluator: async (request, signal) => (await load()).draftEvaluator(request, signal),
    testEvaluator: async (request, signal) => (await load()).testEvaluator(request, signal),
    getStudioSamples: async (request, signal) => (await load()).getStudioSamples(request, signal),
    quickCalibrate: async (request, signal) => (await load()).quickCalibrate(request, signal),
    dryRunEvaluator: async (request, handlers, signal) => (await load()).dryRunEvaluator(request, handlers, signal),
  };
}
