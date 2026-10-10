/**
 * Builds the committed demo recording (trustagent.recording/v1, SPEC §2.13) from the demo scenario
 * pack, the way `make demo` + `export-replay` would record it: trafficgen sends every recordable
 * scenario of one set (default `demo`) once per round, `rounds` rounds `intervalMs` apart (so the
 * default tape spans two hours and its time series have more than one bucket), the mock fast check
 * answers with the scenario's scripted probabilities, and the mock judge returns the scenario's
 * scripted claims.
 *
 * Recordable (selectScenarios): non-streamed openai_chat scenarios with scripted answers whose text
 * no published recording may carry (check_recording.py SECRET_PATTERNS). Streams, the Anthropic
 * and completions formats and generated answers stay out, and so do the scenarios in
 * AWAITING_RUNNER, which the browser gate pipeline cannot reproduce faithfully yet.
 *
 * Nothing here is typed by hand. Each decision runs through the browser gate pipeline (runGate in
 * src/lib/gate, D135: evidence extraction, detectors, compile, decide over the TypeScript decision
 * core the golden vectors pin to the Go gateway) with the scenario's scripted answers and the demo
 * stack's mock calibration, and must reproduce the scenario's `expect` block, or the build fails.
 * What the gateway delivers follows backend-go internal/proxy: a block sends the route's block
 * message; a fallback re-gates the scenario's fallback answer as attempt 1 and delivers it when that
 * passes (fallback.go); observe mode answers first and records the decision after (handler.go
 * respondThenGate). The deep audit follows the engine: no evidence means NOT_ASSESSED with no judge
 * call; a judge that finds no claims means NO_CLAIMS. The manifest mirrors
 * backend-go/internal/recording (Build, events, the pending and decided summaries of the stream),
 * so P6 can swap this file set for `export-replay` output without touching the replay.
 *
 * Every recording this module writes is kind `mock` with the fixed label "Simulated (mock checks +
 * mock judge): not model output", answer_source `scripted`, and costs with cost_source
 * `simulated`. It never produces a `recorded` manifest.
 *
 * Pure: no clock, no randomness, no I/O. The caller passes the parsed contract files and the start
 * time; ids and jitter are derived from sha256 of (scenario, round).
 */
import outcomesDoc from '@contracts/ui/outcomes.v1.json';
import type { EvaluatorSpec } from '@/lib/contracts/types.gen';
import { defaultBudget } from '@/lib/decision/compile';
import { formatRisk } from '@/lib/decision/floats';
import { sha256Hex } from '@/lib/decision/jcs';
import { specHashes, type Spec } from '@/lib/decision/spec';
import { mergeSettings, resolveRoute as resolveGateRoute, RouteError, specMap } from '@/lib/gate/route';
import { NOOP_LEDGER, runGate } from '@/lib/gate/runner';
import type { BrowserGateResult, CalibrationMap, PolicyInput, ResolvedRoute } from '@/lib/gate/types';
import type { JevCall } from '@/lib/openrouter/types';
import { DEFAULT_CPT, est } from '@/lib/decision/tokens';
import type {
  AuditDetail,
  AuditScores,
  AuditVerdict,
  ChatMessage,
  ClaimVerdict,
  ClaimView,
  CostRecord,
  EvidenceItem,
  EvidenceSpan,
  EvidenceSummary,
  GateAction,
  GateDecisionView,
  PolicyMode,
  Recording,
  TraceDetail,
  TraceSummary,
} from '@/types/api';

// ------------------------------------------------------------------------------------ constants

export const RECORDING_SCHEMA = 'trustagent.recording/v1';
/** contracts/ui/copy.v1.json recording.mock: the only label a mock recording may carry. */
export const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';
/** The mock fast check's snapshot (backend-go internal/jev DefaultMockModel). */
export const MOCK_SNAPSHOT = 'mock/jev-heuristic-v1';
/** The list price the mock backend prices its simulated cost at (jev.DefaultPricePerMTok). */
export const MOCK_PRICE_PER_MTOK = 0.042;
/** The Python mock judge names itself provider "mock", model "mock". */
export const MOCK_JUDGE = { provider: 'mock', model: 'mock' } as const;
/** backend-go internal/llmapi DefaultBlockMessage: a route's block message when it sets none. */
export const DEFAULT_BLOCK_MESSAGE = "I can't provide a verified answer to that right now.";
/** The engine's reasoning trace of an audit with nothing to check against (audit_graph.py). */
export const NO_EVIDENCE_TRACE = 'Not assessed: the request carried no evidence documents to check the answer against.';
/** The engine's reasoning trace of an audit whose judge found no claims (scorer.py NO_CLAIMS_TRACE). */
export const NO_CLAIMS_TRACE = 'No verifiable claims were found in the response.';

/**
 * Scenarios the browser gate pipeline (runGate) cannot reproduce as the Go gateway records them
 * yet, with why. They join the tape once runGate has gstudio Runner.Evaluate parity (WEB-LIVE
 * LV-3); demoRecording.test.ts fails as soon as that parity lands, so this list cannot go stale.
 */
export const AWAITING_RUNNER: Readonly<Record<string, string>> = {
  'payments-fallback': 'runGate ignores the route evaluators’ thresholds (space: calibrated), so the fallback is capped to a flag',
  'commitment-unapproved-flag': 'runGate does not pass the route’s resolved rules to the detectors, so the number and date checks do not run',
  'off-topic-flag': 'runGate does not pass the route’s resolved rules to the detectors, so the number and date checks show as not run',
};

/**
 * scripts/check_recording.py SECRET_PATTERNS (SPEC §7.5 redaction patterns, plus private keys,
 * JWTs and e-mail addresses): a scenario whose text matches one stays off the tape, since a
 * published recording may not carry it.
 */
const SECRET_PATTERNS: readonly (readonly [string, RegExp])[] = [
  ['an sk- API key', /sk-[A-Za-z0-9_-]{8,}/],
  ['a bearer token', /Bearer\s+[A-Za-z0-9._~+/=-]{8,}/],
  ['an x-api-key header', /x-api-key:\s*\S+/i],
  ['a private key', /-----BEGIN [A-Z ]*PRIVATE KEY-----/],
  ['a JWT', /eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/],
];
const EMAIL = /[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})/g;
const RESERVED_DOMAINS = ['example.com', 'example.net', 'example.org'];
const RESERVED_TLDS = ['.example', '.test', '.invalid', '.localhost'];

// ------------------------------------------------------------------------------------ inputs

export interface ScenarioClaim {
  claim: string;
  quote: string;
  verdict: ClaimVerdict;
  confidence: number;
  evidence: string[];
  reasoning: string;
}

