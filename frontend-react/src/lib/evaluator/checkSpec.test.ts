// @vitest-environment node
/**
 * Studio's in-browser spec check: lint parity with the frozen lint vectors (the same codes Go's
 * ValidateSpec and the TS lintSpec give, D54), readable schema findings, and every problem of the
 * JSON tab placed on the range it concerns.
 */
import { describe, expect, it } from 'vitest';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import { lintCodes } from '@/lib/decision';
import type { EvaluatorDraft } from '@/types/api';
import { checkSpecDocument, checkSpecJson, draftAsSpec, isSavable, lintPointer, readablePath } from './checkSpec';

interface LintVector {
  name: string;
  status: string;
  input: { spec: unknown };
  expected: { errors: string[]; lints: string[] };
}

const vectors = Object.values(import.meta.glob<LintVector>('@contracts/vectors/lint/*.json', { eager: true, import: 'default' }));
const draft = draftFixture.draft as unknown as EvaluatorDraft;
type Doc = Record<string, unknown>;
const spec = (): Doc => ({ ...draftAsSpec(structuredClone(draft) as unknown as Doc), id: 'blames_customer' });

/** The spec with each `path → value` set (dotted paths into nested objects). */
function edited(changes: Record<string, unknown>, base: Doc = spec()): Doc {
  for (const [path, value] of Object.entries(changes)) {
    const keys = path.split('.');
    let cur = base;
    for (const k of keys.slice(0, -1)) cur = cur[k] as Doc;
    cur[keys[keys.length - 1]] = value;
  }
  return base;
}

describe('lint parity with contracts/vectors/lint', () => {
  it('has the frozen vectors', () => {
    expect(vectors.length).toBeGreaterThanOrEqual(19);
    expect(vectors.every((v) => v.status === 'frozen')).toBe(true);
  });

  it.each(vectors.map((v) => [v.name, v] as const))('%s: same codes as the reference, no schema finding', (_name, v) => {
    const check = checkSpecDocument(v.input.spec);
    expect(check.errors).toEqual([]);
    expect(lintCodes(check.lints)).toEqual(v.expected);
    // The JSON tab reports the same codes from the text.
    const json = checkSpecJson(JSON.stringify(v.input.spec, null, 2));
    const codes = (severity: 'error' | 'warning') => [...new Set(json.problems.filter((p) => p.severity === severity).map((p) => p.code))].sort();
    expect({ errors: codes('error'), lints: codes('warning') }).toEqual(v.expected);
    expect(isSavable(check)).toBe(v.expected.errors.length === 0);
  });
});

