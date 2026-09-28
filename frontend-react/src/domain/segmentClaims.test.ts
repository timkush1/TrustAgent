import { describe, expect, it } from 'vitest';
import type { ClaimVerdict, ClaimView } from '@/types/api';
import { segmentClaims } from './segmentClaims';

function claim(index: number, status: ClaimVerdict, span: ClaimView['span'], text = `claim ${index}`): ClaimView {
  return {
    index,
    claim: text,
    status,
    confidence: 0.9,
    evidence: [],
    reasoning: null,
    decided_by: 'llm',
    verdict_probabilities: {},
    probability_source: 'llm_confidence',
    span,
    quote: null,
    evidence_spans: [],
  };
}

const at = (start: number, end: number, method: 'exact' | 'normalized' | 'fuzzy' = 'exact') => ({ start, end, method });

/** The runs as [text, verdict] pairs. */
const runs = (r: ReturnType<typeof segmentClaims>) => r.segments.map((s) => [s.text, s.verdict]);

describe('segmentClaims', () => {
  it('marks a claim at its span and leaves the rest plain', () => {
    const text = 'You can get a full refund within 60 days of purchase.';
    const start = text.indexOf('60 days');
    const r = segmentClaims(text, [claim(0, 'CONTRADICTED', at(start, start + 7))]);
    expect(runs(r)).toEqual([
      ['You can get a full refund within ', null],
      ['60 days', 'CONTRADICTED'],
      [' of purchase.', null],
    ]);
    expect(r.segments.map((s) => s.text).join('')).toBe(text);
    expect(r.unlocated).toEqual([]);
  });

  it('uses UTF-16 offsets, so text after emoji and astral characters lines up', () => {
    const text = '👋 Refunds: 𝟔𝟎 days, 30 € fee.';
    const start = text.indexOf('30 €');
    const r = segmentClaims(text, [claim(0, 'UNSUPPORTED', at(start, start + 4))]);
    expect(r.segments.find((s) => s.verdict)?.text).toBe('30 €');
  });

  it('marks the characters the engine located in CJK and emoji text (engine offsets, not indexOf)', () => {
    // Offsets computed by the engine's aligner (backend-python text/spans.py align_quote) for this
    // exact text. Python indexes code points, so it must convert: '富士山…' starts at code point 3
    // but UTF-16 offset 5, after the two astral emoji.
    const text = '🗻🎉 富士山は日本にある。退款期限为30天，不是60 days. Launch day 🚀 went well.';
    const r = segmentClaims(text, [
      claim(0, 'SUPPORTED', at(5, 14)),
      claim(1, 'CONTRADICTED', at(15, 23)),
      claim(2, 'UNSUPPORTED', at(26, 33)),
      claim(3, 'PARTIALLY_SUPPORTED', at(42, 58)),
    ]);
    expect(r.segments.filter((s) => s.verdict).map((s) => [s.text, s.verdict])).toEqual([
      ['富士山は日本にある', 'SUPPORTED'],
      ['退款期限为30天', 'CONTRADICTED'],
      ['60 days', 'UNSUPPORTED'],
      ['day 🚀 went well', 'PARTIALLY_SUPPORTED'],
    ]);
    expect(r.segments.map((s) => s.text).join('')).toBe(text);
    expect(r.unlocated).toEqual([]);
  });

  it('keeps engine offsets on the original text when it holds characters the engine strips', () => {
    // The engine audits the text without the zero-width space and maps the span back (19..26).
    const text = 'Refund​ window: 👋 30 days.';
    const r = segmentClaims(text, [claim(0, 'CONTRADICTED', at(19, 26))]);
    expect(r.segments.find((s) => s.verdict)?.text).toBe('30 days');
  });

  it('never splits a surrogate pair', () => {
    const text = 'ab😀cd';
    // 3 is inside the emoji (a high surrogate at 2, low at 3); 4 is after it.
    const r = segmentClaims(text, [claim(0, 'SUPPORTED', at(3, 5))]);
    expect(r.segments.find((s) => s.verdict)?.text).toBe('😀c');
  });

  it('draws overlapping claims with the most severe verdict and keeps every claim on the run', () => {
    const text = 'Plans cost $10 and refunds take 60 days.';
    const sentence = at(0, text.length - 1);
    const days = at(text.indexOf('60 days'), text.indexOf('60 days') + 7);
    const r = segmentClaims(text, [claim(0, 'SUPPORTED', sentence), claim(1, 'CONTRADICTED', days)]);
    expect(runs(r)).toEqual([
      ['Plans cost $10 and refunds take ', 'SUPPORTED'],
      ['60 days', 'CONTRADICTED'],
      ['.', null],
    ]);
    expect(r.segments[1].claims.map((c) => c.index)).toEqual([1, 0]);
  });

  it('lists claims without a usable span as not located, in claim order', () => {
    const text = 'Short answer.';
    const r = segmentClaims(text, [
      claim(2, 'UNSUPPORTED', at(0, 99)),
      claim(0, 'CONTRADICTED', null),
      claim(1, 'PARTIALLY_SUPPORTED', at(5, 5)),
    ]);
    expect(r.unlocated.map((c) => c.index)).toEqual([0, 1, 2]);
    expect(runs(r)).toEqual([[text, null]]);
  });

  it('never marks NOT_CHECKED claims, nor lists them as not located', () => {
    const text = 'Nothing to check here.';
    const r = segmentClaims(text, [claim(0, 'NOT_CHECKED', at(0, 7)), claim(1, 'NOT_CHECKED', null)]);
    expect(runs(r)).toEqual([[text, null]]);
    expect(r.unlocated).toEqual([]);
  });

  it('flags approximate spans', () => {
    const r = segmentClaims('abc def', [claim(0, 'UNKNOWN', at(4, 7, 'fuzzy'))]);
    expect(r.segments.find((s) => s.verdict)?.fuzzy).toBe(true);
  });

  it('keeps the text plain and lists every claim when the spans refer to another text', () => {
    const text = 'A preview of the ans';
    const r = segmentClaims(text, [claim(0, 'CONTRADICTED', at(0, 5))], { located: false });
    expect(runs(r)).toEqual([[text, null]]);
    expect(r.unlocated.map((c) => c.index)).toEqual([0]);
  });
});
