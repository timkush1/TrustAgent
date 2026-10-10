/**
 * Jev from the browser (D100): the JevCall runGate uses in the static live Playground and the
 * static Studio's test bench. It ports backend-go internal/jev/wire.go:
 *
 * - `encodeDecisionRequest` (encodeRequest): `{model, state, questions}` and nothing else; the
 *   browser sends no session_id and no provider preferences (D100);
 * - `decodeDecisionResponse` (decodeResponse): a 2xx must be a decision object with an `answers`
 *   object (a 200 error envelope says why it is not); negative input_tokens is bad_response; a
 *   cost that is not a non-negative number is dropped;
 * - `checkAnswers`: keeps the answers to asked questions and reports the anomalies (missing,
 *   unexpected, type mismatch, probabilities off 1 ± 0.02). Answers are not renormalized here:
 *   decide()'s normalize owns that.
 *
 * One call is one POST on DECISIONS_PATH (chosen at build time, never a runtime fallback). It does
 * not reserve or settle: runGate reserves every request of a check before the first call and
 * settles each one. A refused key is forgotten at once (D100). Owned by WEB-LIVE (LV-3).
 */
import { ApiError } from '@/data/errors';
import { playground } from '@/config/runtime';
import type { CompiledRequest } from '@/lib/decision/compile';
import { EPS } from '@/lib/decision/floats';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import { DECISIONS_BACKEND, DECISIONS_URL, TIMEOUTS_MS } from './endpoints';
import { checkContentType, classifyStatus, diagnoseOpaqueFailure, forgetsKey, openRouterError, parseEnvelope, transportError, usageOf } from './errors';
import { openRouterFetch, type HttpDeps } from './http';
import { getKeyInfo } from './keyInfo';
import { browserKeyStore, type KeyStore } from './keyStore';
import type { CallUsage, JevCall, JevCallResult } from './types';

// ------------------------------------------------------------------------------------ wire

/** The decision request body (wire.go wireRequest, without the OpenRouter-only fields). */
export function encodeDecisionRequest(body: CompiledRequest): string {
  return JSON.stringify({ model: body.model, state: body.state ?? {}, questions: body.questions });
}

/** A decoded decision response (wire.go wireResponse). */
export interface DecisionResponse {
  id: string;
  model: string;
  provider: string;
  answers: Record<string, RawAnswerInput | null>;
  usage: CallUsage | null;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const optString = (v: unknown) => v === undefined || v === null || typeof v === 'string';
const optNumber = (v: unknown) => v === undefined || v === null || typeof v === 'number';
const optMap = (v: unknown, value: (x: unknown) => boolean) => v === undefined || v === null || (isObj(v) && Object.values(v).every(value));

/** Whether a raw answer decodes into wire.go's RawAnswer (a wrong field type fails the whole body, as in Go). */
function isRawAnswer(v: unknown): v is RawAnswerInput | null {
  if (v === null) return true;
  if (!isObj(v)) return false;
  return (
    optString(v.type) &&
    optNumber(v.noul) &&
    optString(v.choice) &&
    optNumber(v.score) &&
    optNumber(v.confidence) &&
    optMap(v.probabilities, (p) => p === null || typeof p === 'number') &&
    optMap(v.legend, () => true)
  );
}

/** The RawAnswer fields only (unknown members are dropped, as Go's decoder does). */
function rawAnswer(v: Obj): RawAnswerInput {
  const out: Record<string, unknown> = {};
  for (const k of ['type', 'noul', 'choice', 'score', 'confidence', 'probabilities', 'legend']) {
    if (v[k] !== undefined) out[k] = v[k];
  }
  return out as RawAnswerInput;
}

/** Parses a 2xx body (wire.go decodeResponse); rejects with bad_response. */
export function decodeDecisionResponse(text: string, status = 200): DecisionResponse {
  const bad = (message: string, cause?: unknown) => openRouterError('bad_response', { status, message, cause });
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    throw bad('the response is not a decision JSON object', err);
  }
  if (!isObj(raw)) throw bad('the response is not a decision JSON object');
  if (!optString(raw.id) || !optString(raw.model) || !optString(raw.provider)) throw bad('the response is not a decision JSON object');
  if (!isObj(raw.answers)) {
    // Some gateways answer 200 with an error envelope; its message says what went wrong.
    const env = parseEnvelope(text);
    throw bad(`the response has no answers object${env.message ? `: ${env.message}` : ''}`);
  }
  const answers: Record<string, RawAnswerInput | null> = {};
  for (const [qid, a] of Object.entries(raw.answers)) {
    if (!isRawAnswer(a)) throw bad('the response is not a decision JSON object');
    answers[qid] = a === null ? null : rawAnswer(a);
  }
  let usage: CallUsage | null = null;
  if (raw.usage !== undefined && raw.usage !== null) {
    if (!isObj(raw.usage)) throw bad('the response is not a decision JSON object');
    const t = raw.usage.input_tokens ?? raw.usage.prompt_tokens;
    if (typeof t === 'number' && t < 0) throw bad(`negative input_tokens ${t}`);
    usage = usageOf(raw.usage) ?? null;
  }
  return { id: (raw.id as string | null) ?? '', model: (raw.model as string | null) ?? '', provider: (raw.provider as string | null) ?? '', answers, usage };
}

