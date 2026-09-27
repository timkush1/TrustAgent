/**
 * Contract checks for src/types/api.ts against contracts/schema/api.v1.schema.json.
 *
 * The schema is generated from api.ts (SPEC §2.4), so the two must never drift. Every list below is
 * proven at compile time to hold exactly the members of its TypeScript union; the runtime
 * assertions then compare it with the committed schema. Editing either file without the other
 * fails here (tsc -b for api.ts, vitest for the schema).
 */
import { describe, it, expect, expectTypeOf } from 'vitest';
import type * as Api from './api';

type JsonSchema = {
  $schema?: string;
  $defs?: Record<string, JsonSchema>;
  enum?: unknown[];
  const?: unknown;
  anyOf?: JsonSchema[];
  properties?: Record<string, JsonSchema>;
  required?: string[];
};

const schemaModules = import.meta.glob<JsonSchema>('../../../contracts/schema/api.v1.schema.json', {
  eager: true,
  import: 'default',
});
const schema = Object.values(schemaModules)[0];

/**
 * Compile-time proof that a literal list holds only members of union U (`L extends U[]`) and every
 * one of them (otherwise the argument must also carry a `missing` property naming the gap).
 */
const exhaustive =
  <U extends string>() =>
  <const L extends readonly U[]>(
    list: L & ([U] extends [L[number]] ? unknown : { missing: Exclude<U, L[number]> }),
  ): readonly string[] =>
    list;

// Proto-mirrored enums (UPPER_SNAKE, SPEC §2.1).
const upperSnake = {
  ClaimVerdict: exhaustive<Api.ClaimVerdict>()([
    'SUPPORTED',
    'PARTIALLY_SUPPORTED',
    'CONTRADICTED',
    'UNSUPPORTED',
    'UNKNOWN',
    'NOT_CHECKED',
  ]),
  AuditVerdict: exhaustive<Api.AuditVerdict>()([
    'GROUNDED',
    'HALLUCINATED',
    'UNVERIFIABLE',
    'NO_CLAIMS',
    'NOT_ASSESSED',
  ]),
};

// Every other enum in the core subset is lower case.
const lowerCase = {
  AuditStatus: exhaustive<Api.AuditStatus>()([
    'queued',
    'running',
    'completed',
    'degraded',
    'failed',
  ]),
  TraceAuditStatus: exhaustive<Api.TraceAuditStatus>()([
    'none',
    'queued',
    'sampled_out',
    'dropped',
    'disabled',
    'running',
    'completed',
    'degraded',
    'failed',
    'not_assessed',
  ]),
  AuditSelection: exhaustive<Api.AuditSelection>()([
    'high',
    'sample',
    'error_sample',
    'uniform',
    'direct',
    'playground',
    'studio',
  ]),
  PolicyMode: exhaustive<Api.PolicyMode>()(['observe', 'flag', 'block', 'fallback']),
  GateAction: exhaustive<Api.GateAction>()(['observe', 'pass', 'flag', 'block', 'fallback']),
  WouldAction: exhaustive<Api.WouldAction>()(['pass', 'flag', 'block']),
  GateStatus: exhaustive<Api.GateStatus>()([
    'ok',
    'partial',
    'not_assessed',
    'skipped',
    'timeout',
    'error',
  ]),
  GateVerdict: exhaustive<Api.GateVerdict>()([
    'supported',
    'unsupported',
    'contradicted',
    'not_assessed',
    'unknown',
  ]),
  EvaluatorLevel: exhaustive<Api.EvaluatorLevel>()(['ok', 'warn', 'critical']),
  EvaluatorResultStatus: exhaustive<Api.EvaluatorResultStatus>()([
    'assessed',
    'not_assessed',
    'error',
  ]),
  SkipReason: exhaustive<Api.SkipReason>()([
    'unparseable_request',
    'upstream_error',
    'no_text',
    'too_long',
    'sampled_out',
    'overload',
    'budget',
    'decision_model_off',
    'breaker_open',
    'rate_limited',
    'client_disconnected',
    'undecodable',
    'unsupported_api',
    'multi_choice',
    'credentials_unavailable',
    'legacy',
  ]),
  Degraded: exhaustive<Api.Degraded>()([
    'uncalibrated',
    'budget',
    'breaker',
    'rate_limited',
    'timeout',
    'error',
    'overload',
    'off',
  ]),
  // Backend ids are hyphenated; the underscore spelling is superseded (SPEC Appendix A).
  DecisionBackend: exhaustive<Api.DecisionBackend>()([
    'openrouter-decisions',
    'openrouter-systemone',
    'typesafe',
    'compatible',
    'mock',
    'off',
  ]),
  EvidenceSource: exhaustive<Api.EvidenceSource>()([
    'request_system',
    'request_context',
    'tool_results',
    'extension',
    'knowledge',
  ]),
  DecidedBy: exhaustive<Api.DecidedBy>()([
    'llm',
    'rule',
    'none',
    'jev',
    'jev>llm',
    'jev>llm_failed',
  ]),
  TraceSource: exhaustive<Api.TraceSource>()([
    'proxy',
    'direct',
    'playground',
    'test',
    'trafficgen',
  ]),
  ApiFormat: exhaustive<Api.ApiFormat>()([
    'openai_chat',
    'openai_completions',
    'anthropic_messages',
    'direct',
  ]),
  Scope: exhaustive<Api.Scope>()(['ingest', 'read', 'content', 'admin']),
  CalibrationState: exhaustive<Api.CalibrationState>()([
    'measured',
    'provisional',
    'proxy',
    'mock',
    'family',
    'none',
  ]),
  ComponentStateName: exhaustive<Api.ComponentStateName>()([
    'up',
    'degraded',
    'down',
    'disabled',
    'unknown',
  ]),
  DetectorId: exhaustive<Api.DetectorId>()([
    'numbers_not_in_evidence',
    'dates_not_in_evidence',
    'urls_not_in_evidence',
    'quotes_not_in_evidence',
    'citation_out_of_range',
    'secret_pattern',
    'system_prompt_leak',
  ]),
};

