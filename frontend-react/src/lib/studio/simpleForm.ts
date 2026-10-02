/**
 * Studio's Simple form over an evaluator spec (SPEC §7.3 step 2): the name, "What should it
 * catch?", the answer type (Yes/No or Pick one; Scale is JSON-only because it calibrates poorly),
 * per option a Problem / Borderline / Fine control (risk weights 1 / 0.5 / 0), "Needs the app's
 * documents", the sensitivity (`warn_at`) and "Allow this check to block", which fills in the
 * recommended corroborators.
 *
 * `readSimpleForm` takes what the form can show from a spec; `applySimpleForm` writes the form back
 * by changing only what differs from the spec's own reading. So reading and writing back an
 * untouched form returns the spec unchanged, member order and every field the form does not know
 * included (Advanced, JSON-only and unknown fields survive any Simple edit).
 */
import type { EvaluatorSpec } from '@/types/api';

export type AnswerType = 'yes_no' | 'pick_one';

/** Risk weights the Problem / Borderline / Fine control sets. */
export const ROLE_WEIGHTS = { problem: 1, borderline: 0.5, fine: 0 } as const;
export type OptionRole = keyof typeof ROLE_WEIGHTS;

/** "Only block when another signal agrees: Contradiction, Number check" (SPEC §7.3). */
export const RECOMMENDED_CORROBORATORS = ['contradiction', 'det:numbers_not_in_evidence'] as const;

/** evaluator.v1 choice option keys (and the reserved ones the schema refuses). */
export const OPTION_KEY_PATTERN = /^[a-z][a-z0-9_]{2,63}$/;
export const RESERVED_OPTION_KEYS: readonly string[] = ['yes', 'no', 'other', 'unknown', 'none', 'option1'];

export interface SimpleOption {
  key: string;
  /** What the option means (its `what`, or its text). */
  what: string;
  /** Its risk weight (0 when the spec gives none). */
  weight: number;
}

export interface SimpleForm {
  name: string;
  /** question.instructions. */
  instructions: string;
  answerType: AnswerType;
  /** Yes/No: what "yes" and "no" mean (criteria.true / criteria.false). */
  yes: string;
  no: string;
  /** Yes/No: which answer is the problem (signal.invert). */
  problemWhen: 'yes' | 'no';
  /** Pick one: the options in order. */
  options: SimpleOption[];
  /** evidence.requires is not "none". */
  needsDocuments: boolean;
  /** outcomes.warn_at. */
  warnAt: number;
  /** outcomes.block_eligible. */
  allowBlock: boolean;
}

export type SimpleRead = { ok: true; form: SimpleForm } | { ok: false; reason: string };

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** The role whose weight this is, or null for any other weight (shown as a custom weight). */
export function roleOf(weight: number): OptionRole | null {
  for (const [role, w] of Object.entries(ROLE_WEIGHTS) as [OptionRole, number][]) if (weight === w) return role;
  return null;
}

function optionText(value: unknown): string | null {
  if (value === null) return '';
  if (typeof value === 'string') return value;
  if (isObj(value) && typeof value.what === 'string') return value.what;
  return null;
}

