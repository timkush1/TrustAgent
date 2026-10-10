/**
 * How the TypeScript port runs each golden vector kind (SPEC §7.6), mirroring
 * backend-go/internal/decision/vectors/kinds.go. The input and expected shape of every kind is
 * documented in contracts/vectors/README.md.
 */
import type { DecideInput, EvaluatorSpec } from '@/lib/contracts/types.gen';
import {
  CRITICAL_MIN_PRECISION_LB,
  CRITICAL_MIN_PREDICTED_POS,
  CRITICAL_NULL_REASON,
  deriveQuality,
  f1Of,
  fitCalibration,
  fitIsotonic,
  fitPlatt,
  isotonicTable,
  maxF1Threshold,
  minThresholdAtPrecision,
  OBJECTIVE_MAX_F1,
  OBJECTIVE_MIN_THRESHOLD_AT_PRECISION,
  plattTable,
  precisionOf,
  QUICK_DEFAULT_TARGET_PRECISION,
  quickCalibrate,
  recallOf,
  WILSON_Z95,
  wilsonCriticalAt,
  wilsonInterval,
  type LabeledScore,
  type LabelSourceCounts,
  type QuickCalibrationRequest,
} from '../calibrate';
import { applyCalibration, checkCalibrationTable } from '../calibration';
import { compile, defaultBudget, FACTUAL_CLAIMS_REF, type Budget, type CompileInputs, type CompileRoute } from '../compile';
import { decide } from '../decide';
import { detectOne, type DetectorInput } from '../detectors';
import { extractEvidence, forwardBody, type ExtractFormat } from '../evidence';
import { canonicalize, sha256Hex } from '../jcs';
import { lintCodes, lintSpec } from '../lint';
import { normalize, type RawAnswerInput } from '../normalize';
import { parseSpec, questionBody, questionId, specHashes, type Spec } from '../spec';
import { builtinSpec } from '../templates';
import { countTokens, est } from '../tokens';
import { costKind } from './cost';
import { pkceKind } from './pkce';

/** One contracts/vectors/<kind>/<name>.json file (contracts/schema/vector.v1.schema.json). */
export interface Vector {
  vector: string;
  kind: string;
  name: string;
  status: string;
  description?: string;
  generated_by?: string;
  input: unknown;
  expected: unknown;
  tolerance?: number;
}

/** How the TS port handles one kind. */
export interface VectorKind {
  /** Computes the output for a vector input; absent when the kind is skipped. */
  run?: (input: unknown) => unknown;
  /** Why the TS runner does not run this kind (yet). */
  skip?: string;
  /** What the Go reference runs of this kind, for the conformance report. */
  go: string;
}

type Obj = Record<string, unknown>;

/** The decoded input as an object with only the allowed members (Go's DisallowUnknownFields). */
function fields(input: unknown, allowed: readonly string[]): Obj {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) throw new Error('vector input must be an object');
  for (const k of Object.keys(input)) if (!allowed.includes(k)) throw new Error(`unknown input member ${JSON.stringify(k)}`);
  return input as Obj;
}

/** The evaluator of a question_hash or normalize vector: a built-in template ref or an inline spec. */
function resolveSpec(x: Obj): Spec {
  if (x.template !== undefined && x.spec !== undefined) throw new Error('give either template or spec, not both');
  if (typeof x.template === 'string') return template(x.template);
  if (x.spec !== undefined) return parseSpec(x.spec);
  throw new Error('missing template or spec');
}

function template(ref: string): Spec {
  const spec = builtinSpec(ref);
  if (spec === undefined) throw new Error(`template ${ref}: no such built-in evaluator`);
  return spec;
}

/** canonical: {value} -> {jcs, sha256} */
function runCanonical(input: unknown) {
  const x = fields(input, ['value']);
  return { jcs: canonicalize(x.value), sha256: sha256Hex(x.value) };
}

