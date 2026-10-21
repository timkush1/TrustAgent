/**
 * compile(route, evaluators, inputs, budget) (SPEC §2.9): turns a route's evaluators and one
 * exchange into decision-model requests. Port of backend-go/internal/decision/compile.go; pinned by
 * contracts/vectors/compile.
 */
import type { DecideInput, EvaluatorStage, ExclusionReason } from '@/lib/contracts/types.gen';
import { sha256Hex } from './jcs';
import { hasStateField, questionBody, questionId, type Spec } from './spec';
import { asciiLower, codePointLength, compareUTF16, has, headCodePoints, own, tailCodePoints } from './text';
import { checkCPT, DEFAULT_CPT, est, OVERHEAD } from './tokens';

/** Hard ceiling of one request: overhead + est(state) + max est(q). */
export const MAX_REQUEST_TOKENS = 28800;
/** Replaces an empty text field in the state. */
export const NONE_PROVIDED = '(none provided)';
/** The internal evaluator compile() adds when on_no_evidence is set (SPEC §2.9 compile step 3). */
export const FACTUAL_CLAIMS_ID = 'factual_claims';
export const FACTUAL_CLAIMS_REF = 'factual_claims@1';

const USER_REQUEST_LONG_TOKENS = 1000;
const USER_REQUEST_HEAD_TOKENS = 600;
const USER_REQUEST_TAIL_TOKENS = 400;
/** Joins the head and tail of a long user_request: space, U+2026, space. */
const USER_REQUEST_JOIN = ' \u2026 ';
const SYSTEM_INSTRUCTIONS_TOKENS = 800;
/** The room an item needs to be truncated instead of skipped. */
const MIN_TRUNCATION_TOKENS = 200;

export type OnNoEvidence = 'ignore' | 'flag' | 'block';

/** A route's reference to an evaluator (policy RouteEvaluatorRef, as compile() reads it). */
export interface CompileRouteEvaluator {
  ref: string;
  /** Default active. */
  stage?: EvaluatorStage;
  /** Default true. */
  enabled?: boolean;
  params?: Readonly<Record<string, string>>;
}

/** The part of a route's settings that compile() reads. */
export interface CompileRoute {
  /** Route order. */
  evaluators: readonly CompileRouteEvaluator[];
  on_no_evidence: OnNoEvidence;
  /** The route's evidence.sources priority order (source_rank). */
  evidence_sources: readonly string[];
  /** overrides.client_rules: whether request trustagent.rules may set params. */
  client_rules: boolean;
  /** The decision model requested. */
  model: string;
}

/** One extracted evidence item as the decision core sees it. */
export interface DecisionEvidenceItem {
  id: string;
  source: string;
  text: string;
  /** Untrusted items (untrusted_tools) never enter the state and are never a detector reference. */
  untrusted?: boolean;
}

/** The texts of one gated exchange. */
export interface CompileInputs {
  user_request: string;
  assistant_response: string;
  system_instructions: string;
  /** Priority-ordered and not yet packed. */
  evidence: readonly DecisionEvidenceItem[];
  /** The request's trustagent.rules, honoured only with client_rules. */
  rules?: Readonly<Record<string, string>> | null;
}

/** Sizes the compiled requests. */
export interface Budget {
  tokens: number;
  cpt: number;
  max_questions: number;
  overhead: number;
  max_response_tokens: number;
  evidence_max_tokens: number;
}

/** The SPEC §2.7 defaults (TRUTHTABLE_JEV_TOKEN_BUDGET, _CHARS_PER_TOKEN, _MAX_QUESTIONS_PER_CALL, route limits). */
export function defaultBudget(): Budget {
  return {
    tokens: 24000,
    cpt: DEFAULT_CPT,
    max_questions: 16,
    overhead: OVERHEAD,
    max_response_tokens: 6000,
    evidence_max_tokens: 8000,
  };
}

/** One decision-model request body (transport fields such as session_id are the caller's). */
export interface CompiledRequest {
  model: string;
  state: Record<string, unknown>;
  questions: Record<string, unknown>;
}

/** An evaluator compile() considered: the enabled route evaluators in order, then the implicit factual_claims@1. */
export interface CompiledEvaluator {
  ref: string;
  stage: EvaluatorStage;
  internal: boolean;
}

