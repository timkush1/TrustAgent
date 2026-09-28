import { describe, expect, it } from 'vitest';
import { OUTCOME_IDS } from '@/domain/contracts';
import { outcomeView } from '@/domain/presentation';
import { CLAIM_VERDICT_ORDER } from '@/domain/presentation';
import { OUTCOME_STACK_ORDER, outcomeSeriesStyle, seriesColor, verdictSeriesColor } from './palette';
import { stackedSegmentPath } from './rechartsTheme';

describe('chart palette', () => {
  it('stacks every outcome exactly once', () => {
    expect([...OUTCOME_STACK_ORDER].sort()).toEqual([...OUTCOME_IDS].sort());
  });

  it("takes each outcome's tone from the contract and gives every outcome its own tone and shade", () => {
    const seen = new Set<string>();
    for (const id of OUTCOME_IDS) {
      const style = outcomeSeriesStyle(id);
      expect(style.tone).toBe(outcomeView(id).tone);
      const key = `${style.tone}/${style.shade}`;
      expect(seen.has(key), `${id} shares ${key}`).toBe(false);
      seen.add(key);
    }
  });

  it('draws only from the tone tokens and the neutral inks', () => {
    for (const id of OUTCOME_IDS) {
      expect(seriesColor(outcomeSeriesStyle(id))).toMatch(/^(var\(--tone-[a-z]+\)|color-mix\(in oklab, var\(--tone-[a-z]+\) 70%, var\(--surface\)\))$/);
    }
    expect(seriesColor({ tone: 'ink', shade: 'solid' })).toBe('var(--fg-muted)');
    expect(seriesColor({ tone: 'ink', shade: 'light' })).toBe('var(--fg-subtle)');
  });

  it('gives each claim verdict a distinct color from the verdict tokens', () => {
    const colors = CLAIM_VERDICT_ORDER.map(verdictSeriesColor);
    expect(new Set(colors).size).toBe(colors.length);
    for (const c of colors) expect(c).toContain('var(--v-');
  });
});

describe('stackedSegmentPath', () => {
  const order = ['passed', 'flagged', 'blocked'];

  it('rounds the top of the topmost non-empty segment and keeps its full height', () => {
    const d = stackedSegmentPath({ x: 10, y: 20, width: 16, height: 30, payload: { passed: 5, flagged: 3, blocked: 0 } }, 'flagged', order);
    expect(d).toBe('M10,50 L10,24 Q10,20 14,20 L22,20 Q26,20 26,24 L26,50 Z');
  });

  it('leaves a 2px surface gap on top of a segment that has another above it', () => {
    const d = stackedSegmentPath({ x: 10, y: 50, width: 16, height: 30, payload: { passed: 5, flagged: 3 } }, 'passed', order);
    expect(d).toBe('M10,52 H26 V80 H10 Z');
  });

  it('keeps a sliver of a tiny segment and draws nothing for an empty one', () => {
    expect(stackedSegmentPath({ x: 0, y: 0, width: 10, height: 1.5, payload: { passed: 1, blocked: 4 } }, 'passed', order)).toBe(
      'M0,0.5 H10 V1.5 H0 Z',
    );
    expect(stackedSegmentPath({ x: 0, y: 0, width: 10, height: 0, payload: {} }, 'passed', order)).toBe('');
  });
});
