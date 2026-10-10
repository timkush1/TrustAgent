/**
 * The static Studio's test bench in the browser (D107, D135): one Studio sample through the one
 * browser gate pipeline (`runGate`) on a one-evaluator route, the way the gateway's
 * `POST /api/studio/test` runs it (gstudio `CompileUnsaved` and `Runner.Evaluate`, cmd/proxy
 * studio_api.go `runner`):
 *
 * - the unsaved spec is compiled as the version a save would create (latest + 1);
 * - the route is the policy's defaults with only that evaluator (its threshold overrides, if any),
 *   in observe mode, with on_no_evidence `ignore` and no calibration required, so exactly one
 *   question is asked and an answer without the evidence it needs is simply not assessed;
 * - the sample's own rules act as the request's trustagent.rules (client rules on), and its
 *   documents arrive as trustagent.context;
 * - the row is gstudio `ResultOf`: the tested evaluator's result, `fires` when it reached warn or
 *   critical, and the error reasons of a stage that failed.
 *
 * Costs are priced with the TS cost core (D150): the vendor's own `usage.cost` when the response
 * carried it (vendor), otherwise tokens × the price table (estimate). Pure, apart from the clock
 * runGate reads.
 */
import prices from '@contracts/pricing/prices.v1.json';
import { CompileError } from '@/lib/decision/compile';
import { lookupPrice, priceCall, type CallCost } from '@/lib/decision/cost';
import { parseSpec, type Spec } from '@/lib/decision/spec';
import { mergeSettings, resolveRoute } from '@/lib/gate/route';
import { GateRefusedError } from '@/lib/gate/runner';
import type { BrowserGateResult, PolicyInput, ResolvedRoute, RouteEvaluatorRef } from '@/lib/gate/types';
import type { CallUsage } from '@/lib/openrouter/types';
import type { CostRecord, EvaluatorSpec, PricingTable, StudioSample, StudioTestRequest, StudioTestResponse } from '@/types/api';

export const PRICE_TABLE = prices as PricingTable;
/** Every call the browser makes goes through OpenRouter (D100). */
export const LIVE_PROVIDER = 'openrouter';
/** The route name a test runs under (Go policy.DefaultRouteName). */
export const STUDIO_ROUTE = 'default';
/** Every evidence source counts in a test (Go studioRoute: store.EvidenceSources). */
const ALL_SOURCES = ['request_system', 'request_context', 'tool_results', 'extension'] as const;

export type TestRow = StudioTestResponse['results'][number];
export type CostSource = CostRecord['cost_source'];

// ------------------------------------------------------------------------------------ the spec

/** An unsaved spec as a test runs it (Go studio.Unsaved). */
export interface UnsavedSpec {
  /** The document with `version` = latest + 1 (every other member keeps its value and order). */
  doc: EvaluatorSpec;
  spec: Spec;
  /** `<id>@<latest+1>`. */
  ref: string;
}

/**
 * Prepares an unsaved spec (already validated) for a run: its version becomes latest + 1, where
 * latest is the newest stored version of its id (0 for a new id). Throws SpecError when it does not
 * parse.
 */
export function compileUnsaved(doc: EvaluatorSpec, latest: number): UnsavedSpec {
  if (!Number.isSafeInteger(latest) || latest < 0) throw new RangeError(`latest version ${latest} is not a whole number >= 0`);
  const next = { ...doc, version: latest + 1 } as EvaluatorSpec;
  const spec = parseSpec(next);
  return { doc: next, spec, ref: `${spec.id}@${spec.version}` };
}

// ----------------------------------------------------------------------------------- the route

/**
 * The route a test runs on: the policy's defaults (detectors, evidence settings) with only the
 * tested evaluator, observe mode, on_no_evidence `ignore`, no calibration required, client rules on
 * and every evidence source.
 */
export function studioTestRoute(policy: PolicyInput, ref: string, thresholds?: StudioTestRequest['thresholds']): ResolvedRoute {
  const evaluator: RouteEvaluatorRef = { ref };
  if (thresholds && Object.keys(thresholds).length > 0) evaluator.thresholds = { ...thresholds } as RouteEvaluatorRef['thresholds'];
  const base = resolveRoute({ version: policy.version, defaults: policy.defaults, routes: [{ name: STUDIO_ROUTE }] }, STUDIO_ROUTE);
  return {
    ...base,
    mode: 'observe',
    requireCalibration: false,
    onNoEvidence: 'ignore',
    evidence: mergeSettings(base.evidence, { sources: [...ALL_SOURCES] }) as unknown as ResolvedRoute['evidence'],
    evaluators: [evaluator],
    clientRules: true,
  };
}

// ---------------------------------------------------------------------------------- the sample

/**
 * A Studio sample as the client request runGate reads (openai_chat): the system instructions, the
 * user's message, the documents as trustagent.context and the sample's rules as trustagent.rules.
 */
export function sampleRequest(sample: StudioSample): Record<string, unknown> {
  const messages: { role: string; content: string }[] = [];
  if (sample.system_instructions) messages.push({ role: 'system', content: sample.system_instructions });
  if (sample.user_request) messages.push({ role: 'user', content: sample.user_request });
  const trustagent: Record<string, unknown> = {};
  const docs = (sample.evidence ?? []).filter((e) => e.text.trim() !== '');
  if (docs.length > 0) trustagent.context = docs.map((e) => ({ ...(e.id ? { id: e.id } : {}), ...(e.source ? { source: e.source } : {}), text: e.text }));
  if (sample.rules && Object.keys(sample.rules).length > 0) trustagent.rules = { ...sample.rules };
  return { model: 'studio-test', messages, ...(Object.keys(trustagent).length > 0 ? { trustagent } : {}) };
}