/** One scripted answer of the mock fast check (scenarios.v1 mock_answers). */
export interface MockAnswer {
  type: 'noul' | 'choice' | 'score';
  noul?: number;
  choice?: string;
  score?: number;
  confidence?: number;
  probabilities?: Record<string, number>;
}

/** A scenario's fallback answer and what its re-gate (attempt 1) must decide. */
export interface ScenarioFallback {
  response: string;
  mock_answers: Record<string, MockAnswer>;
  expect: { would_action: string | null; verdict: string | null; max_risk: number | null; reasons: string[] };
}

/** One scenario of contracts/demo/scenarios.v1.json (the fields a recording needs). */
export interface Scenario {
  id: string;
  title: string;
  sets: string[];
  route: string;
  api_format: string;
  stream: boolean;
  answer_source: 'scripted' | 'generated';
  /** The client request body (openai_chat), as trafficgen sends it. */
  request: { model: string; messages: unknown[] } & Record<string, unknown>;
  response: string;
  mock_answers: Record<string, MockAnswer>;
  fallback?: ScenarioFallback;
  judge: { claims: ScenarioClaim[] } | null;
  expect: {
    mode: PolicyMode;
    checked_against: string;
    action: string;
    would_action: string | null;
    /** X-TrustAgent-Status: `deferred` on an observe-mode response, whose gate runs after it. */
    status: string;
    verdict: string | null;
    max_risk: number | null;
    reasons: string[];
    deep_audit: string;
    audit: string;
    audit_verdict: AuditVerdict;
    /** The outcome id the UI shows (contracts/ui/outcomes.v1.json). */
    outcome: string;
    /** The text the client got, when the gateway replaced the answer. */
    delivered_text?: string;
  };
}

/** One entry of a trustagent.calibration/v1 file (the fields the lookup reads). */
export interface CalibrationEntryInput {
  target: { kind: string; evaluator_id?: string; evaluator_version?: number; calibration_key?: string; params_sha256?: string | null };
  fit_context?: { state_fields?: string[] };
  quality: string;
  table: number[] | null;
}

/** A trustagent.calibration/v1 file (config/calibration/*.json, the demo stack's TRUTHTABLE_CALIBRATION_DIR). */
export interface CalibrationFileInput {
  decision_model: { backend: string; snapshot: string };
  entries: CalibrationEntryInput[];
}

export type { PolicyInput } from '@/lib/gate/types';

export interface BuildInputs {
  /** contracts/demo/scenarios.v1.json */
  scenarios: { scenarios: Scenario[] };
  /** config/policies.yaml (the policy `make demo` runs), parsed. */
  policy: PolicyInput;
  /** contracts/evaluators/*.json */
  templates: readonly EvaluatorSpec[];
  /** config/calibration/*.json: the calibrations the demo stack loads (the mock one). */
  calibrations: readonly CalibrationFileInput[];
}

export interface BuildOptions {
  /** The scenario set trafficgen sends (`--scenarios demo`). */
  set: string;
  /** Rounds of the scenario set (trafficgen --loop). */
  rounds: number;
  /**
   * Time between the starts of two rounds. trafficgen --interval is the pause after a round ends;
   * a round takes seconds, so the two differ by that much.
   */
  intervalMs: number;
  /** When the first request was sent (RFC 3339 UTC). */
  startAt: string;
  gitSha: string;
  version: string;
  /** Scenario id → why it stays off the tape for now (default AWAITING_RUNNER). */
  awaiting?: Readonly<Record<string, string>>;
}

export const DEFAULT_BUILD_OPTIONS: Omit<BuildOptions, 'gitSha' | 'version'> = {
  set: 'demo',
  rounds: 4,
  intervalMs: 40 * 60_000,
  startAt: '2026-09-28T09:00:00.000Z',
};

export interface BuiltRecording {
  manifest: Recording;
  /** traces/<trace_id>.json, oldest first. */
  details: TraceDetail[];
  /** The set's scenarios that stayed off the tape, and why. */
  skipped: SkippedScenario[];
}

export class RecordingBuildError extends Error {
  constructor(message: string) {
    super(`demo recording: ${message}`);
    this.name = 'RecordingBuildError';
  }
}

// ------------------------------------------------------------------------------------ helpers

/** RFC 3339 UTC with milliseconds, as the gateway writes times. */
function iso(ms: number): string {
  return new Date(ms).toISOString();
}

/** Deterministic hex from a seed (sha256 of its canonical JSON). */
function seedHex(...parts: (string | number)[]): string {
  return sha256Hex(parts.join('\u0000'));
}

/** The seed of a scenario's trace in a round: its trace id's random bits (uuidv7). */
export function traceSeed(scenarioId: string, round: number): string {
  return seedHex('trace', scenarioId, round);
}

/** An integer in [lo, hi] from 4 hex digits of a seed. */
function pick(hex: string, at: number, lo: number, hi: number): number {
  const n = parseInt(hex.slice(at, at + 4), 16);
  return lo + (n % (hi - lo + 1));
}

/** A canonical lowercase UUIDv7 for epoch ms `ms`, with the random bits taken from `hex`. */
export function uuidv7(ms: number, hex: string): string {
  const t = ms.toString(16).padStart(12, '0').slice(-12);
  const variant = ((parseInt(hex[3], 16) & 0x3) | 0x8).toString(16);
  return `${t.slice(0, 8)}-${t.slice(8, 12)}-7${hex.slice(0, 3)}-${variant}${hex.slice(4, 7)}-${hex.slice(7, 19)}`;
}

/**
 * A float64 written to a REAL column and read back, as the gateway serves traces.max_risk
 * (store.Float64From32): the shortest decimal that is the same float32.
 */
export function viaFloat32(x: number): number {
  const f = Math.fround(x);
  for (let digits = 1; digits <= 9; digits += 1) {
    const v = Number(f.toPrecision(digits));
    if (Math.fround(v) === f) return v;
  }
  return f;
}

/** First matching rule of contracts/ui/outcomes.v1.json, as the UI words a trace. */
export function outcomeOfSummary(t: Pick<TraceSummary, 'gate_status' | 'action' | 'would_action'>): string {
  const fields: Record<string, string | null> = { gate_status: t.gate_status, action: t.action, would_action: t.would_action };
  const rules = (outcomesDoc as unknown as { rules: { when: Record<string, readonly string[]>; outcome: string }[] }).rules;
  for (const rule of rules) {
    if (Object.entries(rule.when).every(([k, list]) => list.includes(fields[k] ?? ''))) return rule.outcome;
  }
  return 'passed';
}

