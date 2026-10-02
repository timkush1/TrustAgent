/**
 * decide(input) → the decision/v1 core (SPEC §2.9). Port of backend-go/internal/decision/decide.go;
 * pinned by contracts/vectors/decide.
 *
 * The input is decide-input.v1 (contracts/schema/decide-input.v1.schema.json); the output is the
 * part of decision/v1 (GateDecisionView) that decide() owns. The gate adds the trace, route,
 * detector, compile and usage fields.
 */
import type {
  CalibrationQuality,
  CalibrationState,
  CalibrationTable,
  DecideInput,
  DeepAuditClass,
  Degraded,
  EvaluatorLevel,
  EvaluatorResult,
  EvaluatorResultStatus,
  GateAction,
  GateDecisionView,
  GateStatus,
  GateVerdict,
  PolicyMode,
  ThresholdSpace,
  WouldAction,
} from '@/lib/contracts/types.gen';
import { applyCalibration, checkCalibrationTable } from './calibration';
import { FACTUAL_CLAIMS_ID, FACTUAL_CLAIMS_REF } from './compile';
import { EPS } from './floats';
import { normalize, type RawAnswerInput } from './normalize';
import { parseSpec, questionId, type Spec } from './spec';
import { compareUTF16, has, own, record } from './text';

/** The decision/v1 fields decide() computes. */
export type DecisionCore = Pick<
  GateDecisionView,
  | 'status'
  | 'skip_reason'
  | 'effective_mode'
  | 'action'
  | 'would_action'
  | 'enforced'
  | 'verdict'
  | 'max_risk'
  | 'calibration'
  | 'degraded'
  | 'disagreement'
  | 'deep_audit'
  | 'reasons'
  | 'evaluators'
>;

/** The detectors whose firing is a grounding trigger (GROUNDING_DETS). */
export const GROUNDING_DETECTORS: ReadonlySet<string> = new Set([
  'numbers_not_in_evidence',
  'dates_not_in_evidence',
  'urls_not_in_evidence',
  'quotes_not_in_evidence',
  'citation_out_of_range',
]);

/** Raised for a structurally invalid input (unknown mode, duplicate ref, bad calibration table...). */
export class DecideError extends Error {
  constructor(message: string) {
    super(`decide: ${message}`);
    this.name = 'DecideError';
  }
}

type EvaluatorInput = DecideInput['evaluators'][number];
type CalibrationInputEntry = DecideInput['calibration'][string];
/** A calibration for the current snapshot (an other_snapshot entry calibrates nothing; P4-PLAN D56). */
type CalibrationEntry = CalibrationInputEntry & { table: CalibrationTable; quality: CalibrationQuality; state: CalibrationState };

/** EvaluatorResult.calibration_warning when the calibration was fitted with other state fields (SPEC §2.11, D56). */
export const CALIBRATION_WARNING_STATE_FIELDS = 'calibrated with different state fields';

/** Whether a calibration was fitted with other state fields than the evaluator sends (order and repeats aside). */
function fitWithOtherStateFields(cal: CalibrationEntry, spec: Spec): boolean {
  const fit = cal.fit_state_fields;
  if (fit === undefined || fit.length === 0) return false;
  const a = [...new Set(fit)].sort();
  const b = [...new Set(spec.stateFields)].sort();
  return a.length !== b.length || a.some((f, i) => f !== b[i]);
}

/** The entry that applies to this snapshot, if any: other_snapshot entries carry no table. */
function currentCalibration(entry: CalibrationInputEntry | undefined): CalibrationEntry | undefined {
  return entry === undefined || entry.state === 'other_snapshot' ? undefined : (entry as CalibrationEntry);
}

/** The working state of one evaluator. */
interface EvalState {
  ref: string;
  spec: Spec;
  /** The evaluator's decision/v1 result, filled while deciding. */
  res: EvaluatorResult;
  /** stage active and not internal: a member of A. */
  active: boolean;
  status: EvaluatorResultStatus;
  /** null unless assessed. */
  level: EvaluatorLevel | null;
  risk: number;
  calibrated: boolean;
  escalate: EvaluatorResult['escalate'];
}