const wsEventTypes = exhaustive<Api.WsEvent['type']>()([
  'hello',
  'hb',
  'trace_started',
  'gate_decision',
  'audit_queued',
  'audit_started',
  'audit_completed',
  'audit_failed',
  'audit_dropped',
  'gap',
  'policy_updated',
  'policy_error',
  'evaluator_updated',
  'calibration_updated',
  'decision_model_changed',
  'status',
  'credentials_changed',
  'settings_updated',
]);

// The S0 core subset the schema must describe (health/status, traces, gate decisions, audits with
// claims and UTF-16 spans, the WS v2 union and the error body).
const coreRoots = [
  'ApiErrorBody',
  'HealthStatus',
  'ReadyStatus',
  'SystemStatus',
  'SystemStatusLite',
  'TraceSummary',
  'TraceDetail',
  'Page<TraceSummary>',
  'GateDecisionView',
  'GateDecisionRow',
  'Page<GateDecisionRow>',
  'AuditSummary',
  'AuditDetail',
  'Page<AuditSummary>',
  'ClaimView',
  'EvidenceSpan',
  'DirectAuditRequest',
  'WsEvent',
  'WsHello',
] as const;

const defs = (): Record<string, JsonSchema> => schema?.$defs ?? {};
const sorted = (values: readonly unknown[] | undefined) => [...(values ?? [])].map(String).sort();

describe('api.v1.schema.json', () => {
  it('is loaded as a draft 2020-12 schema with $defs', () => {
    expect(schema?.$schema).toBe('https://json-schema.org/draft/2020-12/schema');
    expect(Object.keys(defs()).length).toBeGreaterThan(0);
  });

  it.each(coreRoots)('describes the core type %s', (name) => {
    expect(defs()[name]).toBeDefined();
  });

  it.each(Object.entries({ ...upperSnake, ...lowerCase }))(
    'has the same %s values as api.ts',
    (name, values) => {
      expect(sorted(defs()[name]?.enum)).toEqual(sorted(values));
    },
  );

  it('has the same WsEvent types as api.ts', () => {
    const consts = (defs().WsEvent?.anyOf ?? []).map((variant) => variant.properties?.type?.const);
    expect(sorted(consts)).toEqual(sorted(wsEventTypes));
  });

  it('keeps hb bare: no seq, no epoch, no data', () => {
    const hb = defs().WsEvent?.anyOf?.find((variant) => variant.properties?.type?.const === 'hb');
    expect(Object.keys(hb?.properties ?? {}).sort()).toEqual(['ts', 'type', 'v']);
  });

  it('requires every ClaimVerdict key in verdict_counts', () => {
    expect(sorted(defs().AuditSummary?.properties?.verdict_counts?.required)).toEqual(
      sorted(upperSnake.ClaimVerdict),
    );
  });
});

describe('api.ts enum casing (SPEC §2.1)', () => {
  it.each(Object.entries(upperSnake))('%s is UPPER_SNAKE', (_name, values) => {
    for (const value of values) expect(value).toMatch(/^[A-Z]+(_[A-Z]+)*$/);
  });

  it.each(Object.entries(lowerCase))('%s has no upper-case values', (_name, values) => {
    for (const value of values) expect(value).toBe(value.toLowerCase());
  });
});

describe('api.ts superseded identifiers (SPEC Appendix A)', () => {
  it('rejects the superseded enum values at compile time', () => {
    // These lines name superseded identifiers on purpose, to assert their absence.
    type SupersededGateAction = 'pending' | 'not_assessed'; // check-forbidden: ignore
    type SupersededBackend = 'openrouter_decisions' | 'openrouter_systemone'; // check-forbidden: ignore
    type SupersededEscalate = 'numeric_or_date'; // check-forbidden: ignore
    expectTypeOf<Extract<Api.GateAction, SupersededGateAction>>().toEqualTypeOf<never>();
    expectTypeOf<
      Extract<Api.DecisionBackend, SupersededBackend | 'custom'>
    >().toEqualTypeOf<never>();
    expectTypeOf<
      Extract<Api.EvaluatorResult['escalate'], SupersededEscalate>
    >().toEqualTypeOf<never>();
  });

  it('keeps hb frames without seq or data at compile time', () => {
    type Hb = Extract<Api.WsEvent, { type: 'hb' }>;
    expectTypeOf<keyof Hb>().toEqualTypeOf<'v' | 'type' | 'ts'>();
    type Enveloped = Exclude<Api.WsEvent, Hb>;
    expectTypeOf<Enveloped['seq']>().toEqualTypeOf<number>();
    expectTypeOf<Enveloped['epoch']>().toEqualTypeOf<string>();
  });
});

describe('UTF-16 spans (SPEC §2.1)', () => {
  it('slice the answer text directly with half-open UTF-16 offsets', () => {
    const answer = 'Hi 😀 Paris is the capital.';
    const claim: Pick<Api.ClaimView, 'claim' | 'span'> = {
      claim: 'Paris is the capital.',
      // '😀' is two UTF-16 code units, so the claim starts at 6, not at code point 5.
      span: { start: 6, end: answer.length, method: 'exact' },
    };
    const span = claim.span;
    expect(span).not.toBeNull();
    if (span) expect(answer.slice(span.start, span.end)).toBe(claim.claim);
  });
});
