/**
 * The static build's evaluator reads (D107), owned by WEB-STUDIO: the built-in templates as the
 * library, with the routes each runs on from the recording's policy (lib/studio/staticRegistry.ts,
 * loaded the first time a page asks, so the templates stay out of the replay's own chunk).
 */
import type { ControlPlane } from '../controlPlane';
import type { ReplayPlaneDeps } from './replayStudioPlane';

export type ReplayEvaluatorReads = Pick<ControlPlane, 'listEvaluators' | 'getEvaluator' | 'getEvaluatorVersion'>;

export function replayEvaluatorReads(_deps: ReplayPlaneDeps): ReplayEvaluatorReads {
  let reads: Promise<ReplayEvaluatorReads> | null = null;
  const load = () => (reads ??= import('@/lib/studio/staticRegistry').then((m) => m.createStaticEvaluatorReads(m.sharedStaticRegistry())));
  return {
    listEvaluators: async (params, signal) => (await load()).listEvaluators(params, signal),
    getEvaluator: async (id, signal) => (await load()).getEvaluator(id, signal),
    getEvaluatorVersion: async (id, version, signal) => (await load()).getEvaluatorVersion(id, version, signal),
  };
}