const atLeastWarn = (e: EvalState) => e.level === 'warn' || e.level === 'critical';

/**
 * Computes the gate decision. Each evaluator carries its spec document as written (decide-input.v1),
 * read with parseSpec. Throws DecideError for a structurally invalid input and SpecError for a spec
 * that does not parse.
 */
export function decide(input: DecideInput): DecisionCore {
  checkInput(input);
  const excluded = new Map(input.excluded.map((x) => [x.ref, x.reason]));
  const qidByRef = new Map<string, string>();
  for (const [qid, ref] of Object.entries(input.question_map)) qidByRef.set(ref, qid);
  const fired = (d: string): boolean => own(input.detectors, d)?.fired === true;

  // PER EVALUATOR
  const evals = input.evaluators.map((ev) => {
    const spec = parseSpec(ev.spec);
    return assess(ev, spec, input, qidByRef.get(ev.ref) ?? questionId(spec), excluded, fired);
  });
  const active = evals.filter((e) => e.active);
  const byId = (id: string) => active.find((e) => e.spec.id === id);

  // AGGREGATE
  const critical = new Set(active.filter((e) => e.level === 'critical').map((e) => e.spec.id));
  const corroborators = new Set<string>(); // detectors that corroborated a block-set member
  const corroborated = (e: EvalState): boolean => {
    const req = e.spec.outcomes.requiresCorroboration;
    let ok = req.length === 0;
    for (const x of req) {
      if (x.startsWith('det:')) {
        if (fired(x.slice(4))) ok = true;
      } else if (critical.has(x)) {
        ok = true;
      }
    }
    return ok;
  };
  const blockSet: EvalState[] = [];
  let needsSecond = false;
  for (const e of active) {
    if (e.level !== 'critical' || !e.spec.outcomes.blockEligible) continue;
    const c = corroborated(e);
    e.res.corroborated = c;
    if (c) {
      blockSet.push(e);
      for (const x of e.spec.outcomes.requiresCorroboration) {
        if (x.startsWith('det:') && fired(x.slice(4))) corroborators.add(x.slice(4));
      }
    } else {
      needsSecond = true;
    }
  }
  let disagreement = false;
  for (const e of active) {
    if (e.level !== 'critical') continue;
    for (const x of e.spec.outcomes.implies) {
      const o = byId(x);
      if (o !== undefined && o.status === 'assessed' && o.level === 'ok') disagreement = true;
    }
  }
  const detFlag = input.policy.detectors.flag_on.some(fired);
  const detBlock = input.policy.detectors.block_on.some(fired);

  let would: WouldAction = 'pass';
  if (detFlag || disagreement || active.some(atLeastWarn)) would = 'flag';
  if (blockSet.length > 0 || detBlock) would = 'block';

  // NO-EVIDENCE RULE
  let noEvidence = false;
  if (input.policy.on_no_evidence !== 'ignore' && noEvidenceApplies(evals, active, excluded, fired)) {
    would = maxWould(would, input.policy.on_no_evidence);
    noEvidence = true;
  }

  const status = gateStatus(input, active);

  let effectiveMode: PolicyMode = input.policy.configured_mode;
  let streamNotBuffered = false;
  if ((effectiveMode === 'block' || effectiveMode === 'fallback') && input.meta.stream === 'passthrough') {
    effectiveMode = 'flag';
    streamNotBuffered = true;
  }
  let action = actionFor(effectiveMode, would);

  // FAIL
  const skip = input.meta.skip_reason ?? '';
  let degraded: Degraded | null = null;
  let failing = status === 'skipped' || status === 'timeout' || status === 'error';
  if (status === 'partial') failing = active.some((e) => e.spec.outcomes.blockEligible && e.status === 'error');
  let failClosed = false;
  if (failing && skip !== 'sampled_out') {
    degraded = degradedFor(status, skip);
    if (input.policy.fail === 'closed' && (effectiveMode === 'block' || effectiveMode === 'fallback')) {
      action = 'block';
      failClosed = true;
    }
  }

  // CALIBRATION CAP. The reason says why (P4-PLAN D56): uncalibrated_snapshot when every
  // uncalibrated member is calibrated only on another snapshot, otherwise calibration_missing.
  let uncalibratedCap = false;
  let capReason: 'uncalibrated_snapshot' | 'calibration_missing' = 'calibration_missing';
  if ((action === 'block' || action === 'fallback') && !failClosed && input.policy.require_calibration && !detBlock) {
    const uncalibratedRefs = blockSet.filter((e) => !e.calibrated).map((e) => e.ref);
    if (noEvidence) {
      const fc = evals.find((e) => e.spec.id === FACTUAL_CLAIMS_ID);
      if (fc === undefined) uncalibratedRefs.push(FACTUAL_CLAIMS_REF);
      else if (!fc.calibrated) uncalibratedRefs.push(fc.ref);
    }
    if (uncalibratedRefs.length > 0) {
      action = 'flag';
      degraded ??= 'uncalibrated';
      uncalibratedCap = true;
      if (uncalibratedRefs.every((ref) => own(input.calibration, ref)?.state === 'other_snapshot')) capReason = 'uncalibrated_snapshot';
    }
  }

  // max_risk and the calibration summary over assessed A
  let maxRisk: number | null = null;
  let assessed = 0;
  let calibratedCount = 0;
  for (const e of active) {
    if (e.status !== 'assessed') continue;
    assessed++;
    if (e.calibrated) calibratedCount++;
    if (maxRisk === null || e.risk > maxRisk) maxRisk = e.risk;
  }
  const calibration = assessed > 0 && calibratedCount === assessed ? 'all' : calibratedCount > 0 ? 'partial' : 'none';

  // deep_audit
  let groundingTrigger = disagreement;
  for (const e of active) if (e.spec.verdictRole !== null && (atLeastWarn(e) || e.escalate !== null)) groundingTrigger = true;
  for (const d of [...input.policy.detectors.flag_on, ...input.policy.detectors.block_on]) {
    if (GROUNDING_DETECTORS.has(d) && fired(d)) groundingTrigger = true;
  }
  let deepAudit: DeepAuditClass;
  if (status === 'skipped' || status === 'timeout' || status === 'error') deepAudit = skip === 'sampled_out' ? 'sample' : 'sample_error';
  else if (status === 'not_assessed' && !input.meta.evidence_present) deepAudit = 'none';
  else if (groundingTrigger) deepAudit = 'high';
  else deepAudit = 'sample';

  // reasons
  const reasons = levelReasons(active);
  const listed = new Set<string>([...input.policy.detectors.flag_on, ...input.policy.detectors.block_on]);
  const dets = Object.keys(input.detectors)
    .filter((d) => fired(d) && (listed.has(d) || corroborators.has(d)))
    .sort(compareUTF16);
  for (const d of dets) reasons.push(`det:${d}`);
  const fixed: [boolean, string][] = [
    [disagreement, 'disagreement'],
    [needsSecond, 'needs_second_signal'],
    [noEvidence, 'no_evidence'],
    [streamNotBuffered, 'stream_not_buffered'],
    [failClosed, 'fail_closed'],
    [uncalibratedCap, capReason],
  ];
  for (const [on, reason] of fixed) if (on) reasons.push(reason);

  return {
    status,
    skip_reason: input.meta.skip_reason ?? null,
    effective_mode: effectiveMode,
    action,
    would_action: would,
    enforced: effectiveMode !== 'observe',
    verdict: verdict(active),
    max_risk: maxRisk,
    calibration,
    degraded,
    disagreement,
    deep_audit: deepAudit,
    reasons,
    evaluators: record(evals.map((e) => [e.ref, e.res] as const)),
  };
}