export interface Exclusion {
  ref: string;
  reason: ExclusionReason;
}

/** The output of compile(). */
export interface Compiled {
  requests: CompiledRequest[];
  evaluators: CompiledEvaluator[];
  /** Asked question id → evaluator ref. */
  question_map: Record<string, string>;
  /** Asked question id → index of its request. */
  qid_request: Record<string, number>;
  excluded: Exclusion[];
  packed_ids: string[];
  truncated_ids: string[];
  /** True when an evidence item was truncated or skipped by packing. */
  truncated: boolean;
  cpt: number;
  est_tokens: number[];
  request_sha256: string[];
  question_set_sha256: string;
}

/** Raised for a route or evaluator set that cannot be compiled. */
export class CompileError extends Error {
  constructor(message: string) {
    super(`compile: ${message}`);
    this.name = 'CompileError';
  }
}

interface Member {
  ref: string;
  spec: Spec;
  stage: EvaluatorStage;
  internal: boolean;
  params: Readonly<Record<string, string>> | undefined;
  /** Exclusion reason, null while included. */
  excluded: ExclusionReason | null;
}

/**
 * Compiles a route's evaluators and one exchange into decision-model requests. specs resolves every
 * evaluator ref of the route, plus factual_claims@1 when the route sets on_no_evidence. Throws
 * CompileError when the route or its evaluators are unusable.
 */
export function compile(
  route: CompileRoute,
  specs: ReadonlyMap<string, Spec>,
  inputs: CompileInputs,
  budget: Budget = defaultBudget(),
): Compiled {
  checkBudget(budget);
  if (!route.model) throw new CompileError('the decision model is not set');
  if (!['ignore', 'flag', 'block'].includes(route.on_no_evidence)) {
    throw new CompileError(`unknown on_no_evidence ${JSON.stringify(route.on_no_evidence)}`);
  }

  // Step 0: enabled evaluators; shadow ones are asked too.
  const members: Member[] = [];
  const seen = new Set<string>();
  for (const re of route.evaluators) {
    if (re.enabled === false) continue;
    const spec = specs.get(re.ref);
    if (spec === undefined) throw new CompileError(`no spec for evaluator ${re.ref}`);
    if (seen.has(re.ref)) throw new CompileError(`evaluator ${re.ref} is attached twice`);
    seen.add(re.ref);
    const stage = (re.stage as string | undefined) || 'active';
    if (stage !== 'active' && stage !== 'shadow') throw new CompileError(`evaluator ${re.ref}: unknown stage ${JSON.stringify(stage)}`);
    members.push({ ref: re.ref, spec, stage, internal: false, params: re.params, excluded: null });
  }

  const out: Compiled = {
    requests: [],
    evaluators: [],
    question_map: {},
    qid_request: {},
    excluded: [],
    packed_ids: [],
    truncated_ids: [],
    truncated: false,
    cpt: budget.cpt,
    est_tokens: [],
    request_sha256: [],
    question_set_sha256: '',
  };

  // Step 1: a response too long to check excludes everything.
  if (est(inputs.assistant_response, budget.cpt) > budget.max_response_tokens) {
    for (const m of members) m.excluded = 'too_long';
    return finish(out, route, members, null, null, budget);
  }

  // Step 2: the pre-pack evidence gate. Untrusted items never count as evidence.
  const items = inputs.evidence.filter((it) => !it.untrusted);
  let total = 0;
  for (const it of items) total += codePointLength(it.text);
  let noEvidence = false;
  for (const m of members) {
    if (m.spec.evidence.requires !== 'required') continue;
    if (items.length === 0) {
      m.excluded = 'no_evidence';
      noEvidence = true;
    } else if (total < m.spec.evidence.minChars) {
      m.excluded = 'evidence_too_short';
    }
  }

  // Step 3: the implicit factual_claims evaluator.
  if (route.on_no_evidence !== 'ignore' && noEvidence && !members.some((m) => m.spec.id === FACTUAL_CLAIMS_ID)) {
    const spec = specs.get(FACTUAL_CLAIMS_REF);
    if (spec === undefined) throw new CompileError(`on_no_evidence needs the ${FACTUAL_CLAIMS_REF} spec`);
    members.push({ ref: FACTUAL_CLAIMS_REF, spec, stage: 'active', internal: true, params: undefined, excluded: null });
  }

  // Steps 4-5: questions and the state without evidence.
  const { qEst } = buildQuestions(members, budget.cpt);
  const base = buildState(members, route, inputs, budget.cpt);

  // Step 6: pack evidence when an included evaluator reads it.
  let packed: Record<string, string>[] | null = null;
  if (needsEvidence(members)) {
    const baseEst = est(base, budget.cpt);
    let qSum = 0;
    for (const e of qEst.values()) qSum += e;
    const limit = Math.min(budget.evidence_max_tokens, budget.tokens - budget.overhead - qSum - baseEst);
    const p = pack(items, inputs.assistant_response, route.evidence_sources, limit, budget.cpt);
    packed = p.state;
    out.packed_ids = p.packedIds;
    out.truncated_ids = p.truncatedIds;
    out.truncated = p.truncated;

    // Step 7: the post-pack evidence gate.
    for (const m of members) {
      if (m.excluded === null && m.spec.evidence.requires === 'required' && p.chars < m.spec.evidence.minChars) {
        m.excluded = 'evidence_packed_away';
      }
    }
  }
  return finish(out, route, members, packed, inputs, budget);
}

