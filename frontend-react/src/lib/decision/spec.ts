/**
 * Evaluator specs as compile() and decide() read them (contracts/schema/evaluator.v1.schema.json).
 * Port of backend-go/internal/decision/spec.go.
 *
 * parseSpec keeps the spec as written (`raw`, a well-formed JSON copy) next to a typed view with the
 * schema defaults applied. Hashes and the question sent to the decision model always use `raw`, so a
 * spec round-trips unchanged and hashes never depend on defaults.
 */
import type { EvaluatorSpec, QuestionType, StateField } from '@/lib/contracts/types.gen';
import { sha256Hex } from './jcs';
import { compareUTF16, toWellFormed } from './text';

/** Schema default of evidence.min_chars. */
export const DEFAULT_MIN_CHARS = 40;

export type VerdictRole = 'unsupported' | 'contradicted';
export type EvidenceRequirement = 'required' | 'optional' | 'none';
export type OnError = 'escalate' | 'ignore';

/** A declared rules parameter. */
export interface SpecParam {
  default: string;
  label?: string;
  help?: string;
}

/** A parsed evaluator spec: the typed view with defaults applied, plus the spec as written. */
export interface Spec {
  id: string;
  version: number;
  name: string;
  category: string;
  verdictRole: VerdictRole | null;
  stateFields: readonly string[];
  /** Declared params in document order. */
  params: ReadonlyMap<string, SpecParam>;
  question: { type: QuestionType; instructions: unknown; criteria: unknown };
  evidence: { requires: EvidenceRequirement; minChars: number };
  signal: { invert: boolean; riskWeights: ReadonlyMap<string, number> | null; riskLevels: readonly number[] | null };
  outcomes: {
    warnAt: number;
    criticalAt: number | null;
    minConfidence: number | null;
    blockEligible: boolean;
    requiresCorroboration: readonly string[];
    implies: readonly string[];
  };
  escalation: { escalateFrom: number | null; numericOrDate: boolean; onError: OnError };
  /** The spec exactly as written (well-formed JSON copy). */
  raw: EvaluatorSpec;
}

/** Raised for a document that compile() and decide() cannot use. */
export class SpecError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SpecError';
  }
}

const EVALUATOR_ID = /^[a-z][a-z0-9_]{1,47}$/;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/**
 * A JSON-semantics copy of value: undefined members dropped, keys and strings made well-formed
 * (Go's decoder maps lone surrogates to U+FFFD). Throws for values JSON cannot hold.
 */
export function cloneJSON<T = unknown>(value: unknown): T {
  const clone = (v: unknown): unknown => {
    if (v === null || typeof v === 'boolean') return v;
    if (typeof v === 'string') return toWellFormed(v);
    if (typeof v === 'number') {
      if (!Number.isFinite(v)) throw new SpecError(`${v} is not a JSON number`);
      return v;
    }
    if (Array.isArray(v)) return v.map(clone);
    if (isObj(v)) {
      const entries: [string, unknown][] = [];
      for (const [k, e] of Object.entries(v)) if (e !== undefined) entries.push([toWellFormed(k), clone(e)]);
      return Object.fromEntries(entries);
    }
    throw new SpecError(`${typeof v} is not a JSON value`);
  };
  return clone(value) as T;
}

function num(v: unknown, where: string, id: string): number {
  if (typeof v !== 'number' || !Number.isFinite(v)) throw new SpecError(`evaluator spec ${id}: ${where} must be a number`);
  return v;
}

function optNum(v: unknown, where: string, id: string): number | null {
  return v === undefined || v === null ? null : num(v, where, id);
}

function bool(v: unknown, where: string, id: string): boolean {
  if (v === undefined || v === null) return false;
  if (typeof v !== 'boolean') throw new SpecError(`evaluator spec ${id}: ${where} must be a boolean`);
  return v;
}

function strings(v: unknown, where: string, id: string): string[] {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v) || !v.every((s) => typeof s === 'string')) {
    throw new SpecError(`evaluator spec ${id}: ${where} must be a list of strings`);
  }
  return v as string[];
}

