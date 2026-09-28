/**
 * Checks policy YAML in the browser as the advanced editor types (SPEC §3.3 Policies: "YAML editor
 * for advanced users"): YAML syntax first, then the policy.v1 schema through the precompiled
 * validator, each problem placed on the line and column it concerns. The server's
 * `POST /api/policies/validate` stays the authority (cross-field rules, block readiness); this
 * gives instant feedback without a round trip.
 */
import { isMap, isPair, isScalar, LineCounter, parseDocument, type Document, type Node } from 'yaml';
import validateGenerated from './policyValidator.gen';

interface AjvError {
  instancePath: string;
  keyword: string;
  message?: string;
  params: Record<string, unknown>;
}

type ValidateFn = ((data: unknown) => boolean) & { errors?: AjvError[] | null };

const validatePolicy = validateGenerated as unknown as ValidateFn;

export interface PolicyProblem {
  severity: 'error' | 'warning';
  message: string;
  /** JSON pointer of the value ("/routes/0/mode"), "" for the document or a syntax error. */
  path: string;
  /** Character offsets in the text, half-open. */
  from: number;
  to: number;
  /** 1-based line and column of `from`. */
  line: number;
  col: number;
}

export interface PolicyCheck {
  problems: PolicyProblem[];
  /** The parsed document when the YAML itself is well formed. */
  data: unknown;
}

function segments(pointer: string): (string | number)[] {
  if (!pointer) return [];
  return pointer
    .slice(1)
    .split('/')
    .map((s) => s.replace(/~1/g, '/').replace(/~0/g, '~'))
    .map((s) => (/^\d+$/.test(s) ? Number(s) : s));
}

function readablePath(path: (string | number)[]): string {
  if (path.length === 0) return 'The policy';
  return path.reduce<string>((acc, s) => (typeof s === 'number' ? `${acc}[${s}]` : acc ? `${acc}.${s}` : s), '');
}

/** The node at `path`, or the nearest ancestor that exists, and the key node of an extra property. */
function locate(doc: Document, path: (string | number)[], extraKey?: string): [number, number] {
  if (extraKey !== undefined) {
    const parent = path.length === 0 ? doc.contents : doc.getIn(path, true);
    if (isMap(parent)) {
      const pair = parent.items.find((p) => isPair(p) && isScalar(p.key) && p.key.value === extraKey);
      const key = pair && isPair(pair) ? (pair.key as Node | null) : null;
      if (key?.range) return [key.range[0], key.range[1]];
    }
  }
  for (let n = path.length; n >= 0; n -= 1) {
    const node = (n === 0 ? doc.contents : doc.getIn(path.slice(0, n), true)) as Node | null | undefined;
    if (node && node.range) {
      // A collection spans its whole body: point at its first line only.
      return [node.range[0], isScalar(node) ? node.range[1] : node.range[0] + 1];
    }
  }
  return [0, 0];
}

function message(err: AjvError, path: (string | number)[]): string {
  const where = readablePath(path);
  switch (err.keyword) {
    case 'enum':
      return `${where} must be one of: ${(err.params.allowedValues as unknown[]).map((v) => JSON.stringify(v)).join(', ')}`;
    case 'additionalProperties':
      return `${where} has an unknown field "${String(err.params.additionalProperty)}"`;
    case 'required':
      return `${where} needs "${String(err.params.missingProperty)}"`;
    case 'const':
      return `${where} must be ${JSON.stringify(err.params.allowedValue)}`;
    default:
      return `${where} ${err.message ?? 'is not valid'}`;
  }
}

/** Keeps the most specific errors: combinator summaries go when anything more precise was found. */
function specific(errors: AjvError[]): AjvError[] {
  const summaries = new Set(['anyOf', 'oneOf', 'if', 'not']);
  const precise = errors.filter((e) => !summaries.has(e.keyword));
  const kept = precise.length > 0 ? precise : errors;
  const seen = new Set<string>();
  return kept.filter((e) => {
    const key = `${e.instancePath}|${e.keyword}|${JSON.stringify(e.params)}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function checkPolicyYaml(text: string): PolicyCheck {
  const lineCounter = new LineCounter();
  const doc = parseDocument(text, { lineCounter, prettyErrors: false, uniqueKeys: true });
  const at = (from: number, to: number) => {
    const pos = lineCounter.linePos(from);
    return { from, to: Math.max(to, from), line: pos.line, col: pos.col };
  };

  if (doc.errors.length > 0) {
    return {
      data: null,
      problems: doc.errors.map((e) => ({
        severity: 'error' as const,
        message: e.message.split('\n')[0],
        path: '',
        ...at(e.pos[0], e.pos[1]),
      })),
    };
  }

  const data = doc.toJS({ maxAliasCount: 100 }) as unknown;
  const problems: PolicyProblem[] = doc.warnings.map((w) => ({
    severity: 'warning' as const,
    message: w.message.split('\n')[0],
    path: '',
    ...at(w.pos[0], w.pos[1]),
  }));

  if (!validatePolicy(data)) {
    for (const err of specific(validatePolicy.errors ?? [])) {
      const path = segments(err.instancePath);
      const extra = err.keyword === 'additionalProperties' ? String(err.params.additionalProperty) : undefined;
      const [from, to] = locate(doc, path, extra);
      problems.push({ severity: 'error', message: message(err, path), path: err.instancePath, ...at(from, to) });
    }
  }
  problems.sort((a, b) => a.from - b.from);
  return { problems, data };
}
