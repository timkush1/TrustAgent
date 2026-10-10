/**
 * Chat completions from the browser (D100, D101, D108): Studio's Draft with AI asks the judge model
 * (VITE_OPENROUTER_JUDGE_MODEL) through the tab's key. One call is one
 * `POST /api/v1/chat/completions {model, messages, temperature, max_tokens, usage:{include:true}}`
 * (`usage.include` makes OpenRouter report the call's own cost).
 *
 * Every call reserves one slot of the tab's ledger before it is sent (the prompt estimate plus
 * `max_tokens` must fit the token cap) and settles it once, so drafts and checks share the
 * "{n} of {max} calls" counter. A refused key is forgotten at once. Owned by WEB-LIVE (LV-3).
 */
import { DEFAULT_CPT, est } from '@/lib/decision/tokens';
import { GateRefusedError } from '@/lib/gate/runner';
import { asOpenRouterError, noKeyError, settleFailure } from './decisions';
import { CHAT_URL, TIMEOUTS_MS } from './endpoints';
import { checkContentType, classifyStatus, errorDetails, openRouterError, parseEnvelope, usageOf } from './errors';
import { openRouterFetch, type HttpDeps } from './http';
import { browserKeyStore, type KeyStore } from './keyStore';
import type { CallUsage, ChatCall, ChatResult, UsageLedger } from './types';
import { browserUsage } from './usage';

export interface ChatOptions {
  /** The tab's ledger priced for the call's model by default. */
  ledger?: UsageLedger;
  store?: KeyStore;
  signal?: AbortSignal;
  http?: HttpDeps;
  /** Overrides ERRORS_READABLE (tests). */
  errorsReadable?: boolean;
}

/** The token estimate a chat call reserves: its messages plus the answer it may write (D101). */
export function chatTokenEstimate(call: ChatCall): number {
  return est(call.messages, DEFAULT_CPT) + call.max_tokens;
}

/** The request body (no provider preferences, no session id; D100). */
export function encodeChatRequest(call: ChatCall): string {
  return JSON.stringify({
    model: call.model,
    messages: call.messages,
    ...(call.temperature === undefined ? {} : { temperature: call.temperature }),
    max_tokens: call.max_tokens,
    usage: { include: true },
  });
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Parses a 2xx chat body; rejects with bad_response. */
export function decodeChatResponse(text: string, status: number, requested: string): Omit<ChatResult, 'latency_ms' | 'usage'> & { usage: CallUsage | undefined } {
  const bad = (message: string, cause?: unknown) => openRouterError('bad_response', { status, message, cause });
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (err) {
    throw bad('the response is not a chat completion JSON object', err);
  }
  if (!isObj(raw)) throw bad('the response is not a chat completion JSON object');
  const usage: CallUsage | undefined = isObj(raw.usage) ? usageOf(raw.usage) : undefined;
  const choice = Array.isArray(raw.choices) && isObj(raw.choices[0]) ? raw.choices[0] : null;
  const message = choice !== null && isObj(choice.message) ? choice.message : null;
  if (message === null || !(typeof message.content === 'string' || message.content === null)) {
    // OpenRouter may answer 200 with an error object (a provider failure after routing).
    const env = parseEnvelope(text);
    throw openRouterError('bad_response', { status, message: `the response has no message${env.message ? `: ${env.message}` : ''}`, details: { usage } });
  }
  return {
    text: typeof message.content === 'string' ? message.content : '',
    model: typeof raw.model === 'string' && raw.model !== '' ? raw.model : requested,
    finish_reason: typeof choice?.finish_reason === 'string' ? choice.finish_reason : null,
    usage,
  };
}

/** One chat completion with the tab's key; rejects with an OpenRouter ApiError or GateRefusedError. */
export async function chatComplete(call: ChatCall, opts: ChatOptions = {}): Promise<ChatResult> {
  const store = opts.store ?? browserKeyStore();
  const ledger = opts.ledger ?? browserUsage().forModel(call.model);
  const key = store.currentKey();
  if (key === null) throw noKeyError();
  const reserved = ledger.reserve([chatTokenEstimate(call)]);
  if (!reserved.ok) throw new GateRefusedError(reserved.reason);

  let status = 0;
  try {
    const res = await openRouterFetch(
      { url: CHAT_URL, method: 'POST', body: encodeChatRequest(call), key, timeoutMs: TIMEOUTS_MS.chat, signal: opts.signal, track: store.track },
      opts.http,
    );
    status = res.status;
    if (res.status < 200 || res.status > 299) throw classifyStatus(res.status, res.headers, res.text, key);
    const notJson = checkContentType(res.status, res.headers, res.text);
    if (notJson) throw notJson;
    const result = decodeChatResponse(res.text, res.status, call.model);
    // Without a usage block the call is counted at its estimate (the reserved prompt and the
    // longest answer), priced here and labelled an estimate.
    const usage: CallUsage = result.usage ?? { input_tokens: est(call.messages, DEFAULT_CPT), output_tokens: call.max_tokens, cost: null };
    ledger.settle(status, usage);
    return { ...result, usage, latency_ms: res.latency_ms };
  } catch (err) {
    const e = asOpenRouterError(err, status);
    ledger.settle(e.status, errorDetails(e).usage);
    throw await settleFailure(e, key, store, opts);
  }
}
