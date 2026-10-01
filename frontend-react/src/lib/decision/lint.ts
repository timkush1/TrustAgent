/**
 * Evaluator validation beyond the JSON Schema (SPEC §2.8): the hard rules (severity "error") and
 * the lints (severity "warning"), shown in Studio. Built-in templates must be lint-clean. Pinned by
 * contracts/vectors/lint; backend-go/internal/decision/lint.go is the reference (P4-PLAN D54) and
 * adds the JSON Schema checks the server runs before saving.
 *
 * The SPEC names two hard-rule codes (unresolved_state_reference, no_id_reference) and every lint
 * id; the remaining hard rules use the codes defined here. Three lints have no SPEC definition beyond
 * their id; their rules are documented where they are checked.
 */
import type { Lint } from '@/types/api';
import { DETECTOR_IDS } from './detectors';
import { EPS } from './floats';
import { parseSpec, type Spec } from './spec';
import { codePointLength } from './text';

/** Options for checks that need more than the spec. */
export interface LintOptions {
  /**
   * Evaluator ids in the registry. When given, every requires_corroboration and implies id must be
   * in it (error unknown_evaluator_reference).
   */
  registry?: Iterable<string>;
  /** Evaluator ids attached to the route. When given, a referenced id that is not attached is a warning. */
  attached?: Iterable<string>;
  /** Route threshold overrides, merged before the threshold order check (an explicit null overrides). */
  thresholds?: { warn_at?: number | null; critical_at?: number | null; escalate_from?: number | null };
}

/** Hard-rule codes (severity error). */
export const ERROR_CODES = [
  'unresolved_state_reference',
  'no_id_reference',
  'unknown_risk_weight_option',
  'risk_level_out_of_range',
  'threshold_order',
  'unknown_evaluator_reference',
  'unknown_detector',
] as const;

/** Lint ids (severity warning), SPEC §2.8. */
export const LINT_CODES = [
  'missing_escape_option',
  'non_descriptive_key',
  'double_negative',
  'counting_or_arithmetic',
  'near_duplicate_options',
  'numeric_only_score_levels',
  'long_instructions',
  'score_used_for_policy',
] as const;

/** Warning for a referenced evaluator that the route does not attach. */
export const NOT_ATTACHED = 'reference_not_attached';