/** Rebuilds the state for the evaluators still included, splits the questions (step 8) and hashes (step 9). */
function finish(
  out: Compiled,
  route: CompileRoute,
  members: Member[],
  packed: Record<string, string>[] | null,
  inputs: CompileInputs | null,
  budget: Budget,
): Compiled {
  for (const m of members) {
    out.evaluators.push({ ref: m.ref, stage: m.stage, internal: m.internal });
    if (m.excluded !== null) out.excluded.push({ ref: m.ref, reason: m.excluded });
  }
  const { questions, qEst } = buildQuestions(members, budget.cpt);
  const qids = [...questions.keys()].sort(compareUTF16);
  out.question_set_sha256 = sha256Hex(qids.map((qid) => [qid, questions.get(qid)]));
  if (qids.length === 0) return out;
  for (const m of members) if (m.excluded === null) out.question_map[questionId(m.spec)] = m.ref;

  const state = buildState(members, route, inputs, budget.cpt);
  if (needsEvidence(members)) state.evidence = packed !== null && packed.length > 0 ? packed : NONE_PROVIDED;
  checkTextTree(state, 'state');
  const stateEst = est(state, budget.cpt);

  split(qids, qEst, stateEst, budget).forEach((bin, k) => {
    const first = qEst.get(bin[0]) ?? 0;
    if (budget.overhead + stateEst + first > MAX_REQUEST_TOKENS) {
      throw new CompileError(`question does not fit in a request with the state: ${bin[0]} needs ${budget.overhead + stateEst + first} tokens`);
    }
    const req: CompiledRequest = { model: route.model, state, questions: {} };
    for (const qid of bin) {
      req.questions[qid] = questions.get(qid);
      out.qid_request[qid] = k;
    }
    out.requests.push(req);
    out.request_sha256.push(sha256Hex(req));
    out.est_tokens.push(budget.overhead + est(req, budget.cpt));
  });
  return out;
}

/**
 * First-fit-decreasing by est(q), ties by question id: each request has at most max_questions
 * questions and fits the token budget with the shared state. A question that fits no request opens
 * a new one on its own. The largest question of every request comes first.
 */
function split(qids: string[], qEst: Map<string, number>, stateEst: number, b: Budget): string[][] {
  const size = (qid: string) => qEst.get(qid) ?? 0;
  const order = [...qids].sort((x, y) => size(y) - size(x));
  const bins: string[][] = [];
  const sums: number[] = [];
  for (const qid of order) {
    const i = bins.findIndex((bin, j) => bin.length < b.max_questions && b.overhead + stateEst + sums[j] + size(qid) <= b.tokens);
    if (i >= 0) {
      bins[i].push(qid);
      sums[i] += size(qid);
    } else {
      bins.push([qid]);
      sums.push(size(qid));
    }
  }
  return bins;
}

