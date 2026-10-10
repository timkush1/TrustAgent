/**
 * Test doubles of the tab's live mode for Studio's tests (never imported by app code): a ledger
 * that counts what it reserves and settles, a chat transport that answers from a script, a
 * decision-model client that answers each question from a function, and a StudioLive over them.
 */
import type { RawAnswerInput } from '@/lib/decision/normalize';
import type { CallUsage, ChatCall, ChatResult, JevCall, JevCallRequest, LedgerRefusal, ReserveResult, UsageLedger } from '@/lib/openrouter/types';
import type { ChatSend } from './drafter';
import type { StudioLive } from './live';

export interface FakeLedger extends UsageLedger {
  reserved: number[][];
  settled: { status: number; usage?: CallUsage }[];
  /** Reservations still allowed. */
  left: number;
}

/** A ledger that allows `slots` reservations (all of a multi-request reservation or none), then refuses with `reason`. */
export function fakeLedger(slots = Number.POSITIVE_INFINITY, reason: LedgerRefusal = 'cap_reached'): FakeLedger {
  const ledger: FakeLedger = {
    reserved: [],
    settled: [],
    left: slots,
    reserve(est): ReserveResult {
      if (ledger.left < est.length) return { ok: false, reason };
      ledger.left -= est.length;
      ledger.reserved.push([...est]);
      return { ok: true };
    },
    settle(status, usage) {
      ledger.settled.push(usage === undefined ? { status } : { status, usage });
    },
  };
  return ledger;
}

export type FakeChat = ChatSend & { calls: ChatCall[] };

/** A chat transport that answers turn n with `replies[n]` (an Error is thrown instead). */
export function fakeChat(replies: readonly (ChatResult | Error)[]): FakeChat {
  const calls: ChatCall[] = [];
  const send = async (call: ChatCall): Promise<ChatResult> => {
    calls.push(structuredClone(call));
    const next = replies[calls.length - 1];
    if (next === undefined) throw new Error('fakeChat: no reply scripted for this turn');
    if (next instanceof Error) throw next;
    return next;
  };
  return Object.assign(send, { calls });
}

/** One chat answer with usage (cost null: the vendor reported none). */
export function chatAnswer(text: string, extra: Partial<ChatResult> = {}): ChatResult {
  return { text, model: 'anthropic/claude-haiku-4.5', finish_reason: 'stop', usage: { input_tokens: 2400, output_tokens: 600, cost: 0.0054 }, latency_ms: 900, ...extra };
}

export type FakeJev = JevCall & { requests: JevCallRequest[] };

/**
 * A decision-model client that answers each question of a request with `answer(state, qid)`; an
 * Error it returns is thrown instead. Usage is the request's own estimate, with `cost` as given.
 */
export function fakeJev(
  answer: (state: Record<string, unknown>, qid: string) => RawAnswerInput | Error,
  opts: { cost?: (request: JevCallRequest) => number | null; model?: string; snapshot?: string } = {},
): FakeJev {
  const requests: JevCallRequest[] = [];
  return {
    backend: 'openrouter-decisions',
    model: opts.model ?? 'typesafe/jev-1.13',
    requests,
    async call(request, signal) {
      requests.push(structuredClone(request));
      signal?.throwIfAborted();
      const answers: Record<string, RawAnswerInput> = {};
      for (const qid of Object.keys(request.body.questions)) {
        const a = answer(request.body.state as Record<string, unknown>, qid);
        if (a instanceof Error) throw a;
        answers[qid] = a;
      }
      return {
        answers,
        usage: { input_tokens: request.est_tokens, cost: opts.cost ? opts.cost(request) : null },
        snapshot: opts.snapshot ?? 'jev-1.13-2026-09-01',
        latency_ms: 12,
      };
    },
  };
}

/** A StudioLive over fakes (both clients off unless given). */
export function fakeStudioLive(parts: { jev?: JevCall | null; chat?: ChatSend | null; ledger?: UsageLedger; maxTokens?: number } = {}): StudioLive {
  const ledger = parts.ledger ?? fakeLedger();
  return {
    jev: () => parts.jev ?? null,
    chat: () => parts.chat ?? null,
    ledger: () => ledger,
    maxTokens: parts.maxTokens ?? 8000,
    jevModel: 'typesafe/jev-1.13',
    judgeModel: 'anthropic/claude-haiku-4.5',
    checks: true,
    drafting: true,
  };
}