/** A route's effective settings (lib/gate/route.ts), failing the build for a route the policy lacks. */
export function resolveRoute(policy: PolicyInput, name: string): ResolvedRoute {
  try {
    return resolveGateRoute(policy, name);
  } catch (err) {
    if (err instanceof RouteError) throw new RecordingBuildError(err.message);
    throw err;
  }
}

/** The route's block and fallback settings, merged over the policy defaults (policy.v1 defaults last). */
function deliverySettings(policy: PolicyInput, name: string): { blockText: string; onFail: 'block' | 'flag' | 'original' } {
  const route = policy.routes?.find((r) => r.name === name) ?? { name };
  const s = mergeSettings(policy.defaults ?? {}, route) as { block?: { style?: string; message?: string }; fallback?: { on_fail?: string } };
  const block = s.block ?? {};
  const onFail = s.fallback?.on_fail;
  return {
    // An error-style block sends an error body: no answer text is delivered (sink.go finalText).
    blockText: block.style === 'error' ? '' : block.message || DEFAULT_BLOCK_MESSAGE,
    onFail: onFail === 'flag' || onFail === 'original' ? onFail : 'block',
  };
}

/**
 * The resolved rules params of the route's enabled evaluators (backend-go gate.go resolveRules,
 * with overrides.client_rules off): each declared param at the route's value, else its default,
 * in key order (as Go marshals a map).
 */
export function resolvedRules(route: ResolvedRoute, specs: ReadonlyMap<string, Spec>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const e of route.evaluators) {
    const spec = specs.get(e.ref);
    if (!spec || e.enabled === false) continue;
    for (const [name, p] of spec.params) out[name] = e.params?.[name] ?? p.default;
  }
  return Object.fromEntries(Object.entries(out).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
}

/**
 * Each evaluator's calibration for the snapshot that answered (backend-go calibration.Entries over
 * the file lookup): an entry whose target key is the spec's calibration key, for specs without
 * params (a params target also needs the params hash, which no demo calibration has).
 */
export function calibrationFor(
  route: ResolvedRoute,
  specs: ReadonlyMap<string, Spec>,
  files: readonly CalibrationFileInput[],
  snapshot: string,
): CalibrationMap {
  const out: CalibrationMap = {};
  for (const e of route.evaluators) {
    const spec = specs.get(e.ref);
    if (!spec || e.enabled === false || e.ref in out || spec.params.size > 0) continue;
    const key = specHashes(spec).calibration_key;
    for (const file of files) {
      if (file.decision_model.snapshot !== snapshot) continue;
      const entry = file.entries.find((x) => x.target.kind === 'evaluator' && x.target.calibration_key === key && (x.target.params_sha256 ?? null) === null);
      if (!entry?.table) continue;
      out[e.ref] = { table: entry.table, quality: entry.quality as never, state: entry.quality as never, fit_state_fields: entry.fit_context?.state_fields };
      break;
    }
  }
  return out;
}

/**
 * The request's messages as a trace body stores them (backend-go gate.Messages): role, the text of
 * the content (a string, or its text and input_text parts joined with a newline; none for a tool
 * call), and the name when there is one. Tool calls and ids are not stored.
 */
export function bodyMessages(messages: readonly unknown[]): ChatMessage[] {
  return messages.map((raw) => {
    const m = (typeof raw === 'object' && raw !== null ? raw : {}) as { role?: unknown; content?: unknown; name?: unknown };
    let content = '';
    if (typeof m.content === 'string') content = m.content;
    else if (Array.isArray(m.content)) {
      content = m.content
        .filter((p): p is { type: string; text: string } => typeof p === 'object' && p !== null && (p.type === 'text' || p.type === 'input_text') && typeof p.text === 'string')
        .map((p) => p.text)
        .join('\n');
    }
    const out: ChatMessage = { role: m.role as ChatMessage['role'], content };
    if (typeof m.name === 'string' && m.name !== '') out.name = m.name;
    return out;
  });
}

/** The mock fast check: each asked question gets the scenario's scripted answer. */
function scriptedCall(id: string, mockAnswers: Record<string, MockAnswer>): JevCall {
  return {
    backend: 'mock',
    model: MOCK_SNAPSHOT,
    call: async (request) => {
      const answers: Record<string, MockAnswer> = {};
      for (const qid of Object.keys(request.body.questions)) {
        const a = mockAnswers[qid];
        check(a !== undefined, `scenario ${id} has no mock answer for ${qid} (the mock backend would refuse it in strict mode)`);
        answers[qid] = a;
      }
      return { answers: answers as never, usage: { input_tokens: request.est_tokens, cost: null }, snapshot: MOCK_SNAPSHOT, latency_ms: 0 };
    },
  };
}

function tokensOf(text: string): number {
  return est(text, DEFAULT_CPT);
}

function check(cond: boolean, message: string): asserts cond {
  if (!cond) throw new RecordingBuildError(message);
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

function riskText(x: number | null): string {
  return x === null ? 'null' : formatRisk(x);
}

// ------------------------------------------------------------------------------------ selection

export type SkipReason = 'stream' | 'api_format' | 'generated' | 'secret' | 'awaiting_runner';

export interface SkippedScenario {
  id: string;
  reason: SkipReason;
  detail: string;
}

function* stringsOf(value: unknown): Generator<string> {
  if (typeof value === 'string') yield value;
  else if (Array.isArray(value)) for (const v of value) yield* stringsOf(v);
  else if (typeof value === 'object' && value !== null) for (const v of Object.values(value)) yield* stringsOf(v);
}

function reservedHost(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, '');
  if (h === 'localhost') return true;
  if (RESERVED_DOMAINS.some((d) => h === d || h.endsWith(`.${d}`))) return true;
  return RESERVED_TLDS.some((tld) => h.endsWith(tld));
}

/** What check_recording.py would refuse in `value` (any of its strings), or null. */
export function secretIn(value: unknown): string | null {
  for (const text of stringsOf(value)) {
    for (const [name, pattern] of SECRET_PATTERNS) if (pattern.test(text)) return name;
    for (const m of text.matchAll(EMAIL)) if (!reservedHost(m[1])) return 'an e-mail address';
  }
  return null;
}