/** The question bodies of the included members keyed by question id, and est() of each body. */
function buildQuestions(members: Member[], cpt: number): { questions: Map<string, unknown>; qEst: Map<string, number> } {
  const questions = new Map<string, unknown>();
  const qEst = new Map<string, number>();
  for (const m of members) {
    if (m.excluded !== null) continue;
    const qid = questionId(m.spec);
    if (questions.has(qid)) throw new CompileError(`question id ${qid} is asked twice`);
    const body = questionBody(m.spec);
    checkTextTree(body, `question ${qid}`);
    questions.set(qid, body);
    qEst.set(qid, est(body, cpt));
  }
  return { questions, qEst };
}

/** Whether an included member lists the evidence state field. */
function needsEvidence(members: Member[]): boolean {
  return members.some((m) => m.excluded === null && hasStateField(m.spec, 'evidence'));
}

const orNone = (s: string): string => (s === '' ? NONE_PROVIDED : s);

/** The state of the included members without evidence (step 4). A null inputs yields an empty state. */
function buildState(members: Member[], route: CompileRoute, inputs: CompileInputs | null, cpt: number): Record<string, unknown> {
  const state: Record<string, unknown> = {};
  if (inputs === null) return state;
  const fields = new Set<string>();
  for (const m of members) if (m.excluded === null) for (const f of m.spec.stateFields) fields.add(f);
  if (fields.has('user_request')) state.user_request = orNone(shortenUserRequest(inputs.user_request, cpt));
  if (fields.has('assistant_response')) state.assistant_response = orNone(inputs.assistant_response);
  if (fields.has('system_instructions')) {
    state.system_instructions = orNone(headCodePoints(inputs.system_instructions, Math.floor(SYSTEM_INSTRUCTIONS_TOKENS * cpt)));
  }
  if (fields.has('rules')) {
    const rules = new Map<string, string>();
    for (const m of members) {
      if (m.excluded !== null || !hasStateField(m.spec, 'rules')) continue;
      for (const [name, p] of m.spec.params) {
        let v = p.default;
        if (has(m.params, name)) v = own(m.params, name) ?? v;
        if (route.client_rules && has(inputs.rules, name)) v = own(inputs.rules, name) ?? v;
        rules.set(name, orNone(v));
      }
    }
    state.rules = rules.size > 0 ? Object.fromEntries(rules) : NONE_PROVIDED;
  }
  return state;
}

/** Keeps the head and tail of a user_request longer than 1,000 tokens. */
function shortenUserRequest(s: string, cpt: number): string {
  if (est(s, cpt) <= USER_REQUEST_LONG_TOKENS) return s;
  const head = Math.floor(USER_REQUEST_HEAD_TOKENS * cpt);
  const tail = Math.floor(USER_REQUEST_TAIL_TOKENS * cpt);
  if (codePointLength(s) <= head + tail) return s;
  return headCodePoints(s, head) + USER_REQUEST_JOIN + tailCodePoints(s, tail);
}

interface PackResult {
  /** [{id, source, text}] in (source_rank, index) order. */
  state: Record<string, string>[];
  packedIds: string[];
  truncatedIds: string[];
  truncated: boolean;
  /** Code points of the packed text. */
  chars: number;
}

