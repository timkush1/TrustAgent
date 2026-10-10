/**
 * A policy.v1 route as the browser gate runs it (D135): the route merged over the policy's
 * `defaults`, with the policy.v1 schema defaults last, and the evaluator specs by ref. Extracted
 * from the demo recorder unchanged, so the simulated tape stays byte-identical
 * (`make recording-check`). Owned by WEB-LIVE.
 */
import type { EvaluatorSpec } from '@/lib/contracts/types.gen';
import { parseSpec, specRef, type Spec } from '@/lib/decision/spec';
import type { DetectorId, EvidenceSource, PolicyMode } from '@/types/api';
import type { PolicyInput, ResolvedRoute, RouteEvaluatorRef } from './types';

/** The detector defaults of policy.v1 DetectorSettings. */
export const DEFAULT_FLAG_ON: readonly DetectorId[] = [
  'urls_not_in_evidence',
  'quotes_not_in_evidence',
  'citation_out_of_range',
  'secret_pattern',
  'system_prompt_leak',
];
export const DEFAULT_ROUTE_SOURCES: readonly EvidenceSource[] = ['request_system', 'request_context', 'tool_results', 'extension'];

/** A route the policy does not have. */
export class RouteError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'RouteError';
  }
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Nested settings merge key by key, like the gateway merges a route over its defaults. */
export function mergeSettings(base: unknown, over: unknown): Obj {
  const out: Obj = isObj(base) ? { ...base } : {};
  if (!isObj(over)) return out;
  for (const [k, v] of Object.entries(over)) out[k] = isObj(v) && isObj(out[k]) ? mergeSettings(out[k], v) : v;
  return out;
}

/** A route's effective settings: the route merged over `defaults` (policy.v1 schema defaults last). */
export function resolveRoute(policy: PolicyInput, name: string): ResolvedRoute {
  const route = policy.routes?.find((r) => r.name === name);
  if (!route) throw new RouteError(`the policy has no route ${JSON.stringify(name)}`);
  const s = mergeSettings(policy.defaults ?? {}, route);
  const evidence = mergeSettings({ sources: DEFAULT_ROUTE_SOURCES }, s.evidence) as unknown as ResolvedRoute['evidence'];
  const detectors = mergeSettings({ flag_on: DEFAULT_FLAG_ON, block_on: [], min_integer: 10 }, s.detectors) as unknown as ResolvedRoute['detectors'];
  const deepAudit = mergeSettings({ pass_sample_rate: 0.02, uniform_sample_rate: 0.01 }, s.deep_audit) as unknown as ResolvedRoute['deepAudit'];
  const overrides = isObj(policy.overrides) ? policy.overrides : {};
  return {
    name,
    mode: (route.mode as PolicyMode | undefined) ?? (s.mode as PolicyMode | undefined) ?? 'observe',
    fail: (s.fail as 'open' | 'closed' | undefined) ?? 'open',
    requireCalibration: (s.require_calibration as boolean | undefined) ?? true,
    onNoEvidence: (s.on_no_evidence as ResolvedRoute['onNoEvidence'] | undefined) ?? 'ignore',
    evidence,
    detectors,
    evaluators: (Array.isArray(route.evaluators) ? route.evaluators : (s.evaluators as RouteEvaluatorRef[] | undefined) ?? []) as RouteEvaluatorRef[],
    deepAudit,
    clientRules: overrides.client_rules === true,
  };
}

/** Evaluator ref → parsed spec. */
export function specMap(templates: readonly EvaluatorSpec[]): Map<string, Spec> {
  const out = new Map<string, Spec>();
  for (const doc of templates) {
    const spec = parseSpec(doc);
    out.set(specRef(spec), spec);
  }
  return out;
}