function section(doc: Obj, key: string, id: string): Obj {
  const v = doc[key];
  if (v === undefined || v === null) return {};
  if (!isObj(v)) throw new SpecError(`evaluator spec ${id}: ${key} must be an object`);
  return v;
}

/**
 * Parses an evaluator spec document. It checks what compile() and decide() rely on; the full schema,
 * hard rules and lints belong to validation (lint.ts).
 */
export function parseSpec(doc: unknown): Spec {
  if (!isObj(doc)) throw new SpecError('evaluator spec: not an object');
  const raw = cloneJSON<Obj>(doc);
  const id = raw.id;
  if (typeof id !== 'string' || !EVALUATOR_ID.test(id)) throw new SpecError(`evaluator spec: invalid id ${JSON.stringify(id)}`);
  const version = raw.version;
  if (typeof version !== 'number' || !Number.isInteger(version) || version < 1) {
    throw new SpecError(`evaluator spec ${id}: version must be an integer >= 1`);
  }
  const outcomes = section(raw, 'outcomes', id);
  if (outcomes.warn_at === undefined || outcomes.warn_at === null) {
    throw new SpecError(`evaluator spec ${id}: outcomes.warn_at is required`);
  }
  const question = section(raw, 'question', id);
  const evidence = section(raw, 'evidence', id);
  const signal = section(raw, 'signal', id);
  const escalation = section(raw, 'escalation', id);

  const type = question.type;
  if (type !== 'noul' && type !== 'choice' && type !== 'score') {
    throw new SpecError(`evaluator spec ${id}: unknown question type ${JSON.stringify(type)}`);
  }
  if (type === 'choice' && !isObj(question.criteria)) throw new SpecError(`evaluator spec ${id}: choice criteria must be an object`);
  if (type === 'score' && !Array.isArray(question.criteria)) throw new SpecError(`evaluator spec ${id}: score criteria must be an array`);

  const requires = evidence.requires;
  if (requires !== 'required' && requires !== 'optional' && requires !== 'none') {
    throw new SpecError(`evaluator spec ${id}: unknown evidence.requires ${JSON.stringify(requires)}`);
  }
  const onError = escalation.on_error === undefined || escalation.on_error === null || escalation.on_error === '' ? 'escalate' : escalation.on_error;
  if (onError !== 'escalate' && onError !== 'ignore') {
    throw new SpecError(`evaluator spec ${id}: unknown escalation.on_error ${JSON.stringify(onError)}`);
  }
  const role = raw.verdict_role;
  if (role !== undefined && role !== null && role !== 'unsupported' && role !== 'contradicted') {
    throw new SpecError(`evaluator spec ${id}: unknown verdict_role ${JSON.stringify(role)}`);
  }

  const params = new Map<string, SpecParam>();
  for (const [name, p] of Object.entries(section(raw, 'params', id))) {
    const def = isObj(p) ? (p.default ?? '') : undefined;
    if (typeof def !== 'string') throw new SpecError(`evaluator spec ${id}: params.${name}.default must be a string`);
    params.set(name, { ...(p as Obj), default: def } as SpecParam);
  }

  let riskWeights: Map<string, number> | null = null;
  if (signal.risk_weights !== undefined && signal.risk_weights !== null) {
    if (!isObj(signal.risk_weights)) throw new SpecError(`evaluator spec ${id}: signal.risk_weights must be an object`);
    riskWeights = new Map(Object.entries(signal.risk_weights).map(([k, w]) => [k, num(w, `signal.risk_weights.${k}`, id)]));
  }
  let riskLevels: number[] | null = null;
  if (signal.risk_levels !== undefined && signal.risk_levels !== null) {
    if (!Array.isArray(signal.risk_levels) || !signal.risk_levels.every((l) => Number.isInteger(l))) {
      throw new SpecError(`evaluator spec ${id}: signal.risk_levels must be a list of integers`);
    }
    riskLevels = signal.risk_levels as number[];
  }

  const minChars = evidence.min_chars;
  return {
    id,
    version,
    name: typeof raw.name === 'string' ? raw.name : '',
    category: typeof raw.category === 'string' ? raw.category : '',
    verdictRole: (role ?? null) as VerdictRole | null,
    stateFields: strings(raw.state_fields, 'state_fields', id),
    params,
    question: { type, instructions: question.instructions, criteria: question.criteria },
    evidence: {
      requires,
      minChars: minChars === undefined || minChars === null ? DEFAULT_MIN_CHARS : num(minChars, 'evidence.min_chars', id),
    },
    signal: { invert: bool(signal.invert, 'signal.invert', id), riskWeights, riskLevels },
    outcomes: {
      warnAt: num(outcomes.warn_at, 'outcomes.warn_at', id),
      criticalAt: optNum(outcomes.critical_at, 'outcomes.critical_at', id),
      minConfidence: optNum(outcomes.min_confidence, 'outcomes.min_confidence', id),
      blockEligible: bool(outcomes.block_eligible, 'outcomes.block_eligible', id),
      requiresCorroboration: strings(outcomes.requires_corroboration, 'outcomes.requires_corroboration', id),
      implies: strings(outcomes.implies, 'outcomes.implies', id),
    },
    escalation: {
      escalateFrom: optNum(escalation.escalate_from, 'escalation.escalate_from', id),
      numericOrDate: bool(escalation.numeric_or_date, 'escalation.numeric_or_date', id),
      onError,
    },
    raw: raw as unknown as EvaluatorSpec,
  };
}