/** The per-evaluator part of decide(). */
function assess(
  ev: EvaluatorInput,
  spec: Spec,
  input: DecideInput,
  qid: string,
  excluded: Map<string, string>,
  fired: (d: string) => boolean,
): EvalState {
  const th = ev.thresholds;
  const space: ThresholdSpace = th.space || 'raw';
  const cal: CalibrationEntry | undefined = currentCalibration(own(input.calibration, ev.ref));
  const warn = th.warn_at ?? spec.outcomes.warnAt;
  // An explicit null overrides the spec value; only an absent key falls back to it.
  const crit = has(th, 'critical_at') ? (th.critical_at ?? null) : spec.outcomes.criticalAt;
  const ef = has(th, 'escalate_from') ? (th.escalate_from ?? null) : spec.escalation.escalateFrom;
  const r: EvaluatorResult = {
    ref: ev.ref,
    stage: ev.stage,
    internal: ev.internal,
    status: 'not_assessed',
    reason: null,
    type: spec.question.type,
    answer: null,
    probabilities: null,
    risk_raw: null,
    risk: null,
    threshold_space: space,
    calibrated: false,
    calibration_state: cal?.state ?? 'none',
    calibration_warning: cal !== undefined && fitWithOtherStateFields(cal, spec) ? CALIBRATION_WARNING_STATE_FIELDS : null,
    level: null,
    capped: null,
    top: null,
    vendor_confidence: null,
    confidence_source: null,
    escalate: null,
    corroborated: null,
    block_eligible: spec.outcomes.blockEligible,
    thresholds: { warn_at: warn, critical_at: crit, escalate_from: ef },
  };
  const e: EvalState = {
    ref: ev.ref,
    spec,
    res: r,
    active: ev.stage === 'active' && !ev.internal,
    status: 'not_assessed',
    level: null,
    risk: 0,
    calibrated: false,
    escalate: null,
  };

  const setError = (reason: string) => {
    e.status = 'error';
    r.status = 'error';
    r.reason = reason;
    if (spec.escalation.onError === 'escalate') {
      e.escalate = 'error';
      r.escalate = 'error';
    }
  };

  const exclusion = excluded.get(ev.ref);
  if (exclusion !== undefined) {
    r.reason = exclusion;
    return e;
  }
  if (input.meta.skip_reason !== null && input.meta.skip_reason !== undefined) {
    setError(input.meta.skip_reason);
    return e;
  }
  const k = own(input.meta.qid_request, qid);
  if (k !== undefined) {
    const st = own(input.meta.request_status, String(k));
    if (st !== undefined && st !== 'ok') {
      setError(st);
      return e;
    }
  }
  const questionError = own(input.meta.question_errors, qid);
  if (questionError !== undefined) {
    setError(questionError);
    return e;
  }
  const answer = (own(input.answers, qid) ?? null) as RawAnswerInput | null;
  r.answer = answer === null ? null : rawAnswerView(answer);
  const n = normalize(answer, spec);
  if (n.error !== null) {
    setError(n.error);
    return e;
  }

  e.status = 'assessed';
  r.status = 'assessed';
  r.probabilities = n.probabilities;
  r.risk_raw = n.riskRaw;
  r.vendor_confidence = n.vendorConfidence;
  r.top = n.top;
  r.confidence_source = n.confidenceSource;

  const usable = cal !== undefined && (cal.quality === 'measured' || (cal.quality === 'mock' && input.meta.backend === 'mock'));
  const risk = space === 'calibrated' && cal !== undefined ? applyCalibration(cal.table, n.riskRaw) : n.riskRaw;
  e.risk = risk;
  e.calibrated = space === 'calibrated' && usable;
  r.risk = risk;
  r.calibrated = e.calibrated;

  // min_confidence reads the aggregated signal, never the vendor confidence.
  const minConf = spec.outcomes.minConfidence;
  const confOK = minConf === null || Math.max(risk, 1 - risk) >= minConf - EPS;
  let level: EvaluatorLevel = 'ok';
  if (crit !== null && risk >= crit - EPS && confOK) level = 'critical';
  else if (risk >= warn - EPS) level = 'warn';
  if (level === 'critical' && spec.verdictRole !== null && input.meta.knowledge_only) {
    level = 'warn';
    r.capped = 'knowledge_only';
  } else if (level === 'critical' && spec.verdictRole === 'unsupported' && input.meta.evidence_truncated) {
    level = 'warn';
    r.capped = 'evidence_truncated';
  }
  e.level = level;
  r.level = level;

  if (ef !== null && ef - EPS <= risk && risk < warn - EPS) e.escalate = 'uncertain';
  else if (spec.escalation.numericOrDate && (fired('numbers_not_in_evidence') || fired('dates_not_in_evidence'))) {
    e.escalate = 'numeric_or_date_unverified';
  }
  r.escalate = e.escalate;
  return e;
}

