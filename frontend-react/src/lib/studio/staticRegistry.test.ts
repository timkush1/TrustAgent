// @vitest-environment node
/**
 * The static build's evaluator reads (D107, SS-2): the built-in templates as the library, with the
 * routes each runs on from the recording's policy, details with the versions' hashes, and 404s for
 * anything the demo does not carry.
 */
import { describe, expect, it } from 'vitest';
import { policy } from 'virtual:trustagent/policy';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { parseSpec, specHashes } from '@/lib/decision/spec';
import type { PolicyInput } from '@/lib/gate/types';
import type { EvaluatorSpec } from '@/types/api';
import { isApiError } from '@/data/errors';
import { createStaticEvaluatorReads, DEFAULT_ROUTE, staticRegistry } from './staticRegistry';

const STARTED = new Date('2026-10-01T12:00:00Z');
const ids = [...new Set(Object.values(BUILTIN_TEMPLATES).map((t) => t.id))].sort();

function reads(options: Parameters<typeof staticRegistry>[0] = {}) {
  return createStaticEvaluatorReads(staticRegistry({ started: STARTED, ...options }));
}

describe('static evaluator reads', () => {
  it('lists every built-in once, sorted, as built-in and never archived', async () => {
    const { items } = await reads().listEvaluators({ include_archived: true });
    expect(items.map((e) => e.id)).toEqual(ids);
    for (const e of items) {
      expect(e).toMatchObject({ origin: 'builtin', archived: false, updated_at: STARTED.toISOString() });
      const latest = Object.values(BUILTIN_TEMPLATES)
        .filter((t) => t.id === e.id)
        .reduce((a, b) => (b.version > a.version ? b : a));
      expect(e.latest_version).toBe(latest.version);
      expect(e.name).toBe(latest.name);
    }
  });

  it('says which routes of the recorded policy run each check, the default route last', async () => {
    const { items } = await reads().listEvaluators({});
    const byId = new Map(items.map((e) => [e.id, e.attached_routes]));
    const expected = (id: string) => {
      const out: string[] = [];
      for (const r of [...(policy.routes ?? []), { name: DEFAULT_ROUTE, ...(policy.defaults ?? {}) }]) {
        const refs = (r as { evaluators?: { ref: string; enabled?: boolean }[] }).evaluators ?? [];
        if (refs.some((x) => x.ref.split('@')[0] === id && x.enabled !== false)) out.push(r.name);
      }
      return out;
    };
    expect(byId.get('groundedness')?.map((a) => a.route)).toEqual(expected('groundedness'));
    expect(byId.get('groundedness')?.at(-1)).toEqual({ route: DEFAULT_ROUTE, stage: 'active' });
    expect(byId.get('commitments')?.map((a) => a.route)).toEqual(expected('commitments'));
    expect(expected('commitments').length).toBeGreaterThan(0);
  });

  it('reads stages and skips disabled refs', async () => {
    const small: PolicyInput = {
      version: 1,
      defaults: { evaluators: [{ ref: 'refusal@1' }] },
      routes: [
        { name: 'a', evaluators: [{ ref: 'refusal@1', stage: 'shadow' }] },
        { name: 'b', evaluators: [{ ref: 'refusal@1', enabled: false }] },
      ],
    };
    const { items } = await reads({ policy: small }).listEvaluators({});
    expect(items.find((e) => e.id === 'refusal')?.attached_routes).toEqual([
      { route: 'a', stage: 'shadow' },
      { route: DEFAULT_ROUTE, stage: 'active' },
    ]);
    expect(items.find((e) => e.id === 'off_topic')?.attached_routes).toEqual([]);
  });

  it('details a check: its versions, the latest spec with the hashes the gateway computes, no labels yet', async () => {
    const detail = await reads().getEvaluator('refusal');
    const doc = BUILTIN_TEMPLATES['refusal@1'];
    expect(detail.latest.spec).toEqual(doc);
    expect(detail.latest).toMatchObject({ evaluator_id: 'refusal', version: 1, created_by: 'system', created_at: STARTED.toISOString(), ...specHashes(parseSpec(doc)) });
    expect(detail.versions).toEqual([expect.not.objectContaining({ spec: expect.anything() })]);
    expect(detail.versions[0].spec_sha256).toBe(detail.latest.spec_sha256);
    expect(detail.calibration_progress).toMatchObject({ evaluator_ref: 'refusal@1', state: 'none', human_labels: { n: 0, pos: 0, neg: 0 }, can_fit_measured: false });
  });

  it('gives a version a copy the caller may edit', async () => {
    const r = reads();
    const v = await r.getEvaluatorVersion('refusal', 1);
    (v.spec as { name: string }).name = 'Changed';
    expect((await r.getEvaluatorVersion('refusal', 1)).spec.name).toBe(BUILTIN_TEMPLATES['refusal@1'].name);
  });

  it.each([
    ['an unknown check', (r: ReturnType<typeof reads>) => r.getEvaluator('nope')],
    ['an unknown version', (r: ReturnType<typeof reads>) => r.getEvaluatorVersion('refusal', 9)],
  ])('answers 404 for %s', async (_name, read) => {
    const err = await read(reads()).catch((e: unknown) => e);
    expect(isApiError(err) && err.status === 404 && err.code === 'not_found').toBe(true);
  });

  it('knows the latest version of an id, every spec by ref, the route names and descriptions', () => {
    const extra: EvaluatorSpec = { ...BUILTIN_TEMPLATES['refusal@1'], version: 2 };
    const registry = staticRegistry({ templates: { ...BUILTIN_TEMPLATES, 'refusal@2': extra } });
    expect(registry.latest('refusal')).toBe(2);
    expect(registry.latest('brand_new')).toBe(0);
    expect(registry.specs().get('refusal@2')?.version).toBe(2);
    expect(registry.ids()).toEqual(ids);
    expect(registry.routes()).toEqual([DEFAULT_ROUTE, ...(policy.routes ?? []).map((r) => r.name)]);
    const described = (policy.routes ?? []).find((r) => typeof r.description === 'string');
    expect(described).toBeDefined();
    expect(registry.routeDescription(described!.name)).toBe((described!.description as string).trim());
    expect(registry.routeDescription(DEFAULT_ROUTE)).toBe('');
  });
});
