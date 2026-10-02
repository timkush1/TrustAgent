// @vitest-environment node
/**
 * The Simple form ↔ spec round trip (STW-2): reading and writing back an untouched form
 * returns the same spec, unknown and JSON-only fields included, and each Simple control changes only
 * its own fields.
 */
import { describe, expect, it } from 'vitest';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { checkSpecDocument, isSavable } from '@/lib/evaluator/checkSpec';
import type { EvaluatorDraft, EvaluatorSpec } from '@/types/api';
import { draftToSpec } from './draftToSpec';
import { applySimpleForm, optionKeyProblem, readSimpleForm, RECOMMENDED_CORROBORATORS, roleOf, switchAnswerType, type SimpleForm } from './simpleForm';

const choice = () => draftToSpec(draftFixture.draft as unknown as EvaluatorDraft);

const noul = (): EvaluatorSpec => ({
  spec_version: 'evaluator/v1',
  id: 'promises_date',
  version: 1,
  name: 'Promises a date',
  category: 'custom',
  state_fields: ['assistant_response'],
  question: { type: 'noul', instructions: 'Does `assistant_response` promise a delivery date?', criteria: { true: 'A date is promised', false: 'No date is promised' } },
  evidence: { requires: 'none' },
  signal: { invert: false },
  outcomes: { warn_at: 0.6, critical_at: null, block_eligible: false, requires_corroboration: [], implies: [] },
  escalation: { escalate_from: null, numeric_or_date: true, on_error: 'ignore' },
});

function form(spec: unknown): SimpleForm {
  const read = readSimpleForm(spec);
  if (!read.ok) throw new Error(read.reason);
  return read.form;
}

describe('round trip', () => {
  const specs: [string, EvaluatorSpec][] = [
    ['the drafted choice spec', choice()],
    ['a yes/no spec', noul()],
    ...Object.entries(BUILTIN_TEMPLATES)
      .filter(([, s]) => readSimpleForm(s).ok)
      .map(([ref, s]) => [`built-in ${ref}`, s] as [string, EvaluatorSpec]),
  ];

  it.each(specs)('%s comes back unchanged, member order included', (_name, spec) => {
    const back = applySimpleForm(spec, form(spec));
    expect(back).toEqual(spec);
    expect(JSON.stringify(back)).toBe(JSON.stringify(spec));
  });

  it('keeps unknown and JSON-only fields through every Simple edit', () => {
    const spec = {
      ...choice(),
      params: { tone: { default: 'friendly' } },
      tags: ['support'],
      x_vendor: { note: 'kept' },
    } as unknown as EvaluatorSpec & { x_vendor: unknown };
    (spec.question.criteria as Record<string, unknown>).no_blame = { what: 'Neutral', not_for: 'Blame', examples: ['We are on it.'] };
    const f = form(spec);
    const edited = applySimpleForm(spec, {
      ...f,
      name: 'Blame',
      warnAt: 0.4,
      options: f.options.map((o) => (o.key === 'no_blame' ? { ...o, what: 'Neutral wording' } : o)),
    }) as typeof spec;
    expect(edited.x_vendor).toEqual({ note: 'kept' });
    expect(edited.params).toEqual({ tone: { default: 'friendly' } });
    expect(edited.tags).toEqual(['support']);
    expect((edited.question.criteria as Record<string, unknown>).no_blame).toEqual({ what: 'Neutral wording', not_for: 'Blame', examples: ['We are on it.'] });
    expect(edited.escalation).toEqual(spec.escalation);
  });
});

describe('readSimpleForm', () => {
  it('reads the choice options with their weights (0 when the spec gives none)', () => {
    const f = form(choice());
    expect(f.answerType).toBe('pick_one');
    expect(f.options.map((o) => [o.key, o.weight, roleOf(o.weight)])).toEqual([
      ['no_blame', 0, 'fine'],
      ['implied_blame', 0.5, 'borderline'],
      ['explicit_blame', 1, 'problem'],
    ]);
    expect(f.needsDocuments).toBe(false);
    expect(f.warnAt).toBe(0.6);
  });

  it('says why a spec needs the JSON tab', () => {
    const score = { ...noul(), question: { type: 'score', instructions: 'How rude?', criteria: ['Polite', 'Rude'] }, signal: { risk_levels: [1] } };
    expect(readSimpleForm(score)).toEqual({ ok: false, reason: expect.stringMatching(/scale/) });
    const structured = noul();
    structured.question.instructions = ['Step one', 'Step two'];
    expect(readSimpleForm(structured)).toEqual({ ok: false, reason: expect.stringMatching(/structured/) });
    expect(readSimpleForm(null).ok).toBe(false);
    expect(readSimpleForm({ name: 'x' }).ok).toBe(false);
    expect(roleOf(0.7)).toBeNull();
  });
});