/** tokens: {value, cpt} -> {narrow, wide, est} */
function runTokens(input: unknown) {
  const x = fields(input, ['value', 'cpt']);
  return countTokens(x.value, x.cpt as number);
}

/** question_hash: {template|spec, cpt} -> {question_id, spec_sha256, question_sha256, calibration_key, question_tokens} */
function runQuestionHash(input: unknown) {
  const x = fields(input, ['template', 'spec', 'cpt']);
  const spec = resolveSpec(x);
  return { question_id: questionId(spec), ...specHashes(spec), question_tokens: est(questionBody(spec), x.cpt as number) };
}

/** normalize: {template|spec, answer} -> {error, probabilities, risk_raw, top, vendor_confidence, confidence_source, anomalies} */
function runNormalize(input: unknown) {
  const x = fields(input, ['template', 'spec', 'answer']);
  const n = normalize(x.answer as RawAnswerInput | null, resolveSpec(x));
  if (n.error !== null) {
    return { error: n.error, probabilities: null, risk_raw: null, top: null, vendor_confidence: null, confidence_source: null, anomalies: [] };
  }
  return {
    error: null,
    probabilities: n.probabilities,
    risk_raw: n.riskRaw,
    top: n.top,
    vendor_confidence: n.vendorConfidence,
    confidence_source: n.confidenceSource,
    anomalies: n.anomalies,
  };
}

/** compile: {route, specs?, inputs, budget?} -> Compiled */
function runCompile(input: unknown) {
  const x = fields(input, ['route', 'specs', 'inputs', 'budget']);
  const route = x.route as CompileRoute;
  const specs = new Map<string, Spec>();
  for (const [ref, doc] of Object.entries((x.specs ?? {}) as Record<string, EvaluatorSpec>)) specs.set(ref, parseSpec(doc));
  for (const ref of [FACTUAL_CLAIMS_REF, ...route.evaluators.map((e) => e.ref)]) if (!specs.has(ref)) specs.set(ref, template(ref));
  return compile(route, specs, x.inputs as CompileInputs, (x.budget as Budget | undefined) ?? defaultBudget());
}

/** calibration_apply: {table, x} -> {y} */
function runCalibrationApply(input: unknown) {
  const x = fields(input, ['table', 'x']);
  const table = x.table as number[];
  checkCalibrationTable(table);
  return { y: applyCalibration(table, x.x as number) };
}

/** detectors: {detector, response, evidence, user_request, system_instructions, rules, evidence_meta?, config?} -> {ran, fired, findings} */
function runDetectors(input: unknown) {
  const x = fields(input, ['detector', 'response', 'evidence', 'user_request', 'system_instructions', 'rules', 'evidence_meta', 'config']);
  const evidence = (x.evidence ?? []) as DetectorInput['evidence'];
  const meta = x.evidence_meta as { pre_dedup_items: number; max_marker: number } | undefined;
  const config = x.config as { min_integer: number } | undefined;
  const r = detectOne(x.detector as string, {
    response: (x.response as string) ?? '',
    evidence,
    userRequest: (x.user_request as string) ?? '',
    systemInstructions: (x.system_instructions as string) ?? '',
    rules: (x.rules as Record<string, string> | null) ?? null,
    preDedupItems: meta?.pre_dedup_items ?? evidence.length,
    maxMarker: meta?.max_marker ?? 0,
    minInteger: config?.min_integer,
  });
  return { ran: r.ran, fired: r.fired, findings: r.findings };
}

/** evidence: {api_format, body, settings} -> {items, system_instructions, user_request, pre_dedup_items, max_marker, forwarded_body} */
function runEvidence(input: unknown) {
  const x = fields(input, ['api_format', 'body', 'settings']);
  const body = x.body as Obj;
  const res = extractEvidence(x.api_format as ExtractFormat, body, (x.settings ?? {}) as Obj);
  return {
    items: res.items.map((it) => ({ id: it.id, source: it.source, text: it.text, ...(it.untrusted ? { untrusted: true } : {}) })),
    system_instructions: res.system_instructions,
    user_request: res.user_request,
    pre_dedup_items: res.pre_dedup_items,
    max_marker: res.max_marker,
    forwarded_body: forwardBody(body),
  };
}

