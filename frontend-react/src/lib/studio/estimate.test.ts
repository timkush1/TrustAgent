// @vitest-environment node
import { describe, expect, it } from 'vitest';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import type { EvaluatorDraft } from '@/types/api';
import { draftSamples, draftToSpec } from './draftToSpec';
import { DRAFT_ESTIMATE_USD, draftEstimate, priceEntryFor, sampleTokens, testEstimate } from './estimate';

const draft = draftFixture.draft as unknown as EvaluatorDraft;
const spec = draftToSpec(draft);
const samples = draftSamples(draft);

describe('priceEntryFor', () => {
  it("finds the decision model's price by provider first", () => {
    expect(priceEntryFor('typesafe/jev-1.13-20260917', 'openrouter')?.input_per_mtok).toBe(0.042);
    expect(priceEntryFor('jev-1.13', 'typesafe')?.match).toBe('jev-*');
    expect(priceEntryFor('llama3', 'ollama')?.input_per_mtok).toBe(0);
    expect(priceEntryFor('unknown-model', null)).toBeNull();
  });
});

describe('testEstimate', () => {
  it('prices one call per sample on input tokens, labelled an estimate', () => {
    const e = testEstimate(spec, samples, { backend: 'openrouter-decisions', name: 'typesafe/jev-1.13-20260917' });
    const total = samples.reduce((n, s) => n + sampleTokens(spec, s), 0);
    expect(e.label).toBe('estimate');
    expect(e.usd).toBeCloseTo((total * 0.042) / 1e6, 12);
    expect(e.basis).toMatch(/^6 calls, about [\d,]+ input tokens at \$0.042 per million \(typesafe\/jev-1.13\*, price table\)\.$/);
    // Each sample costs the fixed overhead, the question and its own text.
    expect(sampleTokens(spec, samples[0])).toBeGreaterThan(275);
    expect(sampleTokens(spec, { ...samples[0], assistant_response: samples[0].assistant_response.repeat(10) })).toBeGreaterThan(sampleTokens(spec, samples[0]));
  });

  it('is simulated and free on a mock, and has no figure without a price', () => {
    expect(testEstimate(spec, samples, { backend: 'mock', name: null })).toMatchObject({ usd: 0, label: 'simulated' });
    const unknown = testEstimate(spec, samples, { backend: 'compatible', name: 'acme/judge-9' });
    expect(unknown.usd).toBeNull();
    expect(unknown.basis).toMatch(/not in the price table/);
  });
});

describe('draftEstimate', () => {
  it('is ≈ $0.005 as an estimate, or simulated with a mock judge', () => {
    expect(draftEstimate({ provider: 'openrouter', model: 'anthropic/claude-haiku-4.5' })).toMatchObject({ usd: DRAFT_ESTIMATE_USD, label: 'estimate' });
    expect(DRAFT_ESTIMATE_USD).toBe(0.005);
    expect(draftEstimate({ provider: 'mock', model: 'mock' })).toMatchObject({ usd: 0, label: 'simulated' });
  });
});