/** The set's scenarios this recorder records, in pack order, and the ones it leaves out. */
export function selectScenarios(
  scenarios: readonly Scenario[],
  set: string,
  awaiting: Readonly<Record<string, string>> = AWAITING_RUNNER,
): { recorded: Scenario[]; skipped: SkippedScenario[] } {
  const recorded: Scenario[] = [];
  const skipped: SkippedScenario[] = [];
  for (const s of scenarios) {
    if (!s.sets.includes(set)) continue;
    const secret = secretIn({ request: s.request, response: s.response, fallback: s.fallback ?? null, judge: s.judge });
    if (s.stream) skipped.push({ id: s.id, reason: 'stream', detail: 'streamed answers are not recorded here' });
    else if (s.api_format !== 'openai_chat') skipped.push({ id: s.id, reason: 'api_format', detail: `${s.api_format} is not recorded here` });
    else if (s.answer_source !== 'scripted') skipped.push({ id: s.id, reason: 'generated', detail: 'a mock tape has no generated answers' });
    else if (secret) skipped.push({ id: s.id, reason: 'secret', detail: `its text contains ${secret}, which a published recording may not carry` });
    else if (Object.hasOwn(awaiting, s.id)) skipped.push({ id: s.id, reason: 'awaiting_runner', detail: awaiting[s.id] });
    else recorded.push(s);
  }
  return { recorded, skipped };
}

// ------------------------------------------------------------------------------------ one trace

interface TraceTimes {
  /** Request received. */
  created: number;
  /** The trace's decision recorded (the gate_decision event). */
  decided: number;
  /** Deep audit finished (null without an audit). */
  audited: number | null;
}

function evidenceSummary(route: ResolvedRoute, items: readonly EvidenceItem[], packed: readonly string[], truncated: boolean): EvidenceSummary {
  const configured = route.evidence.sources;
  const used = configured
    .map((source) => {
      const mine = items.filter((it) => it.source === source && packed.includes(it.id));
      return { source, items: mine.length, labels: mine.map((it) => it.label), tokens: mine.reduce((n, it) => n + tokensOf(it.text), 0) };
    })
    .filter((u) => u.items > 0);
  return {
    configured: [...configured],
    used,
    empty: configured.filter((src) => !used.some((u) => u.source === src)),
    knowledge: 'not_requested',
    truncated,
    tokens: used.reduce((n, u) => n + u.tokens, 0),
  };
}

/** Scoring v2 (SPEC §3.2 scoring/aggregate.py) over one-hot verdict distributions. */
function score(claims: readonly ClaimView[], unsupportedPolicy: 'hallucination' | 'unverifiable'): { scores: AuditScores; verdict: AuditVerdict } {
  const decided = claims.filter((c) => c.status !== 'UNKNOWN' && c.status !== 'NOT_CHECKED');
  const dist = decided.map((c) => ({
    s: c.status === 'SUPPORTED' ? 1 : c.status === 'PARTIALLY_SUPPORTED' ? 0.5 : 0,
    c: c.status === 'CONTRADICTED' ? 1 : 0,
    u: c.status === 'UNSUPPORTED' ? 1 : c.status === 'PARTIALLY_SUPPORTED' ? 0.5 : 0,
  }));
  const n = dist.length;
  const mean = (k: 's' | 'c' | 'u') => (n === 0 ? null : dist.reduce((acc, d) => acc + d[k], 0) / n);
  const F = mean('s');
  const K = mean('c');
  const V = mean('u');
  const sumS = dist.reduce((a, d) => a + d.s, 0);
  const sumC = dist.reduce((a, d) => a + d.c, 0);
  const accuracy = sumS + sumC > 0 ? sumS / (sumS + sumC) : null;
  const clamp = (x: number) => Math.min(1, Math.max(0, x));
  const trust = F === null || K === null ? null : unsupportedPolicy === 'hallucination' ? clamp(F - K) : accuracy;
  const maxC = n === 0 ? null : Math.max(...dist.map((d) => d.c));
  const maxU = n === 0 ? 0 : Math.max(...dist.map((d) => d.u));
  let verdict: AuditVerdict;
  if (claims.length === 0) verdict = 'NO_CLAIMS';
  else if (n === 0) verdict = 'UNVERIFIABLE';
  else if ((maxC ?? 0) >= 0.35 || (unsupportedPolicy === 'hallucination' && (maxU >= 0.6 || (V ?? 0) >= 0.3))) verdict = 'HALLUCINATED';
  else if ((V ?? 0) >= 0.3 || maxU >= 0.6) verdict = 'UNVERIFIABLE';
  else verdict = 'GROUNDED';
  return {
    verdict,
    scores: {
      trust_score: trust,
      faithfulness: F,
      contradiction_rate: K,
      unverified_rate: V,
      coverage: V === null ? null : 1 - V,
      accuracy_addressed: accuracy,
      max_contradiction: maxC,
      p_hallucinated: null,
      claims_total: claims.length,
      claims_decided: n,
      claims_unknown: claims.filter((c) => c.status === 'UNKNOWN').length,
    },
  };
}

function verdictCounts(claims: readonly ClaimView[]): Record<ClaimVerdict, number> {
  const out: Record<ClaimVerdict, number> = {
    SUPPORTED: 0,
    PARTIALLY_SUPPORTED: 0,
    CONTRADICTED: 0,
    UNSUPPORTED: 0,
    UNKNOWN: 0,
    NOT_CHECKED: 0,
  };
  for (const c of claims) out[c.status] += 1;
  return out;
}

interface BuiltTrace {
  detail: TraceDetail;
  times: TraceTimes;
}

/** What one scenario runs against: its route, the specs, and the calibration the demo stack loads. */
interface TraceContext {
  policy: PolicyInput;
  route: ResolvedRoute;
  specs: ReadonlyMap<string, Spec>;
  calibration: CalibrationMap;
}

/** One gate pass through the browser pipeline (D135) with scripted answers and no caps. */
function gateOnce(s: Scenario, ctx: TraceContext, response: string, answers: Record<string, MockAnswer>, at: number): Promise<BrowserGateResult> {
  return runGate({
    route: ctx.route,
    specs: ctx.specs,
    request: s.request,
    response,
    apiFormat: 'openai_chat',
    call: scriptedCall(s.id, answers),
    ledger: NOOP_LEDGER,
    maxTokens: defaultBudget().tokens,
    calibration: ctx.calibration,
    clock: () => at,
  });
}

