import exampleYaml from '@contracts/policies/example.policies.yaml?raw';
import { describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { diffTexts, summarizeDiff } from '@/domain/lineDiff';
import {
  addTagPattern,
  allowUncalibratedBlocking,
  applyPreset,
  bufferStreams,
  DEFAULT_ROUTE,
  DraftError,
  draftRouteNames,
  effectiveEvidenceSources,
  keepOriginalFormatting,
  PRESETS,
  SCHEMA_DEFAULT_EVALUATORS,
  setEvaluatorThresholds,
  setRouteMode,
  stopTrustingUserContext,
  yamlDiffers,
} from './policyDraft';

const route = (yaml: string, name: string) => (parse(yaml) as { routes: { name: string }[] }).routes.find((r) => r.name === name) as Record<string, unknown>;

describe('policy draft edits', () => {
  it('changes one line and keeps comments and layout everywhere else', () => {
    const next = setRouteMode(exampleYaml, 'support-bot', 'block');
    expect(route(next, 'support-bot').mode).toBe('block');
    expect(summarizeDiff(diffTexts(exampleYaml, next))).toEqual({ added: 1, removed: 1 });
    expect(next).toContain('# end users cannot inject "documents"');
    expect(next).toContain('# yaml-language-server: $schema=../schema/policy.v1.schema.json');
  });

  it('buffers streams and allows uncalibrated blocking on one route', () => {
    let next = bufferStreams(exampleYaml, 'support-bot');
    next = allowUncalibratedBlocking(next, 'support-bot');
    expect(route(next, 'support-bot')).toMatchObject({ streaming: 'buffer', require_calibration: false });
    expect(route(next, 'payments-faq').require_calibration).toBeUndefined();
  });

  it('edits the defaults for the default route', () => {
    const next = setRouteMode(exampleYaml, DEFAULT_ROUTE, 'flag');
    expect((parse(next) as { defaults: { mode: string } }).defaults.mode).toBe('flag');
  });

  it('stops trusting user context with the route or default sources', () => {
    expect(effectiveEvidenceSources(exampleYaml, 'support-bot')).toEqual(['request_system', 'request_context', 'tool_results', 'extension']);
    const next = stopTrustingUserContext(exampleYaml, 'support-bot');
    expect(route(next, 'support-bot').evidence).toEqual({ sources: ['request_system', 'tool_results', 'extension'] });
    expect(effectiveEvidenceSources(exampleYaml, 'payments-faq')).toEqual(['request_system', 'tool_results', 'extension']);
  });

  it('applies a preset over a route', () => {
    const block = PRESETS.find((p) => p.id === 'block-contradictions');
    expect(block?.ui_note).toBe('Starts as Flag until calibrated');
    const next = applyPreset(exampleYaml, 'support-bot', block!);
    expect(route(next, 'support-bot')).toMatchObject({
      mode: 'block',
      streaming: 'buffer',
      evaluators: [{ ref: 'groundedness@1' }, { ref: 'contradiction@1' }],
    });
  });

  it('merges a preset into nested route settings key by key, like routes merge over defaults', () => {
    const preset = {
      settings: {
        mode: 'fallback' as const,
        evidence: { sources: ['request_system' as const, 'extension' as const] },
        fallback: { upstream: 'same' as const, on_fail: 'flag' as const },
        detectors: { block_on: ['urls_not_in_evidence' as const] },
      },
    };
    const next = applyPreset(exampleYaml, 'payments-faq', preset);
    const r = route(next, 'payments-faq');
    // Arrays replace; the keys the preset does not name stay.
    expect(r.evidence).toEqual({ sources: ['request_system', 'extension'] });
    expect(r.detectors).toEqual({ block_on: ['urls_not_in_evidence'] });
    expect(r.fallback).toMatchObject({ upstream: 'same', model: 'gpt-4o', on_fail: 'flag' });
    expect((r.fallback as { system_prompt: string }).system_prompt).toMatch(/^Answer using only the information in CONTEXT/);
    expect(r.block).toMatchObject({ style: 'message' });
    // A route without the object gets the preset's whole object.
    const support = route(applyPreset(exampleYaml, 'support-bot', preset), 'support-bot');
    expect(support.fallback).toEqual({ upstream: 'same', on_fail: 'flag' });
  });

  it('refuses unknown routes and broken YAML', () => {
    expect(() => setRouteMode(exampleYaml, 'nope', 'flag')).toThrow(DraftError);
    expect(() => setRouteMode('routes: [', 'x', 'flag')).toThrow(/does not parse/);
  });

  it('lists routes with the default first', () => {
    expect(draftRouteNames(exampleYaml)).toEqual(['default', 'support-bot', 'payments-faq']);
    expect(draftRouteNames('{{')).toEqual(['default']);
  });

  it('ignores whitespace-only differences', () => {
    expect(yamlDiffers('a: 1\n', 'a:  1\n')).toBe(false);
    expect(yamlDiffers('a: 1\n', 'a: 2\n')).toBe(true);
    expect(keepOriginalFormatting('a:   1 # c\nb: 2\n', 'a: 1 # c\nb: 3\n')).toBe('a:   1 # c\nb: 3\n');
  });

  it('sets one evaluator\'s thresholds on a route that lists its evaluators, touching nothing else', () => {
    const next = setEvaluatorThresholds(exampleYaml, 'support-bot', 'groundedness@1', { warn_at: 0.7, critical_at: 0.9 });
    const evaluators = route(next, 'support-bot').evaluators as { ref: string; thresholds?: object }[];
    expect(evaluators[0]).toEqual({ ref: 'groundedness@1', thresholds: { warn_at: 0.7, critical_at: 0.9 } });
    expect(evaluators[2]).toMatchObject({ ref: 'commitments@1', params: { allowed_commitments: expect.stringMatching(/^Annual plans/) } });
    expect(evaluators[3]).not.toHaveProperty('thresholds');
    expect(next).toContain('# end users cannot inject "documents"');
    // Adopt writes the calibrated space and a null block threshold when none is recommended.
    const adopted = setEvaluatorThresholds(next, 'support-bot', 'groundedness@1', { space: 'calibrated', warn_at: 0.5, critical_at: null });
    expect((route(adopted, 'support-bot').evaluators as { thresholds?: object }[])[0].thresholds).toEqual({ warn_at: 0.5, critical_at: null, space: 'calibrated' });
  });

  it('copies inherited evaluators to the route before changing one, since lists replace', () => {
    const next = setEvaluatorThresholds(exampleYaml, 'payments-faq', 'contradiction@1', { warn_at: 0.6 });
    const evaluators = route(next, 'payments-faq').evaluators as { ref: string; thresholds?: object }[];
    expect(evaluators.map((e) => e.ref)).toEqual(['groundedness@1', 'contradiction@1', 'refusal@1', 'pii_secret_leak@1']);
    expect(evaluators[1].thresholds).toEqual({ warn_at: 0.6 });
    // The defaults themselves stay as they were.
    expect((parse(next) as { defaults: { evaluators: object[] } }).defaults.evaluators).toEqual([
      { ref: 'groundedness@1' },
      { ref: 'contradiction@1' },
      { ref: 'refusal@1' },
      { ref: 'pii_secret_leak@1' },
    ]);
    // Without a defaults list, the route gets policy.v1's own default list.
    const bare = 'version: 1\ndefaults: {mode: flag}\nroutes:\n  - name: r\n    match: {paths: [/x]}\n';
    const fromSchema = route(setEvaluatorThresholds(bare, 'r', 'refusal@1', { warn_at: 0.4 }), 'r').evaluators as { ref: string }[];
    expect(fromSchema.map((e) => e.ref)).toEqual(SCHEMA_DEFAULT_EVALUATORS.map((e) => e.ref));
  });

  it('refuses a threshold for a check the route does not run', () => {
    expect(() => setEvaluatorThresholds(exampleYaml, 'support-bot', 'refusal@1', { warn_at: 0.5 })).toThrow('Route support-bot does not run refusal@1.');
  });

  it('adds an evidence tag pattern to the list a route inherits, once', () => {
    const pattern = { pattern: '<kb_article id="([^"]+)">([\\s\\S]*?)</kb_article>', flags: 'i' };
    const next = addTagPattern(exampleYaml, 'payments-faq', pattern);
    expect(route(next, 'payments-faq').evidence).toEqual({ sources: ['request_system', 'tool_results', 'extension'], tag_patterns: [pattern] });
    expect(() => addTagPattern(next, 'payments-faq', pattern)).toThrow(/already reads documents with this pattern/);
    const withDefault = addTagPattern(exampleYaml, DEFAULT_ROUTE, { pattern: '<doc>(.*)</doc>' });
    expect((parse(withDefault) as { defaults: { evidence: { tag_patterns: object[] } } }).defaults.evidence.tag_patterns).toEqual([{ pattern: '<doc>(.*)</doc>' }]);
    // A route inherits the defaults' patterns and keeps them when it adds its own.
    const both = addTagPattern(withDefault, 'support-bot', pattern);
    expect((route(both, 'support-bot').evidence as { tag_patterns: object[] }).tag_patterns).toEqual([{ pattern: '<doc>(.*)</doc>' }, pattern]);
  });
});
