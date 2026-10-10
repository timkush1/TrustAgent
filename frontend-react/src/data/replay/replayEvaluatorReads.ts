/**
 * The static build's evaluator reads (D107): the built-in templates as the evaluator list, with
 * route names from `virtual:trustagent/policy`. Owned by WEB-STUDIO (SS-2).
 *
 * Step 0b stub: unavailable, as before (the message says how to run the stack); replayDataSource.ts
 * already wires it in, so SS-2 changes only this file.
 */
import { unavailableControlPlane, type ControlPlane } from '../controlPlane';
import type { ReplayPlaneDeps } from './replayStudioPlane';

export type ReplayEvaluatorReads = Pick<ControlPlane, 'listEvaluators' | 'getEvaluator' | 'getEvaluatorVersion'>;

export function replayEvaluatorReads(deps: ReplayPlaneDeps): ReplayEvaluatorReads {
  const { listEvaluators, getEvaluator, getEvaluatorVersion } = unavailableControlPlane(deps.message);
  return { listEvaluators, getEvaluator, getEvaluatorVersion };
}
