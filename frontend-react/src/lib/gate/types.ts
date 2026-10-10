/**
 * Frozen types of the one browser gate pipeline (D135): the recorder, the static live Playground
 * and the static Studio's test bench all run a check through `runGate` (lib/gate/runner.ts) with
 * this input. Owners implement the bodies (WEB-LIVE: runner, route, playgroundResult); the shapes
 * change only through the lead.
 */
import type {
  DecideInput,
  DetectorResult,
  EvidenceSettings,
  RouteEvaluatorRef as PolicyRouteEvaluatorRef,
} from '@/lib/contracts/types.gen';
import type { Compiled, DecisionEvidenceItem, OnNoEvidence } from '@/lib/decision/compile';
import type { DecisionCore } from '@/lib/decision/decide';
import type { ExtractFormat, Extraction } from '@/lib/decision/evidence';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import type { Spec } from '@/lib/decision/spec';
import type { JevCall, JevCallResult, UsageLedger } from '@/lib/openrouter/types';
import type { DetectorId, EvidenceSource, PolicyMode } from '@/types/api';

/** One evaluator entry of a policy.v1 route. */
export interface RouteEvaluatorRef {
  ref: string;
  stage?: 'active' | 'shadow';
  enabled?: boolean;
  params?: Record<string, string>;
  /** Route threshold overrides, as policy.v1 writes them (raw space when unset; D151). */
  thresholds?: PolicyRouteEvaluatorRef['thresholds'];
}

/** The parts of a policy.v1 document the browser reads (defaults, overrides and routes). */
export interface PolicyInput {
  version: number;
  defaults?: Record<string, unknown>;
  overrides?: Record<string, unknown>;
  routes?: ({ name: string } & Record<string, unknown>)[];
}

/** A route's effective settings: the route merged over `defaults` (policy.v1 schema defaults last). */
export interface ResolvedRoute {
  name: string;
  mode: PolicyMode;
  fail: 'open' | 'closed';
  requireCalibration: boolean;
  onNoEvidence: OnNoEvidence;
  evidence: EvidenceSettings & { sources: EvidenceSource[] };
  detectors: { flag_on: DetectorId[]; block_on: DetectorId[]; min_integer: number };
  evaluators: RouteEvaluatorRef[];
  deepAudit: { pass_sample_rate: number; uniform_sample_rate: number };
  clientRules: boolean;
}

/** decide()'s calibration input: evaluator ref → its table (empty: raw scores, D104). */
export type CalibrationMap = DecideInput['calibration'];

/** The full runGate input (D135). */
export interface RunGateInput {
  route: ResolvedRoute;
  /** Evaluator ref → parsed spec (BUILTIN_TEMPLATES, or the Studio draft). */
  specs: ReadonlyMap<string, Spec>;
  /** The client request body, in `apiFormat`. */
  request: unknown;
  /** The answer being checked (the upstream reply, a scenario's response, or the user's text). */
  response: string;
  apiFormat: ExtractFormat;
  /** The decision-model transport (OpenRouter from the browser, or scripted answers). */
  call: JevCall;
  /** The caps: reserved before any call, settled per request (D101). */
  ledger: UsageLedger;
  /** The per-call token cap; compile's budget is min(24000, maxTokens). */
  maxTokens: number;
  calibration?: CalibrationMap;
  /** Epoch ms, for latency; Date.now by default. */
  clock?: () => number;
  signal?: AbortSignal;
}

/** What one check produced in the browser; playgroundResult.ts turns it into a PlaygroundResult. */
export interface BrowserGateResult {
  route: ResolvedRoute;
  extraction: Extraction;
  /** The decision evidence items (deduplicated, labelled). */
  evidence: DecisionEvidenceItem[];
  detectors: DetectorResult[];
  compiled: Compiled;
  /** Asked question id → raw answer. */
  answers: Record<string, RawAnswerInput>;
  /** Request index → `ok` or an OpenRouterErrorKind. */
  request_status: Record<string, string>;
  /** Each answered request's result, by request index (absent for a failed one). */
  calls: (JevCallResult | null)[];
  decision: DecisionCore;
  /** Sum over the answered requests; cost null when no response reported one. */
  usage: { input_tokens: number; cost: number | null };
  /** The snapshot that answered (the first answered request's), null without a call. */
  snapshot: string | null;
  /** From the first reserve to the last answer. */
  latency_ms: number;
}
