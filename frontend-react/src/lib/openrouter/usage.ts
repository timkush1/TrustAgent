/**
 * The tab's caps on calls to OpenRouter (D101): checks and AI drafts share one ledger.
 *
 * - **Billable** = a 2xx response, or any response carrying `usage`. Only billable calls consume
 *   the cap (`maxCalls`, VITE_PLAYGROUND_MAX_CALLS).
 * - **Rejected** = any other 4xx or 5xx except 402. They do not consume the cap; after
 *   {@link REJECTED_PAUSE} in a row the ledger refuses with `rejected_pause` until `resume()`.
 * - Never counted: 402, no response (network, CORS, timeout, abort), a refusal before any fetch,
 *   the key exchange and the free key check (those never reserve).
 *
 * `reserve` runs before every billable fetch with one estimate per request and takes every slot or
 * none; `settle` runs once per reserved slot and releases a slot that did not bill. The state lives
 * in sessionStorage `ta.playground.usage` `{calls, rejected, input_tokens, cost_usd, cost_source}`,
 * per tab: it survives a reload and Forget key. `rejected` is the current run of rejected responses
 * in a row. The caps are a courtesy, not a security boundary: the key's own credit limit is the
 * real one (SECURITY.md). Owned by WEB-LIVE (LV-1).
 */
import prices from '@contracts/pricing/prices.v1.json';
import { playground } from '@/config/runtime';
import { priceCall } from '@/lib/decision/cost';
import type { PricingTable } from '@/types/api';
import type { CallUsage, LedgerRefusal, ReserveResult, UsageLedger } from './types';

export const USAGE_NAME = 'ta.playground.usage';
/** Rejected responses in a row before the ledger pauses (D101). */
export const REJECTED_PAUSE = 10;

/**
 * Where the running cost comes from: every billable call reported its own cost (`vendor`), at least
 * one was priced here from its tokens (`estimate`), or at least one could be neither (`unknown`:
 * `cost_usd` then counts only the calls that had a figure). Null before the first billable call.
 */
export type UsageCostSource = 'vendor' | 'estimate' | 'unknown';

/** The stored state (D101). */
export interface UsageState {
  calls: number;
  rejected: number;
  input_tokens: number;
  cost_usd: number;
  cost_source: UsageCostSource | null;
}

export interface UsageSnapshot extends UsageState {
  maxCalls: number;
  maxTokens: number;
  /** Slots reserved and not settled yet. */
  inflight: number;
  /** True after {@link REJECTED_PAUSE} rejected responses in a row. */
  paused: boolean;
}

/** Prices one billable call that reported no cost; null when it cannot be priced. */
export type UsagePricer = (model: string, usage: CallUsage) => number | null;

export interface UsageStoreOptions {
  maxCalls?: number;
  maxTokens?: number;
  storage?: () => Storage;
  price?: UsagePricer;
  /** Where to listen for pageshow (a bfcache restore re-reads the state); null listens nowhere. */
  target?: Pick<Window, 'addEventListener' | 'removeEventListener'> | null;
}

/** The ledger: the frozen UsageLedger, plus a store for components and per-model views. */
export interface UsageStore extends UsageLedger {
  /** settle() with the model the call used, so a call without a vendor cost can be priced. */
  settle(status: number, usage?: CallUsage, model?: string): void;
  /** A UsageLedger whose settle prices with `model` (runGate's ledger for the Jev model). */
  forModel(model: string): UsageLedger;
  /** Ends a rejected pause (the panel's Retry). */
  resume(): void;
  subscribe(listener: () => void): () => void;
  getSnapshot(): UsageSnapshot;
  dispose(): void;
}

const ZERO: UsageState = { calls: 0, rejected: 0, input_tokens: 0, cost_usd: 0, cost_source: null };
const COST_SOURCES: readonly UsageCostSource[] = ['vendor', 'estimate', 'unknown'];

const count = (v: unknown): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
const money = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v) && v >= 0;

function parseState(raw: string | null): UsageState {
  if (raw === null) return ZERO;
  try {
    const v = JSON.parse(raw) as Record<string, unknown>;
    if (typeof v !== 'object' || v === null) return ZERO;
    const source = v.cost_source === null || COST_SOURCES.includes(v.cost_source as UsageCostSource) ? (v.cost_source as UsageCostSource | null) : undefined;
    if (!count(v.calls) || !count(v.rejected) || !count(v.input_tokens) || !money(v.cost_usd) || source === undefined) return ZERO;
    return { calls: v.calls, rejected: v.rejected, input_tokens: v.input_tokens, cost_usd: v.cost_usd, cost_source: source };
  } catch {
    return ZERO;
  }
}

/** Whether a settled response is billable (D101): a 2xx, or any response that reported usage. */
export function isBillable(status: number, usage?: CallUsage): boolean {
  return (status >= 200 && status <= 299) || usage !== undefined;
}

/** Whether a settled response is rejected (D101): another 4xx or 5xx, 402 aside, without usage. */
export function isRejected(status: number, usage?: CallUsage): boolean {
  return usage === undefined && status >= 400 && status <= 599 && status !== 402;
}

