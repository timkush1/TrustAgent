/**
 * Checks an evaluator spec in the browser as Studio edits it, the way the gateway's
 * `POST /api/evaluators/validate` does (Go `decision.ValidateSpec` is the reference, P4-PLAN D54):
 * the JSON Schema (evaluator.v1, or evaluator-draft.v1 for a draft) through the precompiled
 * validator, then the hard rules and lints of `lintSpec` whenever the document parses as a spec, so
 * a schema error does not hide them. A document that passes the schema but not the spec parser is
 * the single hard error `invalid_spec`.
 *
 * `checkSpecJson` does the same for the JSON tab's text and places every problem on the range it
 * concerns (the key of an unknown field, the value of a wrong one, the object of a missing one).
 * The server stays the authority on save; this gives instant feedback without a round trip.
 */
import { lintSpec, type LintOptions } from '@/lib/decision';
import type { Lint } from '@/types/api';
import { validateEvaluatorDraft, validateEvaluatorSpec } from './evaluatorValidator.gen';
import { fromPointer, lineCol, scanJson, spanAt, toPointer } from './jsonSpans';

interface AjvError {
  instancePath: string;
  schemaPath: string;
  keyword: string;
  message?: string;
  params: Record<string, unknown>;
}

type ValidateFn = ((data: unknown) => boolean) & { errors?: AjvError[] | null };

const validators = {
  spec: validateEvaluatorSpec as unknown as ValidateFn,
  draft: validateEvaluatorDraft as unknown as ValidateFn,
};

/** A schema finding (or a JSON syntax problem): always an error. */
export interface SchemaProblem {
  /** JSON pointer of the value ("/outcomes/warn_at"); "" for the document. */
  pointer: string;
  /** The same place in the gateway's words ("outcomes.warn_at", "criteria[0]"). */
  path: string;
  message: string;
  /** The underline goes on the member's key rather than its value (an unknown or misnamed field). */
  atKey: boolean;
}

export interface SpecCheck {
  /** JSON Schema findings (empty when the schema accepts the document). */
  errors: SchemaProblem[];
  /** Hard rules (severity error), then lints (severity warning), as lintSpec reports them. */
  lints: Lint[];
}

export interface CheckOptions extends LintOptions {
  /** Check an evaluator-draft.v1 document (Studio's AI draft) instead of evaluator.v1. */
  draft?: boolean;
}

/** The evaluator-draft.v1 members that are not part of a spec (Go draftOnly). */
const DRAFT_ONLY = ['rationale', 'split_suggestions', 'test_samples'] as const;
/** The placeholder id a draft is linted with (Go DraftID). */
export const DRAFT_ID = 'studio_draft';
/** At most this many schema findings per document (Go maxSchemaFindings). */
const MAX_FINDINGS = 50;
/** Deeper documents are refused before the schema runs (Go maxSpecDepth). */
const MAX_DEPTH = 32;
/** The question types in the order of evaluator.v1's `question.oneOf` branches. */
const QUESTION_BRANCHES = ['noul', 'choice', 'score'] as const;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** "question.criteria.no_blame", "criteria[0]": the gateway's path words for a pointer. */
export function readablePath(pointer: string, doc: unknown): string {
  let out = '';
  let cur: unknown = doc;
  for (const seg of fromPointer(pointer)) {
    if (Array.isArray(cur)) {
      out += `[${seg}]`;
      cur = cur[Number(seg)];
      continue;
    }
    out += out ? `.${seg}` : seg;
    cur = isObj(cur) ? cur[seg] : undefined;
  }
  return out;
}