/** The decision/v1 document of one gate pass (attempt 0, or the fallback re-gate). */
function decisionView(
  traceId: string,
  attempt: 0 | 1,
  createdMs: number,
  route: ResolvedRoute,
  gate: BrowserGateResult,
  timing: { latencyMs: number; gateMs: number },
  summary: EvidenceSummary,
): GateDecisionView {
  const compiled = gate.compiled;
  const called = compiled.requests.length > 0;
  const inputTokens = called ? compiled.est_tokens.reduce((a, b) => a + b, 0) : 0;
  return {
    spec: 'decision/v1',
    trace_id: traceId,
    attempt,
    created_at: iso(createdMs),
    route: route.name,
    policy_version: 1,
    mode: route.mode,
    fail: route.fail,
    ...gate.decision,
    detectors: gate.detectors,
    excluded: compiled.excluded,
    decision_model: called
      ? {
          backend: 'mock',
          model_requested: MOCK_SNAPSHOT,
          snapshot: MOCK_SNAPSHOT,
          calls: compiled.requests.length,
          attempts: compiled.requests.length,
          hedged: false,
          latency_ms: timing.latencyMs,
          connect_ms: null,
          input_tokens: inputTokens,
          cost_usd: (inputTokens * MOCK_PRICE_PER_MTOK) / 1e6,
          cost_source: 'simulated',
        }
      : null,
    compile: {
      cpt: compiled.cpt,
      requests: compiled.requests.length,
      question_set_sha256: compiled.question_set_sha256,
      request_sha256: compiled.request_sha256,
      packed_evidence_ids: compiled.packed_ids,
      truncated_ids: compiled.truncated_ids,
      est_tokens: compiled.est_tokens,
    },
    evidence_summary: summary,
    late: false,
    gate_ms: timing.gateMs,
  } as GateDecisionView;
}

/** What the client got (backend-go internal/proxy gate.Final): action, delivery, text and trace flags. */
interface Delivery {
  action: GateAction;
  delivered: 'original' | 'fallback' | 'blocked';
  text: string;
  flags: string[];
}

/** The deep audit the engine returns for one answer (backend-python audit graph, mock judge). */
function auditDetailOf(
  s: Scenario,
  ids: { traceId: string; auditId: string },
  lane: 'high' | 'low',
  gateCore: BrowserGateResult['decision'],
  delivery: Delivery,
  summaryEvidence: EvidenceSummary,
  docs: readonly EvidenceItem[],
  userRequest: string,
  times: { enqueued: number; ms: number; decompose: number },
): AuditDetail {
  const judge = {
    configured_provider: MOCK_JUDGE.provider,
    effective_provider: MOCK_JUDGE.provider,
    model: MOCK_JUDGE.model,
    fallback_active: false,
    fallback_reason: null,
    structured_outputs: true,
    healthy: true,
    credential_source: 'none',
  } as AuditDetail['judge'];
  const base = {
    audit_id: ids.auditId,
    trace_id: ids.traceId,
    created_at: iso(times.enqueued),
    route: s.route,
    source: 'trafficgen',
    model: s.request.model,
    status: 'completed' as const,
    grade: null,
    processing_ms: times.ms,
    judge,
    gate_action: gateCore.action,
    selection: lane === 'high' ? ('high' as const) : ('sample' as const),
    inclusion_prob: 1,
    query_preview: userRequest,
    redacted: false,
    user_query: userRequest,
    llm_response: s.response,
    degraded_reasons: [],
    error: null,
    gate: { action: gateCore.action, would_action: gateCore.would_action, verdict: gateCore.verdict, max_risk: gateCore.max_risk, delivered: delivery.delivered },
    evidence_summary: summaryEvidence,
    context_used: [...docs],
    scoring_version: 'v2',
  };

  if (docs.length === 0) {
    // Preflight: nothing to check against, so NOT_ASSESSED (no_evidence) with no judge call.
    check(s.judge === null, `${s.id}: the audit has no evidence, so the engine never asks the judge, but the scenario scripts judge claims`);
    return {
      ...base,
      verdict: 'NOT_ASSESSED',
      not_assessed_reason: 'no_evidence',
      trust_score: null,
      hallucination_detected: false,
      verdict_counts: verdictCounts([]),
      claims: [],
      scores: null,
      reasoning_trace: NO_EVIDENCE_TRACE,
      step_timings: { total_ms: times.ms },
      unsupported_policy: 'unverifiable',
      costs: [],
    } as AuditDetail;
  }

  check(s.judge !== null, `${s.id}: the audit has evidence, so the engine asks the judge, but the scenario has no judge fixture`);
  const claims: ClaimView[] = s.judge.claims.map((c, index) => {
    const at = s.response.indexOf(c.quote);
    const spans: EvidenceSpan[] = [];
    for (const quote of c.evidence) {
      docs.forEach((doc, docIndex) => {
        const i = doc.text.indexOf(quote);
        if (i >= 0) spans.push({ doc_index: docIndex, doc_id: doc.id, start: i, end: i + quote.length, text: quote, kind: 'quote_verified' });
      });
    }
    return {
      index,
      claim: c.claim,
      status: c.verdict,
      confidence: c.confidence,
      evidence: c.evidence,
      reasoning: c.reasoning,
      decided_by: 'llm',
      verdict_probabilities: {},
      probability_source: 'llm_confidence',
      span: at === -1 ? null : { start: at, end: at + c.quote.length, method: 'exact' },
      quote: c.quote,
      evidence_spans: spans,
    };
  });
  const unsupportedPolicy = 'hallucination';
  const scored = score(claims, unsupportedPolicy);
  const scoreMs = 20;
  // The judge is called to decompose; with no claims there is nothing to verify.
  const verify = claims.length > 0 ? Math.max(times.ms - times.decompose - scoreMs, 100) : 0;
  const costs: CostRecord[] = [
    { component: 'decompose', kind: 'llm', provider: MOCK_JUDGE.provider, model: MOCK_JUDGE.model, input_tokens: 0, output_tokens: 0, cost_usd: 0, cost_source: 'simulated', latency_ms: times.decompose, status: 'ok', questions: 0 },
  ];
  if (claims.length > 0) {
    costs.push({ component: 'verify', kind: 'llm', provider: MOCK_JUDGE.provider, model: MOCK_JUDGE.model, input_tokens: 0, output_tokens: 0, cost_usd: 0, cost_source: 'simulated', latency_ms: verify, status: 'ok', questions: 0 });
  }
  const steps: Record<string, number> = claims.length > 0 ? { decompose_ms: times.decompose, verify_ms: verify, score_ms: scoreMs } : { decompose_ms: times.decompose, score_ms: scoreMs };
  return {
    ...base,
    verdict: scored.verdict,
    not_assessed_reason: null,
    trust_score: scored.scores.trust_score,
    hallucination_detected: scored.verdict === 'HALLUCINATED',
    verdict_counts: verdictCounts(claims),
    claims,
    scores: scored.scores,
    reasoning_trace: claims.length === 0 ? NO_CLAIMS_TRACE : '',
    step_timings: { ...steps, total_ms: times.ms },
    unsupported_policy: unsupportedPolicy,
    costs,
  } as AuditDetail;
}