/** Fits the evidence items into limit tokens (SPEC §2.9 compile step 6). */
function pack(items: readonly DecisionEvidenceItem[], response: string, sources: readonly string[], limit: number, cpt: number): PackResult {
  interface Ranked {
    index: number;
    rank: number;
    overlap: number;
    text: string;
    taken: boolean;
    cut: boolean;
  }
  const respTokens = overlapTokens(response);
  const ranked: Ranked[] = items.map((it, index) => {
    const found = sources.indexOf(it.source);
    let common = 0;
    for (const t of overlapTokens(it.text)) if (respTokens.has(t)) common++;
    return {
      index,
      rank: found < 0 ? sources.length : found,
      overlap: common / Math.sqrt(Math.max(1, est(it.text, cpt))),
      text: it.text,
      taken: false,
      cut: false,
    };
  });
  const order = [...ranked].sort((a, b) => {
    if (a.rank !== b.rank) return a.rank - b.rank;
    if (a.overlap > b.overlap) return -1;
    if (a.overlap < b.overlap) return 1;
    return a.index - b.index;
  });

  const itemEst = (i: number, text: string) => est({ id: items[i].id, source: items[i].source, text }, cpt);
  const res: PackResult = { state: [], packedIds: [], truncatedIds: [], truncated: false, chars: 0 };
  let used = 0;
  for (const r of order) {
    const full = itemEst(r.index, r.text);
    if (used + full <= limit) {
      r.taken = true;
      used += full;
      continue;
    }
    if (limit - used >= MIN_TRUNCATION_TOKENS) {
      const n = Math.floor((limit - used - itemEst(r.index, '')) * cpt);
      if (n > 0) {
        r.text = headCodePoints(r.text, n);
        r.taken = true;
        r.cut = r.text !== items[r.index].text;
        used += itemEst(r.index, r.text);
        continue;
      }
    }
    res.truncated = true; // skipped
  }

  order.sort((a, b) => (a.rank !== b.rank ? a.rank - b.rank : a.index - b.index));
  for (const r of order) {
    if (!r.taken) continue;
    const it = items[r.index];
    res.state.push({ id: it.id, source: it.source, text: r.text });
    res.packedIds.push(it.id);
    res.chars += codePointLength(r.text);
    if (r.cut) {
      res.truncatedIds.push(it.id);
      res.truncated = true;
    }
  }
  return res;
}

/** T(text): the set of [a-z0-9]{4,} runs of the ASCII-lowercased text. */
function overlapTokens(text: string): Set<string> {
  const set = new Set<string>();
  // Only A-Z is lowercased: toLowerCase would turn some non-ASCII letters (U+212A KELVIN SIGN) into ASCII.
  for (const m of asciiLower(text).matchAll(/[a-z0-9]{4,}/g)) set.add(m[0]);
  return set;
}

/**
 * Rejects JSON numbers and booleans, and object keys that are not printable ASCII, in a state or
 * question tree (SPEC §2.1 Jev payloads).
 */
function checkTextTree(v: unknown, where: string): void {
  if (v === null || typeof v === 'string') return;
  if (Array.isArray(v)) {
    for (const e of v) checkTextTree(e, where);
    return;
  }
  if (typeof v === 'object') {
    for (const [k, e] of Object.entries(v as Record<string, unknown>)) {
      if (!/^[\x20-\x7e]+$/.test(k)) throw new CompileError(`${where}: key ${JSON.stringify(k)} is not printable ASCII`);
      checkTextTree(e, where);
    }
    return;
  }
  throw new CompileError(`${where} contains ${String(v)}; only strings, arrays and objects are allowed`);
}

function checkBudget(b: Budget): void {
  checkCPT(b.cpt);
  const ints = [b.tokens, b.max_questions, b.overhead, b.max_response_tokens, b.evidence_max_tokens];
  if (!ints.every(Number.isInteger) || b.tokens <= 0 || b.max_questions < 1 || b.overhead < 0 || b.max_response_tokens <= 0 || b.evidence_max_tokens < 0) {
    throw new CompileError(`invalid budget ${JSON.stringify(b)}`);
  }
}

/** Route threshold overrides of one evaluator, as decide() reads them. */
export type ThresholdOverrides = DecideInput['evaluators'][number]['thresholds'];

/**
 * The decide() evaluators for a compile result: every evaluator compile considered, in order (the
 * implicit factual_claims last), with its spec as written and the route's threshold overrides (keyed
 * by ref; a missing entry means raw space and the spec thresholds).
 */
export function decideEvaluators(
  compiled: Compiled,
  specs: ReadonlyMap<string, Spec>,
  thresholds: Readonly<Record<string, ThresholdOverrides>> = {},
): DecideInput['evaluators'] {
  return compiled.evaluators.map((e) => {
    const spec = specs.get(e.ref);
    if (spec === undefined) throw new CompileError(`decide evaluators: no spec for ${e.ref}`);
    const th = own(thresholds, e.ref);
    return { ref: e.ref, spec: spec.raw, stage: e.stage, internal: e.internal, thresholds: { ...th, space: th?.space || 'raw' } };
  });
}