/** lint: {spec, attached?} -> {errors, lints} (sorted distinct codes); attached is the route's evaluator ids. */
function runLint(input: unknown) {
  const x = fields(input, ['spec', 'attached']);
  if (x.attached !== undefined && !(Array.isArray(x.attached) && x.attached.every((a) => typeof a === 'string'))) {
    throw new Error('attached must be a list of evaluator ids');
  }
  return lintCodes(lintSpec(x.spec, x.attached === undefined ? {} : { attached: x.attached as string[] }));
}

/** decide: decide-input.v1 -> the decision/v1 core */
function runDecide(input: unknown) {
  fields(input, ['policy', 'evaluators', 'answers', 'question_map', 'excluded', 'calibration', 'meta', 'detectors']);
  return decide(input as DecideInput);
}

/** The labels of a calibrate_fit input: samples as written, or groups expanded in order (positives, then negatives). */
function labelSet(x: Obj): LabeledScore[] {
  if (x.samples !== undefined && x.groups !== undefined) throw new Error('give either samples or groups, not both');
  if (x.samples !== undefined) return x.samples as LabeledScore[];
  if (x.groups === undefined) throw new Error('missing samples or groups');
  const out: LabeledScore[] = [];
  (x.groups as { risk_raw: number; pos: number; neg: number }[]).forEach((g, i) => {
    if (g.pos < 0 || g.neg < 0) throw new Error(`groups[${i}]: negative count`);
    for (let k = 0; k < g.pos; k++) out.push({ risk_raw: g.risk_raw, label: true });
    for (let k = 0; k < g.neg; k++) out.push({ risk_raw: g.risk_raw, label: false });
  });
  return out;
}

/** calibrate_fit: {op, ...} -> the op's output (contracts/vectors/README.md "calibrate_fit"). */
function runCalibrateFit(input: unknown) {
  const op = (input as Obj | null)?.op;
  switch (op) {
    case 'wilson': {
      const x = fields(input, ['op', 'k', 'n']);
      const [lo, hi] = wilsonInterval(x.k as number, x.n as number, WILSON_Z95);
      return { lo, hi };
    }
    case 'isotonic': {
      const labels = labelSet(fields(input, ['op', 'samples', 'groups']));
      if (labels.length === 0) throw new Error('isotonic needs at least one label');
      const blocks = fitIsotonic(labels);
      return { blocks, table: isotonicTable(blocks) };
    }
    case 'platt': {
      const labels = labelSet(fields(input, ['op', 'samples', 'groups']));
      if (labels.length === 0) throw new Error('platt needs at least one label');
      const [platt, warnings] = fitPlatt(labels);
      return { platt, table: plattTable(platt), warnings };
    }
    case 'threshold': {
      const x = fields(input, ['op', 'samples', 'groups', 'objective', 'min_precision']);
      const labels = labelSet(x);
      const min = x.min_precision as number | undefined;
      let c;
      if (x.objective === OBJECTIVE_MAX_F1) c = maxF1Threshold(labels, min ?? 0);
      else if (x.objective === OBJECTIVE_MIN_THRESHOLD_AT_PRECISION) c = minThresholdAtPrecision(labels, min ?? QUICK_DEFAULT_TARGET_PRECISION);
      else throw new Error(`unknown objective ${JSON.stringify(x.objective)}`);
      if (c === null) return { result: null };
      return {
        result: { threshold: c.threshold, tp: c.tp, fp: c.fp, tn: c.tn, fn: c.fn, precision: precisionOf(c), recall: recallOf(c), f1: f1Of(c) },
      };
    }
    case 'critical_at': {
      const x = fields(input, ['op', 'samples', 'groups', 'min_predicted_pos', 'min_precision_lb']);
      const c = wilsonCriticalAt(
        labelSet(x),
        (x.min_predicted_pos as number | undefined) ?? CRITICAL_MIN_PREDICTED_POS,
        (x.min_precision_lb as number | undefined) ?? CRITICAL_MIN_PRECISION_LB,
      );
      return c === null ? { critical_at: null, critical_null_reason: CRITICAL_NULL_REASON } : { critical_at: c, critical_null_reason: null };
    }
    case 'quality': {
      const x = fields(input, ['op', 'snapshot', 'label_source', 'n_pos', 'n_neg', 'family']);
      return {
        quality: deriveQuality(x.snapshot as string, x.label_source as LabelSourceCounts, x.n_pos as number, x.n_neg as number, x.family === true),
      };
    }
    case 'quick': {
      const x = fields(input, ['op', 'labeled', 'objective', 'target_precision', 'current']);
      const { message: _message, ...rest } = quickCalibrate(x as unknown as QuickCalibrationRequest);
      return rest;
    }
    case 'fit': {
      const x = fields(input, ['op', 'samples', 'groups', 'method']);
      return fitCalibration(labelSet(x), (x.method as string | undefined) ?? '');
    }
  }
  throw new Error(`unknown calibrate_fit op ${JSON.stringify(op)}`);
}

