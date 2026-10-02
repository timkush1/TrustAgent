// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { checkSpecDocument, isSavable } from '@/lib/evaluator/checkSpec';
import { fieldAt, parseList, parseThreshold, withField } from './specFields';
import { blankSpec, specFromTemplate, STUDIO_TEMPLATES } from './templates';

describe('templates', () => {
  it('offers every built-in except the internal factual_claims, by name', () => {
    expect(STUDIO_TEMPLATES).toHaveLength(Object.keys(BUILTIN_TEMPLATES).length - 1);
    expect(STUDIO_TEMPLATES.some((t) => t.ref.startsWith('factual_claims@'))).toBe(false);
    const names = STUDIO_TEMPLATES.map((t) => t.name);
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)));
  });

  it.each(STUDIO_TEMPLATES.map((t) => [t.ref]))('copies %s as a new, savable check of your own', (ref) => {
    const spec = specFromTemplate(ref, ['refusal_custom']);
    const template = BUILTIN_TEMPLATES[ref];
    expect(spec.id).not.toBe(template.id);
    expect(spec.id).not.toBe('refusal_custom');
    expect(spec.builtin).toBeUndefined();
    expect(spec.parent).toBe(ref);
    expect(spec.version).toBe(1);
    expect(spec.question).toEqual(template.question);
    expect(isSavable(checkSpecDocument(spec))).toBe(true);
  });

  it('starts a blank check that passes every check', () => {
    expect(checkSpecDocument(blankSpec())).toEqual({ errors: [], lints: [] });
    expect(blankSpec(['custom_check']).id).toBe('custom_check_2');
    expect(() => specFromTemplate('ghost@1')).toThrow('There is no template ghost@1.');
  });
});

describe('specFields', () => {
  it('sets and removes one field, keeping the rest', () => {
    const spec = blankSpec();
    const edited = withField(spec, ['outcomes', 'critical_at'], 0.9);
    expect(edited.outcomes).toEqual({ ...spec.outcomes, critical_at: 0.9 });
    expect(spec.outcomes.critical_at).toBeNull();
    expect(Object.keys(withField(spec, ['description'], 'Why')).at(-1)).toBe('description');
    expect('escalation' in withField(spec, ['escalation'], undefined)).toBe(false);
    expect(fieldAt(edited, ['outcomes', 'critical_at'])).toBe(0.9);
    expect(fieldAt(edited, ['nope', 'deeper'])).toBeUndefined();
  });

  it('parses comma lists and thresholds', () => {
    expect(parseList(' a, b,, a ,c ')).toEqual(['a', 'b', 'c']);
    expect(parseThreshold('')).toBeNull();
    expect(parseThreshold('0.75')).toBe(0.75);
    expect(parseThreshold('1.5')).toBeUndefined();
    expect(parseThreshold('x')).toBeUndefined();
  });
});