/**
 * The answer as decision/v1 records it: type, plus the members that are set (Go omitempty). A null
 * probability is recorded as 0, the value Go's decoder stores for it (and the one normalize uses).
 */
function rawAnswerView(a: RawAnswerInput): NonNullable<EvaluatorResult['answer']> {
  const out: Record<string, unknown> = { type: a.type ?? '' };
  for (const key of ['noul', 'choice', 'score', 'confidence', 'probabilities', 'legend'] as const) {
    const v = a[key];
    const empty = v === null || v === undefined || v === '' || (typeof v === 'object' && Object.keys(v).length === 0);
    if (empty) continue;
    out[key] = key === 'probabilities' ? record(Object.entries(v as Record<string, number | null>).map(([k, p]) => [k, p ?? 0] as const)) : v;
  }
  return out as unknown as NonNullable<EvaluatorResult['answer']>;
}

/**
 * The no-evidence rule's condition (on_no_evidence != ignore is checked by the caller): every
 * required active evaluator was excluded for lack of evidence, and either factual_claims found
 * checkable facts or the numbers or dates detector fired.
 */
function noEvidenceApplies(evals: EvalState[], active: EvalState[], excluded: Map<string, string>, fired: (d: string) => boolean): boolean {
  let required = 0;
  for (const e of active) {
    if (e.spec.evidence.requires !== 'required') continue;
    required++;
    const reason = excluded.get(e.ref);
    if (reason !== 'no_evidence' && reason !== 'evidence_too_short' && reason !== 'evidence_packed_away') return false;
  }
  if (required === 0) return false;
  const fc = evals.find((e) => e.spec.id === FACTUAL_CLAIMS_ID);
  if (fc !== undefined && fc.status === 'assessed' && atLeastWarn(fc)) return true;
  return fired('numbers_not_in_evidence') || fired('dates_not_in_evidence');
}