/** "<id>@<version>". */
export function specRef(s: Spec): string {
  return `${s.id}@${s.version}`;
}

/** The question id "<id>__v<version>" (SPEC §2.9 compile step 5). */
export function questionId(s: Spec): string {
  return `${s.id}__v${s.version}`;
}

/** The question exactly as the spec writes it: {type, instructions, criteria}. */
export function questionBody(s: Spec): unknown {
  return (s.raw as unknown as Obj).question;
}

/** Whether the spec lists field in state_fields. */
export function hasStateField(s: Spec, field: StateField): boolean {
  return s.stateFields.includes(field);
}

/**
 * The answer keys of the question: choice option keys in UTF-16 order, "0".."n-1" for a score
 * question with n levels, and [] for noul.
 */
export function specOptions(s: Spec): string[] {
  if (s.question.type === 'choice') return Object.keys(s.question.criteria as Obj).sort(compareUTF16);
  if (s.question.type === 'score') return (s.question.criteria as unknown[]).map((_, i) => String(i));
  return [];
}

/** The identities of an evaluator version (SPEC §2.3 evaluator_versions, §2.11). */
export interface SpecHashes {
  /** sha256(JCS(spec)). */
  spec_sha256: string;
  /** sha256(JCS(spec.question)). */
  question_sha256: string;
  /** sha256(JCS({question, signal})), the calibration target key. */
  calibration_key: string;
}

/** The spec's hashes over the spec as written (defaults are not filled in). */
export function specHashes(s: Spec): SpecHashes {
  const raw = s.raw as unknown as Obj;
  return {
    spec_sha256: sha256Hex(raw),
    question_sha256: sha256Hex(raw.question ?? null),
    calibration_key: sha256Hex({ question: raw.question ?? null, signal: raw.signal ?? null }),
  };
}

/** Splits "<id>@<version>". */
export function parseRef(ref: string): { id: string; version: number } {
  const at = ref.indexOf('@');
  const id = at < 0 ? ref : ref.slice(0, at);
  const v = at < 0 ? '' : ref.slice(at + 1);
  if (at < 0 || !EVALUATOR_ID.test(id)) throw new SpecError(`invalid evaluator ref ${JSON.stringify(ref)}`);
  const version = /^[0-9]+$/.test(v) ? Number(v) : NaN;
  if (!Number.isSafeInteger(version) || version < 1 || String(version) !== v) {
    throw new SpecError(`invalid evaluator ref ${JSON.stringify(ref)}: the version must be a positive integer`);
  }
  return { id, version };
}
