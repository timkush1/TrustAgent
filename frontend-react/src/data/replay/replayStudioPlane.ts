/**
 * The static build's Studio plane (D107): what Evaluator Studio can do with no gateway. Owned by
 * WEB-STUDIO (SS-2): draft through the browser key (D108), test through runGate on a one-evaluator
 * route, samples from the tape, local quick calibration; the dry run stays unavailable.
 *
 * Step 0b stub: every action is unavailable, as before (the message says how to run the stack);
 * replayDataSource.ts already wires it in, so SS-2 changes only this file.
 */
import type { TraceDetail } from '@/types/api';
import { unavailableStudioPlane, type StudioPlane } from '../control/studioPlane';

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
  return unavailableStudioPlane(deps.message);
}