const WOULD_RANK: Record<WouldAction, number> = { pass: 0, flag: 1, block: 2 };

function maxWould(a: WouldAction, b: WouldAction): WouldAction {
  return WOULD_RANK[b] > WOULD_RANK[a] ? b : a;
}

/** The decision status (SPEC §2.9 status). */
function gateStatus(input: DecideInput, active: EvalState[]): GateStatus {
  if (input.meta.skip_reason !== null && input.meta.skip_reason !== undefined) return 'skipped';
  if (active.length > 0 && active.every((e) => e.status === 'not_assessed')) return 'not_assessed';
  const requestStatus = Object.values(input.meta.request_status);
  if (requestStatus.length > 0 && requestStatus.every((st) => st !== 'ok')) {
    return requestStatus.includes('timeout') ? 'timeout' : 'error';
  }
  if (active.some((e) => e.status === 'error')) return 'partial';
  return 'ok';
}

/** Maps the would-action to the effective mode's action. */
function actionFor(mode: PolicyMode, would: WouldAction): GateAction {
  switch (mode) {
    case 'observe':
      return 'observe';
    case 'flag':
      return would === 'pass' ? 'pass' : 'flag';
    case 'fallback':
      return would === 'block' ? 'fallback' : would;
  }
  return would;
}

/** Maps a failing status (or, for skipped, its skip reason) to decision.degraded. */
function degradedFor(status: GateStatus, skip: string): Degraded {
  switch (status === 'skipped' ? skip : status) {
    case 'budget':
      return 'budget';
    case 'breaker_open':
      return 'breaker';
    case 'rate_limited':
      return 'rate_limited';
    case 'decision_model_off':
    case 'credentials_unavailable':
      return 'off';
    case 'overload':
      return 'overload';
    case 'timeout':
      return 'timeout';
  }
  return 'error';
}