// ------------------------------------------------------------------------------------- pricing

/** A whole, non-negative token count (an estimate rounds up: a worst case is never understated; D150). */
function wholeTokens(n: number | undefined): number | null {
  return typeof n === 'number' && Number.isFinite(n) ? Math.max(0, Math.ceil(n)) : null;
}

/**
 * One call's cost: the vendor's usage.cost when present (reported), otherwise its tokens at the
 * price table's price for `model` on OpenRouter (computed). Output tokens that were not reported
 * leave a call that has a price for them unknown, never $0.
 */
export function liveCallCost(usage: CallUsage, model: string, table: PricingTable = PRICE_TABLE): CallCost {
  const input = wholeTokens(usage.input_tokens) ?? 0;
  const output = wholeTokens(usage.output_tokens);
  const priced = priceCall({ provider: LIVE_PROVIDER, model, input_tokens: input, output_tokens: output ?? 0, reported_cost_usd: usage.cost, table });
  if (priced.cost_source !== 'computed' || output !== null) return priced;
  return lookupPrice(table.entries, LIVE_PROVIDER, model)?.output_per_mtok === 0 ? priced : { ...priced, cost_usd: null, cost_source: 'unknown' };
}

/**
 * The cost of several calls as one figure: reported when every call was, unknown when any call has
 * no price (the sum then counts the priced calls only), computed otherwise; $0 computed with none.
 */
export function combinedCost(costs: readonly CallCost[]): { cost_usd: number; cost_source: CostSource } {
  const usd = costs.reduce((sum, c) => sum + (c.cost_usd ?? 0), 0);
  if (costs.length === 0) return { cost_usd: 0, cost_source: 'computed' };
  if (costs.some((c) => c.cost_source === 'unknown')) return { cost_usd: usd, cost_source: 'unknown' };
  if (costs.every((c) => c.cost_source === 'reported')) return { cost_usd: usd, cost_source: 'reported' };
  return { cost_usd: usd, cost_source: 'computed' };
}

// ----------------------------------------------------------------------------------------- rows

const agreesWith = (expected: StudioSample['expected'], fires: boolean | null): boolean | null =>
  expected === undefined || fires === null ? null : (expected === 'fire') === fires;

/** The tested evaluator's row of a gate result (gstudio ResultOf). */
export function resultRow(result: BrowserGateResult, ref: string, sample: StudioSample, cost: number): TestRow {
  const e = result.decision.evaluators[ref];
  const base = { sample_id: sample.id, latency_ms: result.latency_ms, cost_usd: cost, expected: sample.expected ?? null };
  if (e === undefined) {
    return { ...base, status: 'not_assessed', reason: 'not_run', answer: null, probabilities: null, risk_raw: null, risk: null, level: null, escalate: null, fires: null, agrees: null };
  }
  const fires = e.status === 'assessed' && e.level !== null ? e.level === 'warn' || e.level === 'critical' : null;
  return {
    ...base,
    status: e.status,
    reason: e.reason,
    answer: e.answer,
    probabilities: e.probabilities,
    risk_raw: e.risk_raw,
    risk: e.risk,
    level: e.level,
    escalate: e.escalate,
    fires,
    agrees: agreesWith(sample.expected, fires),
  };
}

/** The reason of a sample whose pipeline stopped before a decision (gstudio ResultOf). */
export function failureReason(err: unknown): string {
  if (err instanceof CompileError) return err.message.includes('does not fit') ? 'error:too_large' : 'error:compile';
  // The per-call token cap refused the sample before any call: too large to send.
  if (err instanceof GateRefusedError && err.reason === 'token_cap') return 'error:too_large';
  const code = typeof err === 'object' && err !== null ? (err as { code?: unknown }).code : undefined;
  return typeof code === 'string' && code !== '' ? `error:${code}` : 'error:internal';
}

/** The row of a sample whose pipeline failed. */
export function errorRow(sample: StudioSample, reason: string, latencyMs: number): TestRow {
  return {
    sample_id: sample.id,
    status: 'error',
    reason,
    answer: null,
    probabilities: null,
    risk_raw: null,
    risk: null,
    level: null,
    escalate: null,
    fires: null,
    latency_ms: latencyMs,
    cost_usd: 0,
    expected: sample.expected ?? null,
    agrees: null,
  };
}

/** The summary of a run (Go studioTestSummary): agreement over the rows that have a label and an outcome. */
export function testSummary(rows: readonly TestRow[], costUsd: number, snapshot: string | null, backend: StudioTestResponse['summary']['backend']): StudioTestResponse['summary'] {
  const compared = rows.filter((r) => r.agrees !== null);
  return {
    n: rows.length,
    assessed: rows.filter((r) => r.status === 'assessed').length,
    agreement: compared.length > 0 ? compared.filter((r) => r.agrees).length / compared.length : null,
    cost_usd: costUsd,
    snapshot,
    backend,
  };
}