async function buildTrace(s: Scenario, ctx: TraceContext, round: number, created: number): Promise<BuiltTrace> {
  const { route } = ctx;
  const seed = traceSeed(s.id, round);
  const auditSeed = seedHex('audit', s.id, round);
  const traceId = uuidv7(created, seed);
  const e = s.expect;
  check(route.mode === e.mode, `${s.id}: route ${route.name} is in ${route.mode} mode, the scenario expects ${e.mode}`);

  // Simulated timings of the demo stack (mock upstream answer, mock fast check, mock judge).
  const upstreamMs = pick(seed, 20, 140, 260);
  const answered = created + upstreamMs;

  // Tier 0 and the fast check (attempt 0) through the browser gate pipeline (D135).
  const gate0 = await gateOnce(s, ctx, s.response, s.mock_answers, answered);
  const core = gate0.decision;
  const latency0 = gate0.compiled.requests.length > 0 ? pick(seed, 24, 3, 9) : 0;
  const gateMs = latency0 + pick(seed, 28, 1, 3);
  // The handler's own work around the gate (reading, writing, recording).
  const handlerMs = pick(seed, 32, 2, 5);

  // The recording must show what the scenario pack promises.
  check(core.would_action === e.would_action, `${s.id}: would_action ${String(core.would_action)}, expected ${String(e.would_action)}`);
  // Status deferred is the header of an observe-mode response; the stored status is the decision's.
  if (e.status !== 'deferred') check(core.status === e.status, `${s.id}: status ${core.status}, expected ${e.status}`);
  check(core.verdict === e.verdict, `${s.id}: verdict ${String(core.verdict)}, expected ${String(e.verdict)}`);
  check(riskText(core.max_risk) === riskText(e.max_risk), `${s.id}: max_risk ${String(core.max_risk)}, expected ${String(e.max_risk)}`);
  check(sameList(core.reasons, e.reasons), `${s.id}: reasons ${core.reasons.join(',')}, expected ${e.reasons.join(',')}`);
  check(core.deep_audit === e.deep_audit, `${s.id}: deep_audit ${core.deep_audit}, expected ${e.deep_audit}`);

  const ex = gate0.extraction;
  const summaryEvidence = evidenceSummary(route, ex.items, gate0.compiled.packed_ids, gate0.compiled.truncated);
  const checkedAgainst = summaryEvidence.used.map((u) => `${u.source}:${u.items}`).join(',') || 'none';
  check(checkedAgainst === e.checked_against, `${s.id}: checked against ${checkedAgainst}, expected ${e.checked_against}`);

  // What the client got, and when the trace's decision was recorded.
  const settings = deliverySettings(ctx.policy, route.name);
  const decided0 = answered + gateMs;
  const decisions: GateDecisionView[] = [];
  let delivery: Delivery;
  let decided: number;
  let totalMs: number;
  if (core.action === 'fallback') {
    // fallback.go: the fallback answer is re-gated as attempt 1 (no audit), then delivered when
    // that passes; otherwise on_fail applies. The trace records attempt 0 last (D22).
    const fb = s.fallback;
    check(fb !== undefined, `${s.id}: the decision is fallback, but the scenario has no fallback answer to deliver`);
    const fallbackMs = pick(seed, 44, 140, 260);
    const gate1 = await gateOnce(s, ctx, fb.response, fb.mock_answers, decided0 + fallbackMs);
    const c1 = gate1.decision;
    check(c1.would_action === fb.expect.would_action, `${s.id} fallback: would_action ${String(c1.would_action)}, expected ${String(fb.expect.would_action)}`);
    check(c1.verdict === fb.expect.verdict, `${s.id} fallback: verdict ${String(c1.verdict)}, expected ${String(fb.expect.verdict)}`);
    check(riskText(c1.max_risk) === riskText(fb.expect.max_risk), `${s.id} fallback: max_risk ${String(c1.max_risk)}, expected ${String(fb.expect.max_risk)}`);
    check(sameList(c1.reasons, fb.expect.reasons), `${s.id} fallback: reasons ${c1.reasons.join(',')}, expected ${fb.expect.reasons.join(',')}`);
    const latency1 = gate1.compiled.requests.length > 0 ? pick(seed, 36, 3, 9) : 0;
    const gate1Ms = latency1 + pick(seed, 40, 1, 3);
    const decided1 = decided0 + fallbackMs + gate1Ms;
    const s1 = evidenceSummary(route, gate1.extraction.items, gate1.compiled.packed_ids, gate1.compiled.truncated);
    decisions.push(decisionView(traceId, 1, decided1, route, gate1, { latencyMs: latency1, gateMs: gate1Ms }, s1));
    if (c1.action === 'pass' || c1.action === 'flag') delivery = { action: 'fallback', delivered: 'fallback', text: fb.response, flags: ['fallback_used'] };
    else if (settings.onFail === 'flag') delivery = { action: 'flag', delivered: 'fallback', text: fb.response, flags: ['fallback_used'] };
    else if (settings.onFail === 'original') delivery = { action: 'flag', delivered: 'original', text: s.response, flags: [] };
    else delivery = { action: 'block', delivered: 'blocked', text: settings.blockText, flags: [] };
    decided = decided1 + handlerMs;
    totalMs = decided - created;
  } else if (core.action === 'block') {
    delivery = { action: 'block', delivered: 'blocked', text: settings.blockText, flags: [] };
    decided = decided0 + handlerMs;
    totalMs = decided - created;
  } else if (route.mode === 'observe') {
    // handler.go respondThenGate: the answer goes out unchanged first, the gate runs after it, so
    // total_ms ends when the response was written.
    check(e.status === 'deferred', `${s.id}: an observe-mode response says Status deferred, the scenario expects ${e.status}`);
    delivery = { action: core.action, delivered: 'original', text: s.response, flags: [] };
    totalMs = upstreamMs + handlerMs;
    decided = created + totalMs + gateMs;
  } else {
    delivery = { action: core.action, delivered: 'original', text: s.response, flags: [] };
    decided = decided0 + handlerMs;
    totalMs = decided - created;
  }
  check(delivery.action === e.action, `${s.id}: action ${delivery.action}, expected ${e.action}`);
  if (e.delivered_text !== undefined) check(delivery.text === e.delivered_text, `${s.id}: delivered ${JSON.stringify(delivery.text)}, expected ${JSON.stringify(e.delivered_text)}`);
  const decisionCreated = route.mode === 'observe' ? decided : decided0;
  decisions.unshift(decisionView(traceId, 0, decisionCreated, route, gate0, { latencyMs: latency0, gateMs }, summaryEvidence));
  const decision0 = decisions[0];

  // Deep audit selection (every demo route audits each answer it may audit: pass_sample_rate 1;
  // grounding triggers take the high lane). The audit checks the original answer.
  const auditLane = core.deep_audit === 'high' ? 'high' : core.deep_audit === 'sample' && route.deepAudit.pass_sample_rate >= 1 ? 'low' : null;
  check(auditLane !== null || core.status === 'not_assessed', `${s.id}: an answer the gate checked without a deep audit is not modelled (deep_audit ${core.deep_audit})`);
  const userRequest = ex.user_request;
  let auditDetail: AuditDetail | null = null;
  let auditDone: number | null = null;
  if (auditLane !== null) {
    const engineNotAssessed = ex.items.length === 0;
    const auditMs = engineNotAssessed ? pick(auditSeed, 20, 2, 8) : pick(auditSeed, 20, 900, 1600);
    auditDetail = auditDetailOf(
      s,
      { traceId, auditId: uuidv7(decided, auditSeed) },
      auditLane,
      core,
      delivery,
      summaryEvidence,
      ex.items,
      userRequest,
      { enqueued: decided, ms: auditMs, decompose: pick(auditSeed, 24, 300, 500) },
    );
    check(auditDetail.verdict === e.audit_verdict, `${s.id}: audit verdict ${String(auditDetail.verdict)}, expected ${e.audit_verdict}`);
    auditDone = decided + auditMs;
  }

  const body: TraceDetail['body'] = {
    available: true,
    unavailable_reason: null,
    messages: bodyMessages(s.request.messages),
    user_request: userRequest,
    system_instructions: ex.system_instructions,
    response_text: s.response,
    final_response_text: delivery.text === '' ? null : delivery.text,
    evidence: ex.items,
    rules: resolvedRules(route, ctx.specs),
    truncated: false,
  };

  // The trace's audit as the store reports it: not_assessed for an audit with nothing to check.
  const auditStatus = auditDetail === null ? 'not_assessed' : auditDetail.verdict === 'NOT_ASSESSED' ? 'not_assessed' : 'completed';
  const summary: TraceSummary = {
    trace_id: traceId,
    client_request_id: null,
    created_at: iso(created),
    updated_at: iso(auditDone ?? decided),
    source: 'trafficgen',
    api_format: 'openai_chat',
    route: route.name,
    policy_version: 1,
    mode: route.mode,
    effective_mode: core.effective_mode,
    model: s.request.model,
    stream: false,
    upstream_status: 200,
    answer_source: s.answer_source,
    action: delivery.action,
    would_action: core.would_action,
    gate_status: core.status,
    skip_reason: core.skip_reason,
    verdict: core.verdict,
    max_risk: core.max_risk === null ? null : viaFloat32(core.max_risk),
    reasons: core.reasons,
    checked_against: summaryEvidence,
    timings: {
      upstream_ms: upstreamMs,
      ttft_ms: null,
      gate_ms: gateMs,
      overhead_ms: totalMs - upstreamMs,
      total_ms: totalMs,
    },
    decision_model: decision0.decision_model ? { backend: 'mock', snapshot: MOCK_SNAPSHOT, cost_usd: decision0.decision_model.cost_usd } : null,
    audit: auditDetail
      ? {
          audit_id: auditDetail.audit_id,
          status: auditStatus,
          lane: auditLane,
          selection: auditDetail.selection,
          verdict: auditDetail.verdict,
          trust_score: auditDetail.trust_score,
          grade: auditDetail.grade,
          verdict_counts: auditDetail.verdict_counts,
        }
      : { audit_id: null, status: 'not_assessed', lane: null, selection: null, verdict: 'NOT_ASSESSED', trust_score: null, grade: null, verdict_counts: null },
    question_preview: userRequest,
    response_preview: s.response,
    redacted: false,
    flags: delivery.flags,
  } as TraceSummary;
  check(e.audit === (auditDetail ? 'queued' : summary.audit.status), `${s.id}: audit ${auditDetail ? 'queued' : summary.audit.status}, expected ${e.audit}`);
  check(outcomeOfSummary(summary) === e.outcome, `${s.id}: outcome ${outcomeOfSummary(summary)}, expected ${e.outcome}`);

  return {
    detail: { ...summary, body, decisions, audit_detail: auditDetail, labels: [] },
    times: { created, decided, audited: auditDone },
  };
}