const PRICES = prices as unknown as PricingTable;

/** Prices a call through the TS cost runner (CP-COSTTS); null when it cannot. */
export const priceFromTable: UsagePricer = (model, usage) => {
  try {
    const priced = priceCall({ provider: 'openrouter', model, input_tokens: usage.input_tokens, output_tokens: usage.output_tokens ?? 0, reported_cost_usd: null, table: PRICES });
    return priced.cost_usd;
  } catch {
    return null;
  }
};

function mergeSource(cur: UsageCostSource | null, next: UsageCostSource): UsageCostSource {
  if (cur === null) return next;
  const rank: Record<UsageCostSource, number> = { vendor: 0, estimate: 1, unknown: 2 };
  return rank[next] > rank[cur] ? next : cur;
}

export function createUsageStore(opts: UsageStoreOptions = {}): UsageStore {
  const maxCalls = opts.maxCalls ?? playground.maxCalls;
  const maxTokens = opts.maxTokens ?? playground.maxTokens;
  const price = opts.price ?? priceFromTable;
  const getStorage =
    opts.storage ??
    ((): Storage => {
      if (typeof window === 'undefined') throw new Error('no window');
      return window.sessionStorage;
    });
  const target = opts.target === undefined ? (typeof window === 'undefined' ? null : window) : opts.target;

  const listeners = new Set<() => void>();
  let memory: string | null = null;
  let blocked = false;
  let inflight = 0;

  const read = (): string | null => {
    if (!blocked) {
      try {
        return getStorage().getItem(USAGE_NAME);
      } catch {
        blocked = true;
      }
    }
    return memory;
  };
  const write = (value: string): void => {
    memory = value;
    if (blocked) return;
    try {
      getStorage().setItem(USAGE_NAME, value);
    } catch {
      blocked = true;
    }
  };

  let state: UsageState = parseState(read());
  let snapshot: UsageSnapshot = build();

  function build(): UsageSnapshot {
    return { ...state, maxCalls, maxTokens, inflight, paused: state.rejected >= REJECTED_PAUSE };
  }
  const publish = (): void => {
    snapshot = build();
    for (const l of [...listeners]) l();
  };
  const commit = (next: UsageState): void => {
    state = next;
    write(JSON.stringify(state));
    publish();
  };

  const onPageShow = (e: Event): void => {
    if (!(e as PageTransitionEvent).persisted) return;
    state = parseState(read());
    publish();
  };
  target?.addEventListener('pageshow', onPageShow);

  const reserve = (estTokens: readonly number[]): ReserveResult => {
    if (estTokens.length === 0) return { ok: true };
    let reason: LedgerRefusal | null = null;
    if (state.calls + inflight + estTokens.length > maxCalls) reason = 'cap_reached';
    else if (state.rejected >= REJECTED_PAUSE) reason = 'rejected_pause';
    else if (estTokens.some((t) => !(t <= maxTokens))) reason = 'token_cap';
    if (reason !== null) return { ok: false, reason };
    inflight += estTokens.length;
    publish();
    return { ok: true };
  };

  const settle = (status: number, usage?: CallUsage, model?: string): void => {
    inflight = Math.max(0, inflight - 1);
    if (isBillable(status, usage)) {
      let cost: number | null = usage?.cost ?? null;
      let source: UsageCostSource = 'vendor';
      if (cost === null || !money(cost)) {
        cost = usage !== undefined && model !== undefined ? price(model, usage) : null;
        source = cost !== null && money(cost) ? 'estimate' : 'unknown';
      }
      const tokens = usage !== undefined && count(usage.input_tokens) ? usage.input_tokens : 0;
      commit({
        calls: state.calls + 1,
        rejected: 0,
        input_tokens: state.input_tokens + tokens,
        cost_usd: state.cost_usd + (cost !== null && money(cost) ? cost : 0),
        cost_source: mergeSource(state.cost_source, source),
      });
      return;
    }
    if (isRejected(status, usage)) {
      commit({ ...state, rejected: state.rejected + 1 });
      return;
    }
    publish();
  };

  return {
    reserve,
    settle,
    forModel: (model) => ({ reserve, settle: (status, usage) => settle(status, usage, model) }),
    resume() {
      if (state.rejected === 0) return;
      commit({ ...state, rejected: 0 });
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => snapshot,
    dispose() {
      target?.removeEventListener('pageshow', onPageShow);
      listeners.clear();
    },
  };
}

/** The snapshot before any store exists (server render, prerender). */
export const SERVER_USAGE_SNAPSHOT: UsageSnapshot = Object.freeze({
  ...ZERO,
  maxCalls: playground.maxCalls,
  maxTokens: playground.maxTokens,
  inflight: 0,
  paused: false,
});

let shared: UsageStore | null = null;

/** This tab's ledger (created on first use). */
export function browserUsage(): UsageStore {
  shared ??= createUsageStore();
  return shared;
}