/** The JSON pointer of a lintSpec path ("question.criteria.no_blame", "outcomes.implies[1]"). */
export function lintPointer(path: string): string {
  if (!path) return '';
  const segments: string[] = [];
  for (const part of path.split('.')) {
    const m = /^([^[]*)((?:\[\d+\])*)$/.exec(part);
    if (!m) {
      segments.push(part);
      continue;
    }
    if (m[1]) segments.push(m[1]);
    for (const idx of m[2].matchAll(/\[(\d+)\]/g)) segments.push(idx[1]);
  }
  return toPointer(segments);
}

function depthOver(v: unknown, depth: number, path: string[]): string[] | null {
  if (Array.isArray(v) || isObj(v)) {
    if (depth >= MAX_DEPTH) return path;
    const entries: [string, unknown][] = Array.isArray(v) ? v.map((e, i) => [String(i), e]) : Object.entries(v).sort(([a], [b]) => (a < b ? -1 : 1));
    for (const [k, e] of entries) {
      const found = depthOver(e, depth + 1, [...path, k]);
      if (found) return found;
    }
  }
  return null;
}

function list(values: unknown): string {
  return (Array.isArray(values) ? values : [values]).map((v) => JSON.stringify(v)).join(', ');
}

function messageOf(err: AjvError): string {
  switch (err.keyword) {
    case 'enum':
      return `must be one of: ${list(err.params.allowedValues)}`;
    case 'const':
      return `must be ${JSON.stringify(err.params.allowedValue)}`;
    case 'additionalProperties':
      return `unknown field '${String(err.params.additionalProperty)}'`;
    case 'required':
      return `is missing '${String(err.params.missingProperty)}'`;
    case 'propertyNames':
      return `invalid key '${String(err.params.propertyName)}'`;
    case 'type':
      return `must be ${String(err.params.type).replace(/,/g, ' or ')}`;
    case 'anyOf':
      return 'must be text, a list of texts, or a map of texts';
    case 'oneOf':
      return err.instancePath === '/question' ? `type must be one of: ${list(QUESTION_BRANCHES)}` : 'must match exactly one of the allowed shapes';
    case 'contains':
      return "must include 'evidence' when the check reads the app's documents";
    case 'not':
      return err.instancePath === '/signal' ? 'a yes/no question takes no risk_weights or risk_levels' : 'uses a value that is not allowed here';
    case 'minLength':
      return err.params.limit === 1 ? 'must not be empty' : `must be at least ${String(err.params.limit)} characters`;
    case 'maxLength':
      return `must be at most ${String(err.params.limit)} characters`;
    default:
      return err.message ?? 'is not valid';
  }
}

/**
 * The errors worth showing (ajv with allErrors reports every alternative it tried):
 * - only the `question` branch the document chose, not the other two;
 * - at a value that must be one of several shapes (an `anyOf`, such as evaluator text), the
 *   alternatives' "wrong type" findings go: the deeper findings explain it, or else one summary;
 * - no `if`/`then` or `oneOf` summaries when the precise finding is there; no duplicates.
 */
function specific(errors: readonly AjvError[], doc: unknown): AjvError[] {
  const type = isObj(doc) && isObj(doc.question) ? doc.question.type : undefined;
  const branch = QUESTION_BRANCHES.indexOf(type as (typeof QUESTION_BRANCHES)[number]);
  const kept = errors.filter((e) => {
    const m = /\/properties\/question\/oneOf\/(\d+)\//.exec(e.schemaPath);
    // An unknown type is reported once, by the oneOf summary.
    return !m || (branch >= 0 && Number(m[1]) === branch);
  });
  const anyOfAt = new Set(kept.filter((e) => e.keyword === 'anyOf').map((e) => e.instancePath));
  const deeper = (path: string) => kept.some((e) => e.instancePath.startsWith(`${path}/`));
  const containsAt = new Set(kept.filter((e) => e.keyword === 'contains').map((e) => e.instancePath));
  const otherThanType = (path: string) => kept.some((e) => e.instancePath === path && e.keyword !== 'type' && e.keyword !== 'anyOf');
  const result = kept.filter((e) => {
    if (e.keyword === 'if') return false;
    if (e.keyword === 'oneOf') return branch < 0;
    if (e.keyword === 'not') return !kept.some((o) => o.keyword === 'propertyNames' && o.instancePath === e.instancePath);
    // `contains` tries every item; their mismatches are not findings of their own.
    if (e.keyword === 'const' && containsAt.has(e.instancePath.slice(0, e.instancePath.lastIndexOf('/')))) return false;
    if (anyOfAt.has(e.instancePath)) {
      if (e.keyword === 'type') return false;
      if (e.keyword === 'anyOf') return !deeper(e.instancePath) && !otherThanType(e.instancePath);
    }
    return true;
  });
  const seen = new Set<string>();
  return result.filter((e) => {
    const key = `${e.instancePath}|${messageOf(e)}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function schemaProblems(doc: unknown, draft: boolean): SchemaProblem[] {
  const tooDeep = depthOver(doc, 1, []);
  if (tooDeep) {
    const pointer = toPointer(tooDeep);
    return [{ pointer, path: readablePath(pointer, doc), message: `is nested more than ${MAX_DEPTH} levels deep`, atKey: false }];
  }
  const validate = draft ? validators.draft : validators.spec;
  if (validate(doc)) return [];
  return specific(validate.errors ?? [], doc)
    .slice(0, MAX_FINDINGS)
    .map((err) => {
      let pointer = err.instancePath;
      let atKey = false;
      if (err.keyword === 'additionalProperties') {
        pointer += toPointer([String(err.params.additionalProperty)]);
        atKey = true;
      } else if (err.keyword === 'propertyNames') {
        pointer += toPointer([String(err.params.propertyName)]);
        atKey = true;
      }
      return { pointer, path: readablePath(pointer, doc), message: messageOf(err), atKey };
    });
}

/** A draft as a spec document: the draft-only members go, the placeholder id and version 1 come in (Go draftSpec). */
export function draftAsSpec(doc: Obj): Obj {
  const out: Obj = { ...doc };
  for (const k of DRAFT_ONLY) delete out[k];
  return { ...out, id: DRAFT_ID, version: 1 };
}

/** Validates a parsed evaluator document: schema findings, then hard rules and lints. */
export function checkSpecDocument(doc: unknown, options: CheckOptions = {}): SpecCheck {
  const { draft = false, ...lintOptions } = options;
  const errors = schemaProblems(doc, draft);
  if (errors.length > 0 && errors[0].message.startsWith('is nested more than')) return { errors, lints: [] };
  if (draft && !isObj(doc)) return { errors, lints: [] };
  let lints = lintSpec(draft ? draftAsSpec(doc as Obj) : doc, lintOptions);
  // The parser's refusal is reported only when the schema found nothing more precise (Go ValidateSpec).
  if (errors.length > 0) lints = lints.filter((l) => l.code !== 'invalid_spec');
  return { errors, lints };
}

/** One problem as Studio lists it: severity, code (`schema`, `syntax` or the lint code), message and place. */
export interface SpecProblem {
  severity: 'error' | 'warning';
  code: string;
  message: string;
  /** The gateway's path words ("outcomes.warn_at"); "" for the document. */
  path: string;
  line?: number;
  col?: number;
}

/** A check's findings as one list: schema errors, then hard rules, then lints. */
export function specProblems(check: SpecCheck): SpecProblem[] {
  return [
    ...check.errors.map((e) => ({ severity: 'error' as const, code: 'schema', message: e.message, path: e.path })),
    ...check.lints.map((l) => ({ severity: l.severity, code: l.code, message: l.message, path: l.path })),
  ];
}

/** Whether a check leaves the spec savable: no schema error and no hard rule broken. */
export function isSavable(check: SpecCheck): boolean {
  return check.errors.length === 0 && check.lints.every((l) => l.severity !== 'error');
}

/** One problem of the JSON tab, placed on its range of the text. */
export interface JsonProblem {
  severity: 'error' | 'warning';
  /** `syntax`, `schema`, or the lint code. */
  code: string;
  message: string;
  /** The gateway's path words ("outcomes.warn_at"); "" for the document or a syntax error. */
  path: string;
  /** UTF-16 offsets, half-open, and the 1-based line and column of `from`. */
  from: number;
  to: number;
  line: number;
  col: number;
}

export interface JsonCheck {
  problems: JsonProblem[];
  /** The parsed document when the text is valid JSON. */
  data: unknown;
  /** Whether the text is valid JSON at all. */
  parsed: boolean;
}

/** Checks the JSON tab's text: syntax, then schema, hard rules and lints, each on its range. */
export function checkSpecJson(text: string, options: CheckOptions = {}): JsonCheck {
  const scan = scanJson(text);
  const place = (from: number, to: number) => ({ from, to: Math.max(to, from), ...lineCol(text, from) });
  if (!scan.ok) {
    const at = Math.min(scan.offset, text.length);
    return {
      parsed: false,
      data: undefined,
      problems: [{ severity: 'error', code: 'syntax', message: scan.message, path: '', ...place(at, Math.min(at + 1, text.length)) }],
    };
  }
  const data = JSON.parse(text) as unknown;
  const check = checkSpecDocument(data, options);
  // A collection spans its whole body: underline its first character only.
  const range = (pointer: string, atKey: boolean) => {
    const { from, to } = spanAt(scan.spans, pointer, atKey);
    const c = text[from];
    return c === '{' || c === '[' ? place(from, from + 1) : place(from, to);
  };
  const problems: JsonProblem[] = [
    ...check.errors.map((e) => ({ severity: 'error' as const, code: 'schema', message: e.message, path: e.path, ...range(e.pointer, e.atKey) })),
    ...check.lints.map((l) => ({ severity: l.severity, code: l.code, message: l.message, path: l.path, ...range(lintPointer(l.path), false) })),
  ];
  return { parsed: true, data, problems };
}