/** What the Simple form shows for `spec`, or why it cannot (the spec is then edited in Advanced or JSON). */
export function readSimpleForm(spec: unknown): SimpleRead {
  if (!isObj(spec) || !isObj(spec.question) || !isObj(spec.outcomes) || !isObj(spec.evidence)) {
    return { ok: false, reason: 'The spec is incomplete. Fix it in the JSON tab first.' };
  }
  const q = spec.question;
  if (q.type === 'score') return { ok: false, reason: 'This check answers on a scale, which the Simple form does not edit (a scale calibrates poorly). Use the JSON tab.' };
  if (typeof q.instructions !== 'string') return { ok: false, reason: 'The instructions are structured text. Edit them in the JSON tab.' };
  if (typeof spec.name !== 'string' || typeof spec.outcomes.warn_at !== 'number') {
    return { ok: false, reason: 'The spec is incomplete. Fix it in the JSON tab first.' };
  }
  const signal = isObj(spec.signal) ? spec.signal : {};
  const form: SimpleForm = {
    name: spec.name,
    instructions: q.instructions,
    answerType: 'yes_no',
    yes: '',
    no: '',
    problemWhen: signal.invert === true ? 'no' : 'yes',
    options: [],
    needsDocuments: spec.evidence.requires !== 'none',
    warnAt: spec.outcomes.warn_at,
    allowBlock: spec.outcomes.block_eligible === true,
  };
  if (q.type === 'noul') {
    const c = isObj(q.criteria) ? q.criteria : {};
    if (typeof c.true !== 'string' || typeof c.false !== 'string') return { ok: false, reason: 'The answers are structured text. Edit them in the JSON tab.' };
    form.yes = c.true;
    form.no = c.false;
    return { ok: true, form };
  }
  if (q.type !== 'choice' || !isObj(q.criteria)) return { ok: false, reason: 'The question type is not one the form knows. Use the JSON tab.' };
  const weights = isObj(signal.risk_weights) ? signal.risk_weights : {};
  form.answerType = 'pick_one';
  for (const [key, value] of Object.entries(q.criteria)) {
    const what = optionText(value);
    if (what === null) return { ok: false, reason: `Option ${key} is structured text. Edit it in the JSON tab.` };
    const w = weights[key];
    form.options.push({ key, what, weight: typeof w === 'number' ? w : 0 });
  }
  return { ok: true, form };
}

/** A choice option's new value: `what` set on an option object (keeping not_for and examples), else text. */
function withWhat(previous: unknown, what: string): unknown {
  if (isObj(previous)) return { ...previous, what };
  if (previous === null && what === '') return null;
  return what;
}

function questionFor(form: SimpleForm, previous: Obj): Obj {
  if (form.answerType === 'yes_no') {
    const criteria = isObj(previous.criteria) && previous.type === 'noul' ? { ...previous.criteria, true: form.yes, false: form.no } : { true: form.yes, false: form.no };
    return { ...previous, type: 'noul', instructions: form.instructions, criteria };
  }
  const old = isObj(previous.criteria) && previous.type === 'choice' ? previous.criteria : {};
  const criteria: Obj = {};
  for (const o of form.options) criteria[o.key] = withWhat(old[o.key], o.what);
  return { ...previous, type: 'choice', instructions: form.instructions, criteria };
}

function signalFor(form: SimpleForm, previous: Obj, converted: boolean): Obj {
  const out: Obj = { ...previous };
  delete out.risk_levels;
  if (form.answerType === 'yes_no') {
    delete out.risk_weights;
    // Keep an explicit `invert: false`; write `invert` only when it says something.
    if (form.problemWhen === 'no') out.invert = true;
    else if ('invert' in previous) out.invert = false;
    return out;
  }
  // Pick one says which answers are a problem through the weights; a Yes/No `invert` goes with it.
  if (converted) delete out.invert;
  const old = isObj(previous.risk_weights) && !converted ? previous.risk_weights : {};
  const weights: Obj = {};
  for (const o of form.options) {
    // An option without a weight in the spec stays without one while it is Fine.
    if (o.weight !== 0 || o.key in old) weights[o.key] = o.weight;
  }
  out.risk_weights = weights;
  return out;
}

const weightsOf = (f: SimpleForm) => f.options.map((o) => `${o.key}=${o.weight}`).join(',');

function sameOptions(a: readonly SimpleOption[], b: readonly SimpleOption[]): boolean {
  return a.length === b.length && a.every((o, i) => o.key === b[i].key && o.what === b[i].what && o.weight === b[i].weight);
}

/**
 * `spec` with the form written back. Only what the form changed is touched, so an untouched form
 * returns an equal spec; a spec the form cannot read is returned unchanged.
 */
