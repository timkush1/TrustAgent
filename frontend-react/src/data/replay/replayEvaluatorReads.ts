/**
 * The static build's evaluator reads (D107): the built-in templates are the library, as the
 * gateway's registry lists them before anyone saves a check (cmd/proxy evaluators_api.go `meta`,
 * `versionMeta`, `get`), and the routes each one runs on come from the recording's policy
 * (`virtual:trustagent/policy`, config/policies.yaml). Studio reads them to open a built-in
 * version (`/app/evaluators/studio/$id/$version` copies it as a new check), to keep a new check's
 * id clear of the library's, and to compile an unsaved spec as latest + 1. Owned by WEB-STUDIO.
 *
 * Nothing is stored: no labels, no calibrations, no saved versions. A built-in's times are the
 * moment the page loaded, as the gateway reports its start time for them.
 */
import { policy } from 'virtual:trustagent/policy';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { MEASURED_MIN_LABELS, MEASURED_MIN_PER_CLASS } from '@/lib/decision/calibrate';
import { parseRef, parseSpec, specHashes, type Spec } from '@/lib/decision/spec';
import type { PolicyInput } from '@/lib/gate/types';
import type { EvaluatorDetail, EvaluatorMeta, EvaluatorSpec, EvaluatorVersion } from '@/types/api';
import { ApiError } from '../errors';
import type { ControlPlane } from '../controlPlane';
import type { ReplayPlaneDeps } from './replayStudioPlane';

export type ReplayEvaluatorReads = Pick<ControlPlane, 'listEvaluators' | 'getEvaluator' | 'getEvaluatorVersion'>;

/** The route of policy.v1 `defaults` (Go policy.DefaultRouteName). */
export const DEFAULT_ROUTE = 'default';

export interface StaticRegistryOptions {
  /** The policy routes come from (default: the recording's). */
  policy?: PolicyInput;
  /** Template documents by ref (default: contracts/evaluators). */
  templates?: Readonly<Record<string, EvaluatorSpec>>;
  /** What a built-in's times read (default: when the registry was made). */
  started?: Date;
}

/** The evaluator registry of the static build: built-ins only, never archived. */
export interface StaticRegistry {
  /** Evaluator ids, sorted (the ids a draft may reference; Go existingIDs). */
  ids(): string[];
  /** The newest version of an id; 0 when the library has none (CompileUnsaved's latest). */
  latest(id: string): number;
  /** Every version by ref, parsed (compile's specs). */
  specs(): Map<string, Spec>;
  /** The policy's route names, the default route first (Studio's route pickers). */
  routes(): string[];
  /** A route's description from the policy ("" when it has none). */
  routeDescription(name: string): string;
  list(): EvaluatorMeta[];
  detail(id: string): EvaluatorDetail | null;
  version(id: string, version: number): EvaluatorVersion | null;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Every route of a policy, the default route (its `defaults`) last, as Go AllRoutes lists them. */
function allRoutes(p: PolicyInput): { name: string; route: Obj }[] {
  return [...(p.routes ?? []).map((r) => ({ name: r.name, route: r as Obj })), { name: DEFAULT_ROUTE, route: (p.defaults ?? {}) as Obj }];
}

export function staticRegistry(options: StaticRegistryOptions = {}): StaticRegistry {
  const p = options.policy ?? policy;
  const templates = options.templates ?? BUILTIN_TEMPLATES;
  const at = (options.started ?? new Date()).toISOString();
  const byId = new Map<string, EvaluatorSpec[]>();
  for (const doc of Object.values(templates)) byId.set(doc.id, [...(byId.get(doc.id) ?? []), doc]);
  for (const versions of byId.values()) versions.sort((a, b) => a.version - b.version);
  const ids = [...byId.keys()].sort();

  const attached = (id: string): EvaluatorMeta['attached_routes'] => {
    const out: EvaluatorMeta['attached_routes'] = [];
    for (const { name, route } of allRoutes(p)) {
      const refs = Array.isArray(route.evaluators) ? route.evaluators : [];
      const ref = refs.find((r): r is Obj => {
        if (!isObj(r) || typeof r.ref !== 'string' || r.enabled === false) return false;
        try {
          return parseRef(r.ref).id === id;
        } catch {
          return false;
        }
      });
      if (ref) out.push({ route: name, stage: ref.stage === 'shadow' ? 'shadow' : 'active' });
    }
    return out;
  };

  const meta = (id: string): EvaluatorMeta => {
    const versions = byId.get(id) ?? [];
    const latest = versions[versions.length - 1];
    return { id, name: latest.name, category: latest.category, origin: 'builtin', latest_version: latest.version, archived: false, attached_routes: attached(id), updated_at: at };
  };

  const versionOf = (doc: EvaluatorSpec): EvaluatorVersion => ({
    evaluator_id: doc.id,
    version: doc.version,
    spec: structuredClone(doc),
    ...specHashes(parseSpec(doc)),
    created_by: 'system',
    created_at: at,
    changelog: doc.changelog ?? '',
  });

  return {
    ids: () => [...ids],
    latest: (id) => byId.get(id)?.at(-1)?.version ?? 0,
    specs: () => new Map(Object.values(templates).map((doc) => [`${doc.id}@${doc.version}`, parseSpec(doc)])),
    routes: () => [DEFAULT_ROUTE, ...(p.routes ?? []).map((r) => r.name)],
    routeDescription: (name) => {
      const route = name === DEFAULT_ROUTE ? undefined : p.routes?.find((r) => r.name === name);
      return typeof route?.description === 'string' ? route.description.trim() : '';
    },
    list: () => ids.map(meta),
    detail: (id) => {
      const versions = byId.get(id);
      if (!versions) return null;
      const latest = versionOf(versions[versions.length - 1]);
      return {
        ...meta(id),
        versions: versions.map((doc) => {
          const { spec: _spec, ...rest } = versionOf(doc);
          return rest;
        }),
        latest,
        calibration_progress: {
          evaluator_ref: `${id}@${latest.version}`,
          snapshot: null,
          human_labels: { n: 0, pos: 0, neg: 0 },
          needed: { n: MEASURED_MIN_LABELS, pos: MEASURED_MIN_PER_CLASS, neg: MEASURED_MIN_PER_CLASS },
          proxy_labels: 0,
          state: 'none',
          can_fit_measured: false,
          can_fit_provisional: false,
        },
      };
    },
    version: (id, version) => {
      const doc = byId.get(id)?.find((d) => d.version === version);
      return doc ? versionOf(doc) : null;
    },
  };
}

function notFound(what: string): ApiError {
  return new ApiError({ kind: 'http', status: 404, code: 'not_found', message: `${what} is not one of the built-in checks this demo carries.` });
}

export function createReplayEvaluatorReads(registry: StaticRegistry): ReplayEvaluatorReads {
  return {
    async listEvaluators() {
      return { items: registry.list() };
    },
    async getEvaluator(id) {
      const detail = registry.detail(id);
      if (!detail) throw notFound(`“${id}”`);
      return detail;
    },
    async getEvaluatorVersion(id, version) {
      const found = registry.version(id, version);
      if (!found) throw notFound(`Version ${version} of “${id}”`);
      return found;
    },
  };
}

export function replayEvaluatorReads(_deps: ReplayPlaneDeps): ReplayEvaluatorReads {
  return createReplayEvaluatorReads(staticRegistry());
}
