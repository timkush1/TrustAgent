/**
 * One live check in the static Playground (D99, D101, D104): what to check (a D99 scenario's pack
 * request and response, never its scripted answers, or the visitor's own documents, question and
 * answer on `support-bot`), the estimate shown on Run, and the run itself through runGate with
 * OpenRouter from the browser, the tab's caps and raw scores (`{}`: a live check is uncalibrated).
 * A live result is never compared with a scenario's `expect` (D104). Owned by WEB-LIVE (LV-6).
 */
import grounded from 'virtual:trustagent/scenario/grounded-pass';
import pii from 'virtual:trustagent/scenario/pii-leak-flag';
import refund from 'virtual:trustagent/scenario/refund-contradiction-flag';
import type { LiveScenarioId } from '@/domain/liveScenarios';
import type { DecisionRequestView, PlaygroundResult } from '@/types/api';
import { errorKind } from '@/lib/openrouter/errors';
import { priceFromTable } from '@/lib/openrouter/usage';
import type { JevCall, UsageLedger } from '@/lib/openrouter/types';
import { builtinSpecs, LIVE_POLICY, liveBlockMessage, liveRoute } from './livePolicy';
import { decisionRequestView, playgroundResult } from './playgroundResult';
import { GateRefusedError, prepareCheck, recordFailures, runGate } from './runner';
import type { BrowserGateResult } from './types';

/** The route the custom form runs on (D104). */
export const CUSTOM_ROUTE = 'support-bot';
/** The custom form's limits, in characters (the per-call token cap still applies). */
export const CUSTOM_LIMITS = { documents: 6_000, question: 1_000, answer: 2_000 } as const;

/** A chat request and the answer to check on one route. */
export interface LiveInput {
  route: string;
  request: { model: string; messages: readonly unknown[] };
  response: string;
}

/** The visitor's own text. */
export interface CustomText {
  documents: string;
  question: string;
  answer: string;
}

export interface LiveScenario {
  id: LiveScenarioId;
  title: string;
  route: string;
  /** The user's last question. */
  question: string;
  response: string;
  input: LiveInput;
}

type Message = { role: string; content: unknown };
const PACK = { 'grounded-pass': grounded, 'refund-contradiction-flag': refund, 'pii-leak-flag': pii } as const;

/** A D99 scenario as the live panel offers it: only its pack request and response. */
export function liveScenario(id: LiveScenarioId): LiveScenario {
  const s = PACK[id];
  const messages = s.request.messages as readonly Message[];
  const question = [...messages].reverse().find((m) => m.role === 'user' && typeof m.content === 'string')?.content as string | undefined;
  return {
    id,
    title: s.title,
    route: s.route,
    question: question ?? '',
    response: s.response,
    input: { route: s.route, request: { model: s.request.model, messages }, response: s.response },
  };
}

const SYSTEM_PROMPT = 'You are a support assistant. Answer only from the documents below. If they do not cover the question, say so.';

/** The visitor's text as a support-bot request: the documents in the system prompt, the question as the user. */
export function customInput(text: CustomText): LiveInput {
  const documents = text.documents.trim();
  return {
    route: CUSTOM_ROUTE,
    request: {
      model: 'your-model',
      messages: [
        { role: 'system', content: `${SYSTEM_PROMPT}\n\n<document id="your-document">${documents}</document>` },
        { role: 'user', content: text.question.trim() },
      ],
    },
    response: text.answer.trim(),
  };
}

/** What is missing from the custom form, in the order the fields appear (empty: ready). */
export function customProblems(text: CustomText): ('documents' | 'answer' | 'documents_long' | 'question_long' | 'answer_long')[] {
  const out: ReturnType<typeof customProblems> = [];
  if (text.documents.trim() === '') out.push('documents');
  else if (text.documents.length > CUSTOM_LIMITS.documents) out.push('documents_long');
  if (text.question.length > CUSTOM_LIMITS.question) out.push('question_long');
  if (text.answer.trim() === '') out.push('answer');
  else if (text.answer.length > CUSTOM_LIMITS.answer) out.push('answer_long');
  return out;
}