describe('checkSpecDocument', () => {
  it('accepts the drafted spec and the draft itself', () => {
    expect(checkSpecDocument(spec())).toEqual({ errors: [], lints: [] });
    expect(checkSpecDocument(draft, { draft: true })).toEqual({ errors: [], lints: [] });
  });

  it('words schema findings like the gateway and keeps only the chosen question shape', () => {
    const bad = edited({ 'question.type': 'quiz' });
    expect(checkSpecDocument(bad).errors).toEqual([{ pointer: '/question', path: 'question', message: 'type must be one of: "noul", "choice", "score"', atKey: false }]);

    const extra = { ...spec(), colour: 'blue' };
    expect(checkSpecDocument(extra).errors).toEqual([{ pointer: '/colour', path: 'colour', message: "unknown field 'colour'", atKey: true }]);

    const noWeights = { ...spec(), signal: {} };
    expect(checkSpecDocument(noWeights).errors.map((e) => e.message)).toEqual(["is missing 'risk_weights'"]);

    const empty = edited({ 'question.instructions': '' });
    expect(checkSpecDocument(empty).errors.map((e) => [e.path, e.message])).toEqual([['question.instructions', 'must not be empty']]);

    const wrongText = edited({ 'question.instructions': 5 });
    expect(checkSpecDocument(wrongText).errors.map((e) => e.message)).toEqual(['must be text, a list of texts, or a map of texts']);

    const docs = { ...spec(), evidence: { requires: 'required' } };
    expect(checkSpecDocument(docs).errors.map((e) => [e.path, e.message])).toEqual([
      ['state_fields', "must include 'evidence' when the check reads the app's documents"],
    ]);

    const reserved = edited({ 'question.criteria.other': 'Anything else' });
    expect(checkSpecDocument(reserved).errors).toEqual([
      { pointer: '/question/criteria/other', path: 'question.criteria.other', message: "invalid key 'other'", atKey: true },
    ]);
  });

  it('still lints a spec the schema refuses, and reports invalid_spec only when nothing is more precise', () => {
    const both = edited({ 'outcomes.warn_at': 2, 'question.instructions': 'Does `policy_text` blame the customer?' });
    const check = checkSpecDocument(both);
    expect(check.errors.map((e) => [e.path, e.message])).toEqual([['outcomes.warn_at', 'must be <= 1']]);
    expect(lintCodes(check.lints).errors).toEqual(['unresolved_state_reference']);
    expect(check.lints.some((l) => l.code === 'invalid_spec')).toBe(false);
    expect(isSavable(check)).toBe(false);
  });

  it('refuses a document nested deeper than the gateway accepts, before the schema', () => {
    let deep: unknown = 'x';
    for (let i = 0; i < 40; i++) deep = [deep];
    const check = checkSpecDocument({ ...spec(), tags: deep });
    expect(check.errors).toHaveLength(1);
    expect(check.errors[0].message).toBe('is nested more than 32 levels deep');
    expect(check.lints).toEqual([]);
  });

  it('checks references against the registry when one is given', () => {
    const s = edited({ 'outcomes.requires_corroboration': ['contradiction', 'ghost_check'] });
    expect(lintCodes(checkSpecDocument(s).lints).errors).toEqual([]);
    expect(lintCodes(checkSpecDocument(s, { registry: ['contradiction'] }).lints).errors).toEqual(['unknown_evaluator_reference']);
  });
});

describe('checkSpecJson', () => {
  it('places a syntax error on its character', () => {
    const text = '{\n  "name": "x",\n  "id": oops\n}';
    const check = checkSpecJson(text);
    expect(check.parsed).toBe(false);
    expect(check.problems).toEqual([
      expect.objectContaining({ severity: 'error', code: 'syntax', line: 3, col: 9, from: text.indexOf('oops'), message: 'Expected a value, found “o”.' }),
    ]);
  });

  it('underlines the key of an unknown field, the value of a wrong one and the opening of an object missing one', () => {
    const s = edited({ colour: 'blue', 'outcomes.warn_at': 2, signal: {} });
    const text = JSON.stringify(s, null, 2);
    const byPath = new Map(checkSpecJson(text).problems.map((p) => [p.path, p]));
    const colour = byPath.get('colour');
    expect(text.slice(colour?.from, colour?.to)).toBe('"colour"');
    const warn = byPath.get('outcomes.warn_at');
    expect(text.slice(warn?.from, warn?.to)).toBe('2');
    const signal = byPath.get('signal');
    expect(text.slice(signal?.from, signal?.to)).toBe('{');
    expect(signal?.line).toBe(text.slice(0, text.indexOf('"signal"')).split('\n').length);
  });

  it('puts a lint on the field it names', () => {
    const s = edited({ 'question.instructions': 'Does `policy_text` blame the customer?' });
    const text = JSON.stringify(s, null, 2);
    const lint = checkSpecJson(text).problems.find((p) => p.code === 'unresolved_state_reference');
    expect(lint?.severity).toBe('error');
    expect(text.slice(lint?.from, lint?.to)).toBe('"Does `policy_text` blame the customer?"');
  });
});

describe('paths', () => {
  it('maps lint paths to pointers and pointers to the gateway words', () => {
    expect(lintPointer('')).toBe('');
    expect(lintPointer('question.criteria.no_blame')).toBe('/question/criteria/no_blame');
    expect(lintPointer('outcomes.implies[1]')).toBe('/outcomes/implies/1');
    expect(lintPointer('question.criteria[0][2]')).toBe('/question/criteria/0/2');
    expect(readablePath('/question/criteria/0', { question: { criteria: ['a'] } })).toBe('question.criteria[0]');
    expect(readablePath('/a~1b/c~0d', { 'a/b': { 'c~d': 1 } })).toBe('a/b.c~d');
  });
});
