import exampleYaml from '@contracts/policies/example.policies.yaml?raw';
import { describe, expect, it } from 'vitest';
import { checkPolicyYaml } from './checkPolicyYaml';

describe('checkPolicyYaml', () => {
  it('accepts the SPEC §2.7 example policy', () => {
    const r = checkPolicyYaml(exampleYaml);
    expect(r.problems).toEqual([]);
    expect(r.data).toMatchObject({ version: 1 });
  });

  it('reports a YAML syntax error with its line', () => {
    const r = checkPolicyYaml('version: 1\nroutes:\n  - name: a\n   mode: flag\n');
    expect(r.data).toBeNull();
    expect(r.problems[0]).toMatchObject({ severity: 'error', line: 4 });
  });

  it('places a bad enum value on its own line, with the allowed values', () => {
    const text = 'version: 1\nroutes:\n  - name: support-bot\n    match: {}\n    mode: enforce\n';
    const [p] = checkPolicyYaml(text).problems;
    expect(p.message).toBe('routes[0].mode must be one of: "observe", "flag", "block", "fallback"');
    expect(p.line).toBe(5);
    expect(text.slice(p.from, p.to)).toBe('enforce');
  });

  it('points an unknown field at its key', () => {
    const text = 'version: 1\ndefaults:\n  deadline: 600\n';
    const [p] = checkPolicyYaml(text).problems;
    expect(p.message).toBe('defaults has an unknown field "deadline"');
    expect(text.slice(p.from, p.to)).toBe('deadline');
  });

  it('forbids @latest evaluator refs (SPEC §2.7)', () => {
    const r = checkPolicyYaml('version: 1\ndefaults:\n  evaluators:\n    - ref: groundedness@latest\n');
    expect(r.problems.length).toBeGreaterThan(0);
    expect(r.problems[0].line).toBe(4);
  });

  it('flags a duplicate key', () => {
    const r = checkPolicyYaml('version: 1\nversion: 1\n');
    expect(r.problems[0].severity).toBe('error');
  });
});