/** What Run will cost before it is pressed. */
export type LiveEstimate =
  | {
      ok: true;
      /** compile()'s estimate per request. */
      est_tokens: number[];
      /** The Jev price of those tokens (input only; output is free), null when the table has no price. */
      cost_usd: number | null;
    }
  | { ok: false; reason: 'token_cap' };

/** Estimates a check without any call: compile's token estimate priced from the table (D150: whole tokens, rounded up). */
export function estimateCheck(input: LiveInput, model: string, maxTokens: number): LiveEstimate {
  let est: number[];
  try {
    est = prepareCheck({ route: liveRoute(input.route), specs: builtinSpecs(), request: input.request, response: input.response, apiFormat: 'openai_chat', maxTokens }, model).compiled.est_tokens;
  } catch (err) {
    if (err instanceof GateRefusedError) return { ok: false, reason: 'token_cap' };
    throw err;
  }
  // The ledger refuses a request estimated over the per-call cap before any fetch (D101).
  if (est.some((t) => t > maxTokens)) return { ok: false, reason: 'token_cap' };
  let cost: number | null = 0;
  for (const tokens of est) {
    const c = priceFromTable(model, { input_tokens: Math.ceil(tokens), output_tokens: 0, cost: null });
    cost = c === null || cost === null ? null : cost + c;
  }
  return { ok: true, est_tokens: est, cost_usd: cost };
}

export interface LiveCheckDeps {
  call: JevCall;
  ledger: UsageLedger;
  maxTokens: number;
  signal?: AbortSignal;
}

export interface LiveCheckResult {
  gate: BrowserGateResult;
  result: PlaygroundResult;
  /** "Show exact Jev request": the bodies sent. */
  request: DecisionRequestView;
  /** What this check cost: OpenRouter's figure (vendor), else priced here (estimate); null when neither. */
  cost: { usd: number; label: 'vendor' | 'estimate' } | null;
}

/**
 * The failure that explains a check's failed requests (by request index): a refused key is
 * forgotten at once, which cancels the check's other requests, so a cancel is reported only when
 * nothing else failed. Undefined when no request failed.
 */
export function explainingFailure(failures: ReadonlyMap<number, unknown>): unknown {
  const failed = [...failures.entries()].sort(([a], [b]) => a - b).map(([, err]) => err);
  return failed.find((err) => errorKind(err) !== 'aborted') ?? failed[0];
}

/**
 * Runs one check. Rejects with GateRefusedError before any call when the caps refuse, and with the
 * first failed request's ApiError when any request failed (402, 401, a timeout, a cancel): the
 * gateway would apply the route's fail policy, but a live check either has Jev's answers or says
 * why not, rather than showing a fail-open pass that looks like a result.
 */
export async function runLiveCheck(input: LiveInput, deps: LiveCheckDeps): Promise<LiveCheckResult> {
  const watched = recordFailures(deps.call);
  const gate = await runGate({
    route: liveRoute(input.route),
    specs: builtinSpecs(),
    request: input.request,
    response: input.response,
    apiFormat: 'openai_chat',
    call: watched.call,
    ledger: deps.ledger,
    maxTokens: deps.maxTokens,
    calibration: {},
    signal: deps.signal,
  });
  const failure = explainingFailure(watched.failures);
  if (failure !== undefined) throw failure;
  let cost: LiveCheckResult['cost'] = null;
  let computed: number | null = null;
  if (gate.usage.cost !== null) cost = { usd: gate.usage.cost, label: 'vendor' };
  else {
    const answered = gate.calls.filter((c) => c !== null);
    let sum: number | null = answered.length > 0 ? 0 : null;
    for (const c of answered) {
      const priced = priceFromTable(deps.call.model, c.usage);
      sum = priced === null || sum === null ? null : sum + priced;
    }
    if (sum !== null) {
      computed = sum;
      cost = { usd: sum, label: 'estimate' };
    }
  }
  const result = playgroundResult(gate, deps.call, input.response, {
    policyVersion: LIVE_POLICY.version,
    blockMessage: liveBlockMessage(input.route),
    computedCostUsd: computed,
  });
  return { gate, result, request: decisionRequestView(gate.compiled), cost };
}
