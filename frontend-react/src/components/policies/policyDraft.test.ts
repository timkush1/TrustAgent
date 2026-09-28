import exampleYaml from '@contracts/policies/example.policies.yaml?raw';
import { describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { diffTexts, summarizeDiff } from '@/domain/lineDiff';
import {
  allowUncalibratedBlocking,
  applyPreset,
  bufferStreams,
  DEFAULT_ROUTE,
  DraftError,
  draftRouteNames,
  effectiveEvidenceSources,
  keepOriginalFormatting,
  PRESETS,
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
});
