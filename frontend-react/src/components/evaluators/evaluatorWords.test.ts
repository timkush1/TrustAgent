import { describe, expect, it } from 'vitest';
import evaluatorFiles from '@contracts/evaluators/off_topic.v1.json';
import { copy } from '@/domain/presentation';
import { evaluatorDescription } from './evaluatorWords';

const of = (id: string, origin: 'builtin' | 'file' | 'dashboard', description?: string) => ({ id, origin, latest: { spec: { description } } });

describe('evaluatorDescription', () => {
  it('prefers the spec’s own line, then the built-in copy, and invents none for a saved check', () => {
    expect(evaluatorDescription(of('groundedness', 'builtin', 'Flags answers that state facts the evidence does not contain.'))).toBe(
      'Flags answers that state facts the evidence does not contain.',
    );
    // off_topic's template has no description of its own: the contract copy gives it one.
    expect((evaluatorFiles as { description?: string }).description).toBeUndefined();
    expect(evaluatorDescription(of('off_topic', 'builtin'))).toBe(copy.evaluators.builtin_descriptions.off_topic);
    expect(evaluatorDescription(of('off_topic', 'builtin', '  '))).toBe(copy.evaluators.builtin_descriptions.off_topic);
    expect(evaluatorDescription(of('off_topic', 'dashboard'))).toBeNull();
    expect(evaluatorDescription(of('blames_the_customer', 'builtin'))).toBeNull();
  });

  it('has a line for every built-in check whose template has none', () => {
    const templates = import.meta.glob<{ id: string; description?: string }>('../../../../contracts/evaluators/*.v1.json', { eager: true, import: 'default' });
    const missing = Object.values(templates).filter((t) => !t.description).map((t) => t.id);
    expect(missing.length).toBeGreaterThan(0);
    for (const id of missing) expect(evaluatorDescription(of(id, 'builtin')), id).toMatch(/^Flags answers /);
  });
});