/** Anomalies the client detects (wire.go; trustagent_gateway_jev_anomalies_total kinds). */
export type AnswerAnomaly = 'missing_answer' | 'unexpected_answer' | 'type_mismatch' | 'probability_sum';

/** Keeps the answers to asked questions and reports anomalies (wire.go checkAnswers). */
export function checkAnswers(
  questions: Readonly<Record<string, unknown>>,
  answers: Readonly<Record<string, RawAnswerInput | null>>,
): { answers: Record<string, RawAnswerInput>; anomalies: AnswerAnomaly[] } {
  const anomalies: AnswerAnomaly[] = [];
  const out: Record<string, RawAnswerInput> = {};
  for (const [qid, a] of Object.entries(answers)) {
    if (!Object.hasOwn(questions, qid)) {
      anomalies.push('unexpected_answer');
      continue;
    }
    if (a === null) continue;
    const q = questions[qid];
    const qt = isObj(q) && typeof q.type === 'string' ? q.type : '';
    // The wire may carry any string (an empty type means "not given", as in Go).
    const at: unknown = a.type;
    if (qt !== '' && typeof at === 'string' && at !== '' && at !== qt) anomalies.push('type_mismatch');
    if (a.probabilities && Object.keys(a.probabilities).length > 0) {
      const sum = Object.values(a.probabilities).reduce<number>((s, p) => s + (p ?? 0), 0);
      if (Math.abs(sum - 1) > 0.02 + EPS) anomalies.push('probability_sum');
    }
    out[qid] = a;
  }
  for (const qid of Object.keys(questions)) {
    if (!Object.hasOwn(out, qid)) anomalies.push('missing_answer');
  }
  return { answers: out, anomalies };
}

// ------------------------------------------------------------------------------------ the call

/**
 * No key in this tab: the call is never sent. The panel shows its own Connect and Paste state, so
 * liveErrorView says nothing about it.
 */
export function noKeyError(): ApiError {
  return openRouterError('auth', { status: 0, message: 'no OpenRouter key in this tab', details: { no_key: true } });
}

/** An ApiError for anything a call threw (a bug in parsing is a bad response, as Go's ErrorKind says). */
export function asOpenRouterError(err: unknown, status = 0): ApiError {
  if (err instanceof ApiError) return err;
  if (err instanceof DOMException && err.name === 'AbortError') return transportError('aborted', status, err);
  if (err instanceof TypeError) return transportError('network', status, err);
  return openRouterError('bad_response', { status, message: err instanceof Error ? err.message : 'unexpected failure', cause: err });
}

/**
 * Runs a failure through the D100 rules: an unreadable one gets the free key check
 * (ERRORS_READABLE=false), and a refused key is forgotten.
 */
export async function settleFailure(err: unknown, key: string, store: KeyStore, opts: { http?: HttpDeps; errorsReadable?: boolean }): Promise<ApiError> {
  const e = await diagnoseOpaqueFailure(asOpenRouterError(err), () => getKeyInfo(key, { track: store.track, http: opts.http }), opts.errorsReadable);
  if (forgetsKey(e) && store.currentKey() === key) store.forget();
  return e;
}

export interface DecisionsCallOptions {
  /** The decision model; VITE_JEV_MODEL by default. */
  model?: string;
  store?: KeyStore;
  http?: HttpDeps;
  /** Overrides ERRORS_READABLE (tests). */
  errorsReadable?: boolean;
}

/** The JevCall that asks Jev on OpenRouter with the tab's key. */
export function openRouterJevCall(opts: DecisionsCallOptions = {}): JevCall {
  const model = opts.model ?? playground.jevModel;
  const store = opts.store ?? browserKeyStore();
  return {
    backend: DECISIONS_BACKEND,
    model,
    async call(request, signal): Promise<JevCallResult> {
      const key = store.currentKey();
      if (key === null) throw noKeyError();
      try {
        const res = await openRouterFetch(
          { url: DECISIONS_URL, method: 'POST', body: encodeDecisionRequest(request.body), key, timeoutMs: TIMEOUTS_MS.decisions, signal, track: store.track },
          opts.http,
        );
        if (res.status < 200 || res.status > 299) throw classifyStatus(res.status, res.headers, res.text, key);
        const notJson = checkContentType(res.status, res.headers, res.text);
        if (notJson) throw notJson;
        const w = decodeDecisionResponse(res.text, res.status);
        const { answers } = checkAnswers(request.body.questions, w.answers);
        // Without a usage block the input is counted at compile()'s estimate (the cost is then
        // priced here and labelled an estimate).
        const usage: CallUsage = w.usage ?? { input_tokens: request.est_tokens, cost: null };
        return { answers, usage, snapshot: w.model || request.body.model, latency_ms: res.latency_ms };
      } catch (err) {
        throw await settleFailure(err, key, store, opts);
      }
    },
  };
}