const MAX_INSTRUCTIONS = 600;
const ESCAPE_KEY = /^(no_|nothing_|not_|too_short|too_generic)/;
const ESCAPE_WHAT = /greeting|too short|nothing to|not about|no .*(subject|facts|content)/i;
const DOUBLE_NEGATIVE = /\bnot\b.*\b(no|not|never|without)\b/;
const COUNTING = /more than|at least|fewer than|how many|[0-9]/;
const QUESTION_ID = /\b[a-z][a-z0-9_]{1,47}__v[0-9]+\b/;
const BACKTICKED = /`([^`]*)`/g;
/** Words that say nothing about an option on their own. */
const GENERIC_WORDS = new Set(['option', 'choice', 'answer', 'item', 'value', 'class', 'category', 'label', 'type', 'case', 'other', 'misc', 'level', 'result']);

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Every string of a text tree (strings, arrays and objects), in document order, with its path. */
function texts(v: unknown, path: string, out: [string, string][] = []): [string, string][] {
  if (typeof v === 'string') out.push([path, v]);
  else if (Array.isArray(v)) v.forEach((e, i) => texts(e, `${path}[${i}]`, out));
  else if (isObj(v)) for (const [k, e] of Object.entries(v)) texts(e, `${path}.${k}`, out);
  return out;
}

/** The description of a choice option: its `what`, or the option text itself. */
function optionWhat(option: unknown): string {
  if (isObj(option) && typeof option.what === 'string') return option.what;
  return texts(option, '')
    .map(([, t]) => t)
    .join(' ');
}

const hasNotFor = (option: unknown) => isObj(option) && typeof option.not_for === 'string' && option.not_for !== '';

/** Lowercase [a-z0-9]+ tokens. */
const tokens = (s: string) => new Set(s.toLowerCase().match(/[a-z0-9]+/g) ?? []);

function jaccard(a: Set<string>, b: Set<string>): number {
  let common = 0;
  for (const t of a) if (b.has(t)) common++;
  const union = a.size + b.size - common;
  return union === 0 ? 0 : common / union;
}

/**
 * Validates an evaluator spec: hard rules and lints, errors first. The spec must parse (parseSpec);
 * a document that does not is reported as a single error with code "invalid_spec".
 */
export function lintSpec(doc: unknown, options: LintOptions = {}): Lint[] {
  let spec: Spec;
  try {
    spec = parseSpec(doc);
  } catch (err) {
    return [{ code: 'invalid_spec', severity: 'error', path: '', message: (err as Error).message }];
  }
  const errors: Lint[] = [];
  const warnings: Lint[] = [];
  const error = (code: string, path: string, message: string) => errors.push({ code, severity: 'error', path, message });
  const warn = (code: string, path: string, message: string) => warnings.push({ code, severity: 'warning', path, message });

  const question = (spec.raw as unknown as Obj).question as Obj;
  const questionTexts = [...texts(question.instructions, 'question.instructions'), ...texts(question.criteria, 'question.criteria')];
  const instructionTexts = texts(question.instructions, 'question.instructions');

  // Hard rule: backticked references resolve to a declared state field or to rules.<param>.
  for (const [path, text] of questionTexts) {
    for (const m of text.matchAll(BACKTICKED)) {
      const ref = m[1];
      const param = ref.startsWith('rules.') ? ref.slice('rules.'.length) : null;
      const ok = param === null ? spec.stateFields.includes(ref) : spec.stateFields.includes('rules') && spec.params.has(param);
      if (!ok) error('unresolved_state_reference', path, `\`${ref}\` is neither a declared state field nor rules.<declared param>`);
    }
  }
  // Hard rule: question ids never appear in text (questions are independent).
  for (const [path, text] of questionTexts) {
    const m = QUESTION_ID.exec(text);
    if (m !== null) error('no_id_reference', path, `mentions the question id ${m[0]}; questions are answered independently`);
  }

  const criteria = question.criteria;
  const optionKeys = spec.question.type === 'choice' && isObj(criteria) ? Object.keys(criteria) : [];
  if (spec.signal.riskWeights !== null && spec.question.type === 'choice') {
    for (const k of spec.signal.riskWeights.keys()) {
      if (!optionKeys.includes(k)) error('unknown_risk_weight_option', `signal.risk_weights.${k}`, `${k} is not an option of the question`);
    }
  }
  if (spec.signal.riskLevels !== null && spec.question.type === 'score' && Array.isArray(criteria)) {
    for (const l of spec.signal.riskLevels) {
      if (l >= criteria.length) error('risk_level_out_of_range', 'signal.risk_levels', `level ${l} does not exist: the question has ${criteria.length} levels`);
    }
  }

  // Hard rule: escalate_from < warn_at <= critical_at, after the route overrides are merged.
  const th = options.thresholds ?? {};
  const warnAt = th.warn_at ?? spec.outcomes.warnAt;
  const criticalAt = th.critical_at !== undefined ? th.critical_at : spec.outcomes.criticalAt;
  const escalateFrom = th.escalate_from !== undefined ? th.escalate_from : spec.escalation.escalateFrom;
  if (escalateFrom !== null && !(escalateFrom < warnAt - EPS)) {
    error('threshold_order', 'escalation.escalate_from', `escalate_from ${escalateFrom} must be below warn_at ${warnAt}`);
  }
  if (criticalAt !== null && !(warnAt <= criticalAt + EPS)) {
    error('threshold_order', 'outcomes.critical_at', `critical_at ${criticalAt} must be at least warn_at ${warnAt}`);
  }

  // Hard rule: referenced evaluators exist; a warning when the route does not attach them.
  const registry = options.registry === undefined ? null : new Set(options.registry);
  const attached = options.attached === undefined ? null : new Set(options.attached);
  const references: [string, readonly string[]][] = [
    ['outcomes.requires_corroboration', spec.outcomes.requiresCorroboration],
    ['outcomes.implies', spec.outcomes.implies],
  ];
  for (const [path, ids] of references) {
    for (const x of ids) {
      if (x.startsWith('det:')) {
        if (!(DETECTOR_IDS as readonly string[]).includes(x.slice(4))) error('unknown_detector', path, `${x} is not a detector`);
        continue;
      }
      if (registry !== null && !registry.has(x)) error('unknown_evaluator_reference', path, `${x} is not a known evaluator`);
      else if (attached !== null && !attached.has(x)) warn(NOT_ATTACHED, path, `${x} is not attached to this route, so it can never agree`);
    }
  }

  // Lints.
  if (spec.question.type === 'choice' && isObj(criteria)) {
    const entries = Object.entries(criteria);
    if (entries.length >= 3 && !entries.some(([k, o]) => ESCAPE_KEY.test(k) || ESCAPE_WHAT.test(optionWhat(o)))) {
      warn('missing_escape_option', 'question.criteria', 'add an option for answers with nothing to judge (a greeting, a very short reply)');
    }
    // non_descriptive_key: no word of three or more letters besides generic ones (option_a, choice_2).
    for (const [k] of entries) {
      const words = k.split(/[_0-9]+/).filter((w) => w !== '');
      if (!words.some((w) => w.length >= 3 && !GENERIC_WORDS.has(w))) {
        warn('non_descriptive_key', `question.criteria.${k}`, `${k} does not say what the option means; the key is part of the question`);
      }
    }
    for (let i = 0; i < entries.length; i++) {
      for (let j = i + 1; j < entries.length; j++) {
        const [ka, a] = entries[i];
        const [kb, b] = entries[j];
        if (hasNotFor(a) || hasNotFor(b)) continue;
        if (jaccard(tokens(optionWhat(a)), tokens(optionWhat(b))) > 0.5) {
          warn('near_duplicate_options', `question.criteria.${kb}`, `${ka} and ${kb} are described almost the same way; add not_for to separate them`);
        }
      }
    }
  }
  for (const [path, text] of instructionTexts) {
    if (DOUBLE_NEGATIVE.test(text)) warn('double_negative', path, 'rephrase without a double negative');
    if (COUNTING.test(text)) warn('counting_or_arithmetic', path, 'the decision model is weak at counting and arithmetic; consider escalation.numeric_or_date');
  }
  if (codePointLength(instructionTexts.map(([, t]) => t).join('')) > MAX_INSTRUCTIONS) {
    warn('long_instructions', 'question.instructions', `instructions over ${MAX_INSTRUCTIONS} characters; move detail into the options`);
  }
  if (spec.question.type === 'score') {
    // numeric_only_score_levels: a level described by digits and punctuation only.
    if (Array.isArray(criteria)) {
      criteria.forEach((level, i) => {
        if (!/\p{L}/u.test(texts(level, '').map(([, t]) => t).join(' '))) {
          warn('numeric_only_score_levels', `question.criteria[${i}]`, 'describe each level in words, not only a number');
        }
      });
    }
    // score_used_for_policy: every attached evaluator drives policy, and score answers are poorly calibrated.
    warn('score_used_for_policy', 'question.type', 'score answers are poorly calibrated; prefer noul or choice for policy decisions');
  }
  return [...errors, ...warnings];
}

/** The distinct codes of lintSpec's result, sorted: the shape of a lint vector's expected value. */
export function lintCodes(results: readonly Lint[]): { errors: string[]; lints: string[] } {
  const codes = (severity: Lint['severity']) => [...new Set(results.filter((r) => r.severity === severity).map((r) => r.code))].sort();
  return { errors: codes('error'), lints: codes('warning') };
}