/** The gate verdict over the active evaluators with a verdict role. */
function verdict(active: EvalState[]): GateVerdict | null {
  const rb = active.filter((e) => e.spec.verdictRole !== null);
  if (rb.length === 0) return null;
  if (rb.some((e) => e.spec.verdictRole === 'contradicted' && atLeastWarn(e))) return 'contradicted';
  if (rb.some((e) => e.spec.verdictRole === 'unsupported' && atLeastWarn(e))) return 'unsupported';
  if (rb.some((e) => e.status === 'error')) return 'unknown';
  if (rb.every((e) => e.status === 'not_assessed')) return 'not_assessed';
  return 'supported';
}

/** "<id>:<level>" for active evaluators at warn or critical: critical first, then risk descending, then id. */
function levelReasons(active: EvalState[]): string[] {
  return active
    .filter(atLeastWarn)
    .sort((a, b) => {
      if ((a.level === 'critical') !== (b.level === 'critical')) return a.level === 'critical' ? -1 : 1;
      if (a.risk !== b.risk) return a.risk > b.risk ? -1 : 1;
      return compareUTF16(a.spec.id, b.spec.id);
    })
    .map((e) => `${e.spec.id}:${e.level}`);
}

/** Rejects inputs decide() cannot interpret. */
function checkInput(input: DecideInput): void {
  if (!['observe', 'flag', 'block', 'fallback'].includes(input.policy.configured_mode)) {
    throw new DecideError(`unknown configured_mode ${JSON.stringify(input.policy.configured_mode)}`);
  }
  if (!['open', 'closed'].includes(input.policy.fail)) throw new DecideError(`unknown fail mode ${JSON.stringify(input.policy.fail)}`);
  if (!['ignore', 'flag', 'block'].includes(input.policy.on_no_evidence)) {
    throw new DecideError(`unknown on_no_evidence ${JSON.stringify(input.policy.on_no_evidence)}`);
  }
  const seen = new Set<string>();
  for (const e of input.evaluators) {
    if (e.spec === null || e.spec === undefined) throw new DecideError(`evaluator ${e.ref} has no spec`);
    if (seen.has(e.ref)) throw new DecideError(`duplicate evaluator ${e.ref}`);
    seen.add(e.ref);
    if (e.stage !== 'active' && e.stage !== 'shadow') throw new DecideError(`evaluator ${e.ref}: unknown stage ${JSON.stringify(e.stage)}`);
    const space = e.thresholds.space as string;
    if (space && space !== 'raw' && space !== 'calibrated') throw new DecideError(`evaluator ${e.ref}: unknown threshold space ${JSON.stringify(space)}`);
  }
  for (const [ref, c] of Object.entries(input.calibration)) {
    if (c.state === 'other_snapshot') {
      if (c.table !== undefined && c.table.length > 0) throw new DecideError(`calibration for ${ref}: an other_snapshot entry has no table`);
      continue;
    }
    try {
      checkCalibrationTable(c.table as CalibrationTable);
    } catch (err) {
      throw new DecideError(`calibration for ${ref}: ${(err as Error).message}`);
    }
  }
}