// ------------------------------------------------------------------------------------ events

type RecordingEvent = Recording['events'][number];

/** The summary as trace_started showed it (recording.go pendingSummary). */
export function pendingSummary(s: TraceSummary): TraceSummary {
  return {
    ...s,
    updated_at: s.created_at,
    effective_mode: null,
    upstream_status: null,
    action: null,
    would_action: null,
    skip_reason: null,
    verdict: null,
    max_risk: null,
    gate_status: 'pending',
    reasons: [],
    checked_against: null,
    timings: { upstream_ms: null, ttft_ms: null, gate_ms: null, overhead_ms: null, total_ms: null },
    decision_model: null,
    audit: { audit_id: null, status: 'none', lane: null, selection: null, verdict: null, trust_score: null, grade: null, verdict_counts: null },
    response_preview: null,
  };
}

/** The summary as gate_decision showed it: decided, a planned audit still queued (recording.go decidedSummary). */
export function decidedSummary(s: TraceSummary): TraceSummary {
  if (['running', 'completed', 'degraded', 'failed'].includes(s.audit.status)) {
    return {
      ...s,
      audit: { audit_id: s.audit.audit_id, status: 'queued', lane: s.audit.lane, selection: s.audit.selection, verdict: null, trust_score: null, grade: null, verdict_counts: null },
    };
  }
  return s;
}