describe('Simple edits', () => {
  it('sets a weight, and leaves a Fine option without one out of risk_weights', () => {
    const spec = choice();
    const f = form(spec);
    const edited = applySimpleForm(spec, { ...f, options: f.options.map((o) => (o.key === 'implied_blame' ? { ...o, weight: 1 } : o)) });
    expect(edited.signal.risk_weights).toEqual({ explicit_blame: 1, implied_blame: 1 });
    const fine = applySimpleForm(spec, { ...f, options: f.options.map((o) => (o.key === 'implied_blame' ? { ...o, weight: 0 } : o)) });
    expect(fine.signal.risk_weights).toEqual({ explicit_blame: 1, implied_blame: 0 });
  });

  it('adds, renames and removes options with their weights', () => {
    const spec = choice();
    const f = form(spec);
    const options = [...f.options.filter((o) => o.key !== 'implied_blame'), { key: 'mocks_customer', what: 'Mocks the customer', weight: 1 }];
    options[1] = { ...options[1], key: 'outright_blame' };
    const edited = applySimpleForm(spec, { ...f, options });
    expect(Object.keys(edited.question.criteria)).toEqual(['no_blame', 'outright_blame', 'mocks_customer']);
    expect(edited.signal.risk_weights).toEqual({ outright_blame: 1, mocks_customer: 1 });
    expect(isSavable(checkSpecDocument(edited))).toBe(true);
  });

  it("renames an option with what it is not for, its examples and its weight, in its place", () => {
    const spec = choice();
    (spec.question.criteria as Record<string, Record<string, unknown>>).implied_blame.examples = ['You should have read the manual.'];
    const f = form(spec);
    const edited = applySimpleForm(spec, { ...f, options: f.options.map((o) => (o.key === 'implied_blame' ? { ...o, key: 'hinted_blame' } : o)) });
    expect(Object.keys(edited.question.criteria)).toEqual(['no_blame', 'hinted_blame', 'explicit_blame']);
    expect((edited.question.criteria as Record<string, unknown>).hinted_blame).toEqual({
      what: 'Hints that the customer is at fault, without saying it outright',
      not_for: 'Plain instructions that do not judge the customer',
      examples: ['You should have read the manual.'],
    });
    expect(edited.signal.risk_weights).toEqual({ explicit_blame: 1, hinted_blame: 0.5 });
    // An option the form adds under a key the spec used before starts fresh: plain text, no weight.
    const readded = applySimpleForm(edited, {
      ...form(edited),
      options: [...form(edited).options.filter((o) => o.key !== 'no_blame'), { key: 'no_blame', what: 'Neutral wording', weight: 0 }],
    });
    expect((readded.question.criteria as Record<string, unknown>).no_blame).toBe('Neutral wording');
  });

  it('turns documents on and off together with the evidence state field', () => {
    const spec = noul();
    const on = applySimpleForm(spec, { ...form(spec), needsDocuments: true });
    expect(on.evidence).toEqual({ requires: 'required' });
    expect(on.state_fields).toEqual(['assistant_response', 'evidence']);
    expect(isSavable(checkSpecDocument(on))).toBe(true);
    const off = applySimpleForm(on, { ...form(on), needsDocuments: false });
    expect(off.evidence).toEqual({ requires: 'none' });
    expect(off.state_fields).toEqual(['assistant_response']);
  });

  it('fills in and takes back the recommended corroborators with "Allow this check to block"', () => {
    const spec = noul();
    spec.outcomes.requires_corroboration = ['groundedness'];
    const on = applySimpleForm(spec, { ...form(spec), allowBlock: true });
    expect(on.outcomes.block_eligible).toBe(true);
    expect(on.outcomes.requires_corroboration).toEqual(['groundedness', ...RECOMMENDED_CORROBORATORS]);
    const off = applySimpleForm(on, { ...form(on), allowBlock: false });
    expect(off.outcomes.block_eligible).toBe(false);
    expect(off.outcomes.requires_corroboration).toEqual(['groundedness']);
  });

  it('writes invert only when "No" is the problem, keeping an explicit false', () => {
    const spec = noul();
    const inverted = applySimpleForm(spec, { ...form(spec), problemWhen: 'no' });
    expect(inverted.signal).toEqual({ invert: true });
    expect(applySimpleForm(inverted, { ...form(inverted), problemWhen: 'yes' }).signal).toEqual({ invert: false });
  });

  it('switches between Yes/No and Pick one, carrying the words and giving a savable spec', () => {
    const spec = noul();
    const asChoice = switchAnswerType(form(spec), 'pick_one');
    expect(asChoice.options).toEqual([
      { key: 'no_problem', what: 'No date is promised', weight: 0 },
      { key: 'problem_found', what: 'A date is promised', weight: 1 },
    ]);
    const choiceSpec = applySimpleForm(spec, asChoice);
    expect(choiceSpec.question).toEqual({
      type: 'choice',
      instructions: spec.question.instructions,
      criteria: { no_problem: 'No date is promised', problem_found: 'A date is promised' },
    });
    expect(choiceSpec.signal).toEqual({ risk_weights: { problem_found: 1 } });
    expect(isSavable(checkSpecDocument(choiceSpec))).toBe(true);

    const back = switchAnswerType(form(choiceSpec), 'yes_no');
    expect([back.yes, back.no, back.problemWhen]).toEqual(['A date is promised', 'No date is promised', 'yes']);
    const noulSpec = applySimpleForm(choiceSpec, back);
    expect(noulSpec.question).toEqual({ ...spec.question });
    expect(noulSpec.signal).toEqual({});
    expect(isSavable(checkSpecDocument(noulSpec))).toBe(true);
  });

  it('returns a spec it cannot read unchanged', () => {
    const score = { ...noul(), question: { type: 'score', instructions: 'How rude?', criteria: ['Polite', 'Rude'] } } as unknown as EvaluatorSpec;
    expect(applySimpleForm(score, form(noul()))).toBe(score);
  });
});

describe('optionKeyProblem', () => {
  it('checks the schema pattern, reserved keys and duplicates', () => {
    expect(optionKeyProblem('blames_customer', [])).toBeNull();
    expect(optionKeyProblem('No', [])).toMatch(/lower-case/);
    expect(optionKeyProblem('ab', [])).toMatch(/3 to 64/);
    expect(optionKeyProblem('other', [])).toMatch(/says nothing/);
    expect(optionKeyProblem('dup_key', ['dup_key'])).toMatch(/already/);
  });
});