export function applySimpleForm(spec: EvaluatorSpec, form: SimpleForm): EvaluatorSpec {
  const read = readSimpleForm(spec);
  if (!read.ok) return spec;
  const was = read.form;
  const out: Obj = { ...(spec as unknown as Obj) };

  if (form.name !== was.name) out.name = form.name;

  const questionChanged =
    form.answerType !== was.answerType ||
    form.instructions !== was.instructions ||
    (form.answerType === 'yes_no' ? form.yes !== was.yes || form.no !== was.no : !sameOptions(form.options, was.options));
  const converted = form.answerType !== was.answerType;
  const signalChanged = converted || (form.answerType === 'yes_no' ? form.problemWhen !== was.problemWhen : weightsOf(form) !== weightsOf(was));
  if (questionChanged) out.question = questionFor(form, out.question as Obj);
  if (signalChanged) out.signal = signalFor(form, isObj(out.signal) ? out.signal : {}, converted);

  if (form.needsDocuments !== was.needsDocuments) {
    const evidence = { ...(out.evidence as Obj) };
    const fields = Array.isArray(out.state_fields) ? [...(out.state_fields as string[])] : [];
    if (form.needsDocuments) {
      evidence.requires = 'required';
      if (!fields.includes('evidence')) fields.push('evidence');
    } else {
      evidence.requires = 'none';
      const at = fields.indexOf('evidence');
      if (at >= 0) fields.splice(at, 1);
    }
    out.evidence = evidence;
    out.state_fields = fields;
  }

  if (form.warnAt !== was.warnAt) out.outcomes = { ...(out.outcomes as Obj), warn_at: form.warnAt };

  if (form.allowBlock !== was.allowBlock) {
    const outcomes = { ...(out.outcomes as Obj) };
    const current = Array.isArray(outcomes.requires_corroboration) ? (outcomes.requires_corroboration as string[]) : [];
    outcomes.block_eligible = form.allowBlock;
    outcomes.requires_corroboration = form.allowBlock
      ? [...current, ...RECOMMENDED_CORROBORATORS.filter((c) => !current.includes(c))]
      : current.filter((c) => !(RECOMMENDED_CORROBORATORS as readonly string[]).includes(c));
    out.outcomes = outcomes;
  }
  return out as unknown as EvaluatorSpec;
}

/**
 * Converts the form between Yes/No and Pick one, carrying the words over: Yes/No becomes two
 * options (the problem answer and the fine one); Pick one becomes Yes/No from its first problem
 * option and its first fine option.
 */
export function switchAnswerType(form: SimpleForm, to: AnswerType): SimpleForm {
  if (form.answerType === to) return form;
  if (to === 'pick_one') {
    const problem = form.problemWhen === 'yes' ? form.yes : form.no;
    const fine = form.problemWhen === 'yes' ? form.no : form.yes;
    return {
      ...form,
      answerType: 'pick_one',
      options: [
        { key: 'no_problem', what: fine, weight: ROLE_WEIGHTS.fine },
        { key: 'problem_found', what: problem, weight: ROLE_WEIGHTS.problem },
      ],
    };
  }
  const problem = form.options.find((o) => o.weight > 0) ?? form.options[0];
  const fine = form.options.find((o) => o.weight === 0 && o !== problem) ?? form.options.find((o) => o !== problem);
  return { ...form, answerType: 'yes_no', yes: problem?.what ?? '', no: fine?.what ?? '', problemWhen: 'yes', options: [] };
}

/** Why an option key cannot be used, or null. */
export function optionKeyProblem(key: string, others: readonly string[]): string | null {
  if (!OPTION_KEY_PATTERN.test(key)) return 'Use 3 to 64 lower-case letters, digits and underscores, starting with a letter.';
  if (RESERVED_OPTION_KEYS.includes(key)) return `“${key}” says nothing about the answer; name what it means.`;
  if (others.includes(key)) return 'Another option already uses this name.';
  return null;
}