/**
 * Every kind of contracts/schema/vector.v1.schema.json. A vector of any other kind fails the run.
 * `go` records what the Go reference runs (backend-go/internal/decision/vectors/kinds.go,
 * internal/evidence TestEvidenceVectors) so the report can compare coverage.
 */
export const KINDS: Readonly<Record<string, VectorKind>> = {
  canonical: { run: runCanonical, go: 'all' },
  tokens: { run: runTokens, go: 'all' },
  question_hash: { run: runQuestionHash, go: 'all' },
  normalize: { run: runNormalize, go: 'all' },
  compile: { run: runCompile, go: 'all' },
  calibration_apply: { run: runCalibrationApply, go: 'all' },
  decide: { run: runDecide, go: 'all' },
  detectors: { run: runDetectors, go: 'all' },
  evidence: { run: runEvidence, go: 'all (internal/evidence, every api_format through llmapi.ParseRequest)' },
  lint: { run: runLint, go: 'all' },
  calibrate_fit: { run: runCalibrateFit, go: 'all' },
  // Filled by their owners (cost: WEB-LANDING LD-1; pkce: WEB-LIVE LV-4).
  cost: costKind,
  pkce: pkceKind,
};

/**
 * The output of a vector input as JSON sees it: the TS value marshalled and parsed again, like Go's
 * runner, so undefined members disappear and -0 becomes 0.
 */
export function runVectorKind(kind: string, input: unknown): unknown {
  const k = Object.hasOwn(KINDS, kind) ? KINDS[kind] : undefined;
  if (k === undefined) throw new Error(`unknown vector kind ${JSON.stringify(kind)}`);
  if (k.run === undefined) throw new Error(`vector kind ${kind} has no TS runner: ${k.skip ?? ''}`);
  return JSON.parse(JSON.stringify(k.run(input)) ?? 'null') as unknown;
}

/** File-level problems: format version, kind matching its directory, name matching its file name, status. */
export function checkVector(v: Vector, dir: string, fileName: string): string[] {
  const problems: string[] = [];
  if (v.vector !== '1') problems.push(`vector is ${JSON.stringify(v.vector)}, want "1"`);
  if (v.kind !== dir) problems.push(`kind ${JSON.stringify(v.kind)} does not match its directory ${JSON.stringify(dir)}`);
  const want = fileName.replace(/\.json$/, '');
  if (v.name !== want) problems.push(`name ${JSON.stringify(v.name)} does not match the file name ${JSON.stringify(want)}`);
  if (v.status !== 'draft' && v.status !== 'frozen') problems.push(`status ${JSON.stringify(v.status)} is neither draft nor frozen`);
  return problems;
}
