// @vitest-environment node
/**
 * Studio's policy edits on the SPEC example policy: each edit changes only the lines it must (the
 * reviewed diff shows just the change), a route that inherits the defaults' list gets its own copy,
 * and the result still passes the policy.v1 schema.
 */
import { diffLines } from 'diff';
import { describe, expect, it } from 'vitest';
import exampleYaml from '@contracts/policies/example.policies.yaml?raw';
import { DraftError } from '@/components/policies/policyDraft';
import { checkPolicyYaml } from '@/lib/policy/checkPolicyYaml';
import { attachEvaluator, findEvaluator, refId, replaceEvaluatorRef, routeEvaluators, setEvaluatorStage } from './policyEdits';

function changed(before: string, after: string): { added: string[]; removed: string[] } {
  const added: string[] = [];
  const removed: string[] = [];
  for (const part of diffLines(before, after)) {
    const lines = part.value.split('\n').filter((l) => l !== '');
    if (part.added) added.push(...lines);
    if (part.removed) removed.push(...lines);
  }
  return { added, removed };
}

const valid = (yaml: string) => expect(checkPolicyYaml(yaml).problems.filter((p) => p.severity === 'error')).toEqual([]);

describe('routeEvaluators', () => {
  it("reads a route's own list, or the defaults' list it inherits", () => {
    expect(routeEvaluators(exampleYaml, 'support-bot')).toMatchObject({ own: true, entries: [{ ref: 'groundedness@1' }, {}, {}, { ref: 'off_brand_tone@1', stage: 'shadow' }] });
    const inherited = routeEvaluators(exampleYaml, 'payments-faq');
    expect(inherited.own).toBe(false);
    expect(inherited.entries.map((e) => e.ref)).toEqual(['groundedness@1', 'contradiction@1', 'refusal@1', 'pii_secret_leak@1']);
    expect(routeEvaluators(exampleYaml, 'default').own).toBe(true);
    expect(findEvaluator(exampleYaml, 'support-bot', 'commitments')?.params).toBeDefined();
    expect(() => routeEvaluators(exampleYaml, 'nowhere')).toThrow(DraftError);
    expect(refId('a_b@12')).toBe('a_b');
  });
});

describe('attachEvaluator', () => {
  it('appends a shadow entry to a route with its own list and touches nothing else', () => {
    const next = attachEvaluator(exampleYaml, 'support-bot', 'blames_customer@1');
    expect(changed(exampleYaml, next)).toEqual({ added: ['      - ref: blames_customer@1', '        stage: shadow'], removed: [] });
    expect(findEvaluator(next, 'support-bot', 'blames_customer')).toEqual({ ref: 'blames_customer@1', stage: 'shadow' });
    valid(next);
  });

  it("gives an inheriting route its own copy of the defaults' list, plus the new entry", () => {
    const next = attachEvaluator(exampleYaml, 'payments-faq', 'blames_customer@1');
    const { entries, own } = routeEvaluators(next, 'payments-faq');
    expect(own).toBe(true);
    expect(entries.map((e) => [e.ref, e.stage])).toEqual([
      ['groundedness@1', undefined],
      ['contradiction@1', undefined],
      ['refusal@1', undefined],
      ['pii_secret_leak@1', undefined],
      ['blames_customer@1', 'shadow'],
    ]);
    // The defaults keep their list.
    expect(routeEvaluators(next, 'default').entries).toHaveLength(4);
    expect(changed(exampleYaml, next).removed).toEqual([]);
    valid(next);
  });

  it('edits the defaults for the default route', () => {
    const next = attachEvaluator(exampleYaml, 'default', 'blames_customer@1', 'active');
    expect(changed(exampleYaml, next)).toEqual({ added: ['    - ref: blames_customer@1', '      stage: active'], removed: [] });
  });

  it('refuses an evaluator the route already runs, in any version', () => {
    expect(() => attachEvaluator(exampleYaml, 'support-bot', 'commitments@1')).toThrow('support-bot already runs commitments@1.');
    expect(() => attachEvaluator(exampleYaml, 'support-bot', 'commitments@2')).toThrow(/already runs commitments@1; replace it/);
  });
});

describe('setEvaluatorStage and replaceEvaluatorRef', () => {
  it('makes a watched evaluator active by changing one line', () => {
    const next = setEvaluatorStage(exampleYaml, 'support-bot', 'off_brand_tone', 'active');
    expect(changed(exampleYaml, next)).toEqual({ added: ['        stage: active'], removed: ['        stage: shadow'] });
    valid(next);
  });

  it('adds a stage to an entry that had none', () => {
    const next = setEvaluatorStage(exampleYaml, 'support-bot', 'groundedness', 'shadow');
    expect(findEvaluator(next, 'support-bot', 'groundedness')).toEqual({ ref: 'groundedness@1', stage: 'shadow' });
    expect(changed(exampleYaml, next).removed).toEqual([]);
  });

  it('points an entry at a new version, keeping its stage and parameters', () => {
    const next = replaceEvaluatorRef(exampleYaml, 'support-bot', 'off_brand_tone@2');
    expect(findEvaluator(next, 'support-bot', 'off_brand_tone')).toMatchObject({ ref: 'off_brand_tone@2', stage: 'shadow', params: { brand_voice: expect.any(String) } });
    expect(changed(exampleYaml, next)).toEqual({ added: ['      - ref: off_brand_tone@2'], removed: ['      - ref: off_brand_tone@1'] });
  });

  it('says what is wrong instead of editing', () => {
    expect(() => setEvaluatorStage(exampleYaml, 'support-bot', 'ghost', 'active')).toThrow('support-bot does not run ghost.');
    expect(() => replaceEvaluatorRef(exampleYaml, 'support-bot', 'ghost@2')).toThrow('support-bot does not run ghost.');
    expect(() => replaceEvaluatorRef(exampleYaml, 'support-bot', 'commitments@1')).toThrow('support-bot already runs commitments@1.');
    expect(() => attachEvaluator('routes: [', 'support-bot', 'x_y@1')).toThrow(/does not parse/);
  });
});