/** recording.go events(): the WS v2 stream of every trace, t_ms after the first request. */
function events(built: readonly BuiltTrace[]): RecordingEvent[] {
  const start = built[0].times.created;
  const ms = (t: number) => Math.max(t - start, 0);
  const out: RecordingEvent[] = [];
  for (const { detail, times } of built) {
    const summary: TraceSummary = { ...detail };
    delete (summary as Partial<TraceDetail>).body;
    delete (summary as Partial<TraceDetail>).decisions;
    delete (summary as Partial<TraceDetail>).audit_detail;
    delete (summary as Partial<TraceDetail>).labels;
    const at = ms(times.created);
    out.push({ t_ms: at, type: 'trace_started', trace_id: summary.trace_id, data: { trace: pendingSummary(summary) } });
    const decidedAt = summary.timings.total_ms !== null ? at + summary.timings.total_ms : at;
    out.push({ t_ms: decidedAt, type: 'gate_decision', trace_id: summary.trace_id, data: { trace: decidedSummary(summary), decision: detail.decisions[0] } });
    if (summary.audit.audit_id && summary.audit.lane) {
      out.push({ t_ms: decidedAt, type: 'audit_queued', trace_id: summary.trace_id, data: { audit_id: summary.audit.audit_id, lane: summary.audit.lane, queue_depth: 0 } });
    }
    const a = detail.audit_detail;
    if (a) {
      const done = Math.max(ms(Date.parse(a.created_at)) + a.processing_ms, decidedAt);
      out.push({ t_ms: done, type: 'audit_completed', trace_id: summary.trace_id, data: { audit: a } });
    }
  }
  // A stable sort by time keeps each trace's own order.
  return out.map((ev, i) => ({ ev, i })).sort((x, y) => x.ev.t_ms - y.ev.t_ms || x.i - y.i).map((x) => x.ev);
}

/** The CalibrationState values from weakest to strongest (recording.go calibrationStates). */
const CALIBRATION_STATES: Recording['calibration_state'][] = ['none', 'mock', 'proxy', 'family', 'provisional', 'measured'];

/** The weakest calibration of the active, assessed, non-internal evaluators ("none" when there are none). */
function calibrationState(details: readonly TraceDetail[]): Recording['calibration_state'] {
  let weakest = CALIBRATION_STATES.length - 1;
  let found = false;
  for (const d of details) {
    for (const decision of d.decisions) {
      for (const ev of Object.values(decision.evaluators)) {
        if (ev.stage !== 'active' || ev.internal || ev.status !== 'assessed') continue;
        found = true;
        weakest = Math.min(weakest, Math.max(CALIBRATION_STATES.indexOf(ev.calibration_state), 0));
      }
    }
  }
  return found ? CALIBRATION_STATES[weakest] : 'none';
}

function roiWindow(spanMs: number): Recording['roi']['window'] {
  const windows: [Recording['roi']['window'], number][] = [
    ['15m', 15 * 60_000],
    ['1h', 3_600_000],
    ['24h', 86_400_000],
    ['7d', 7 * 86_400_000],
  ];
  return windows.find(([, d]) => spanMs <= d)?.[0] ?? '30d';
}

// ------------------------------------------------------------------------------------ build

/**
 * The demo recording: `rounds` rounds of the set's recordable scenarios (selectScenarios), round
 * starts `intervalMs` apart, each scenario sent after the previous one answered. Throws
 * RecordingBuildError when a decision, delivery or audit differs from its scenario's `expect`.
 */
export async function buildDemoRecording(inputs: BuildInputs, opts: BuildOptions): Promise<BuiltRecording> {
  const { recorded: scenarios, skipped } = selectScenarios(inputs.scenarios.scenarios, opts.set, opts.awaiting ?? AWAITING_RUNNER);
  if (scenarios.length === 0) throw new RecordingBuildError(`no recordable scenario is in set ${JSON.stringify(opts.set)}`);
  if (!Number.isInteger(opts.rounds) || opts.rounds < 1) throw new RecordingBuildError('rounds must be a positive integer');
  const start = Date.parse(opts.startAt);
  if (Number.isNaN(start)) throw new RecordingBuildError(`startAt ${JSON.stringify(opts.startAt)} is not a time`);
  const specs = specMap(inputs.templates);
  const contexts = new Map<string, TraceContext>();
  const contextOf = (name: string): TraceContext => {
    let ctx = contexts.get(name);
    if (!ctx) {
      const route = resolveRoute(inputs.policy, name);
      ctx = { policy: inputs.policy, route, specs, calibration: calibrationFor(route, specs, inputs.calibrations, MOCK_SNAPSHOT) };
      contexts.set(name, ctx);
    }
    return ctx;
  };

  const built: BuiltTrace[] = [];
  for (let round = 0; round < opts.rounds; round += 1) {
    let t = start + round * opts.intervalMs;
    // trafficgen sends one request at a time: a round starts once the last answer of the round before it is in.
    check(built.length === 0 || t > built[built.length - 1].times.decided, `round ${round} starts before round ${round - 1} ended: raise intervalMs`);
    for (const s of scenarios) {
      const trace = await buildTrace(s, contextOf(s.route), round, t);
      built.push(trace);
      // trafficgen sends the next scenario once this one answered.
      t = trace.times.decided + pick(seedHex('gap', s.id, round), 0, 40, 120);
    }
  }

  const details = built.map((b) => b.detail);
  const summaries = details.map((d) => {
    const s: TraceSummary & Partial<Pick<TraceDetail, 'body' | 'decisions' | 'audit_detail' | 'labels'>> = { ...d };
    delete s.body;
    delete s.decisions;
    delete s.audit_detail;
    delete s.labels;
    return s as TraceSummary;
  });
  const decisionUsd = summaries.reduce((acc, s) => acc + (s.decision_model?.cost_usd ?? 0), 0);
  const last = built[built.length - 1].times.created;
  const manifest: Recording = {
    schema: RECORDING_SCHEMA,
    kind: 'mock',
    label: MOCK_LABEL,
    recorded_at: iso(built[0].times.created),
    git_sha: opts.gitSha,
    trustagent_version: opts.version,
    decision_model: { backend: 'mock', snapshot: MOCK_SNAPSHOT },
    judge: { provider: MOCK_JUDGE.provider, model: MOCK_JUDGE.model, fallback_active: false },
    calibration_state: calibrationState(details),
    answer_sources: {
      scripted: summaries.filter((s) => s.answer_source === 'scripted').length,
      generated: summaries.filter((s) => s.answer_source === 'generated').length,
    },
    totals: { traces: summaries.length, decision_usd: Number(decisionUsd.toFixed(9)), llm_usd: 0 },
    // export-replay writes only the schema minimum; the replay derives the rest from the traces.
    roi: {
      window: roiWindow(last - built[0].times.created),
      gated_responses: summaries.filter((s) => s.gate_status !== 'pending' && s.gate_status !== 'skipped').length,
      simulated: true,
    } as Recording['roi'],
    traces: summaries,
    events: events(built),
  };
  // Belt and braces: nothing a published recording may not carry, wherever it came from.
  const leak = secretIn({ manifest, details });
  check(leak === null, `the recording contains ${String(leak)}`);
  return { manifest, details, skipped };
}
