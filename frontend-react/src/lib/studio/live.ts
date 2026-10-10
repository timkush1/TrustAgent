/**
 * What the static Studio needs from live mode (D107): the tab's OpenRouter transports and caps.
 * The key store, the ledger and the Jev and chat clients belong to the live mode (lib/openrouter); the
 * Studio plane (lib/studio/staticPlane.ts) reads them only through this seam, at the moment an
 * action runs, so a key connected after the page opened is used and a forgotten one is not.
 *
 * - `jev()`: Jev on OpenRouter's decisions endpoint with the tab's key (openRouterJevCall), when
 *   this build tests checks live (LIVE_CHECKS) and the tab has a key. runGate reserves and settles
 *   each of its requests on the ledger.
 * - `chat()`: one chat completion with the tab's key (chatComplete), when this build drafts from the
 *   browser (CHAT_FROM_BROWSER) and the tab has a key. The drafter reserves and settles every turn
 *   itself (D101, D108), so the transport is given a ledger that counts nothing: one turn is one
 *   call on the counter, never two.
 * - `ledger(model)`: the tab's usage ledger (D101), shared with the live Playground, pricing a call
 *   that reports no cost with `model`'s table price (an estimate).
 *
 * Tests pass their own StudioLive (lib/studio/fakeLive.ts) to createStaticStudioPlane, or real
 * clients over a fake fetch through the options here.
 */
import { playground } from '@/config/runtime';
import { CHAT_FROM_BROWSER, LIVE_CHECKS } from '@/domain/liveScenarios';
import { chatComplete } from '@/lib/openrouter/chat';
import { openRouterJevCall } from '@/lib/openrouter/decisions';
import type { HttpDeps } from '@/lib/openrouter/http';
import { browserKeyStore, type KeyStore } from '@/lib/openrouter/keyStore';
import type { JevCall, UsageLedger } from '@/lib/openrouter/types';
import { browserUsage, type UsageStore } from '@/lib/openrouter/usage';
import type { ChatSend } from './drafter';

export interface StudioLive {
  /** The decision model through the tab's key; null without a key, or when live checks are off. */
  jev(): JevCall | null;
  /** Chat completions through the tab's key (Draft with AI); null without a key, or when chat from the browser is off. */
  chat(): ChatSend | null;
  /** The tab's caps, shared with the live Playground (D101); a call without a reported cost is priced for `model`. */
  ledger(model: string): UsageLedger;
  /** The per-call token cap (VITE_PLAYGROUND_MAX_TOKENS). */
  readonly maxTokens: number;
  /** The decision model the checks name (VITE_JEV_MODEL). */
  readonly jevModel: string;
  /** The judge that drafts (VITE_OPENROUTER_JUDGE_MODEL). */
  readonly judgeModel: string;
  /** Whether this build can test checks live at all (scope L, D100). */
  readonly checks: boolean;
  /** Whether this build can draft from the browser (D108). */
  readonly drafting: boolean;
}

export interface StudioLiveOptions {
  /** The tab's key store (default: browserKeyStore()). */
  store?: KeyStore;
  /** The tab's ledger (default: browserUsage()). */
  usage?: UsageStore;
  /** The fetch the clients use (tests). */
  http?: HttpDeps;
  /** Overrides ERRORS_READABLE (tests). */
  errorsReadable?: boolean;
  /** Override LIVE_CHECKS and CHAT_FROM_BROWSER (tests). */
  checks?: boolean;
  drafting?: boolean;
}

/** The drafter counts each turn on the tab's ledger itself, so its transport counts nothing. */
const COUNTED_BY_DRAFTER: UsageLedger = { reserve: () => ({ ok: true }), settle: () => {} };

/** The live mode of this build: the tab's key, the tab's ledger and the OpenRouter clients. */
export function studioLive(opts: StudioLiveOptions = {}): StudioLive {
  const store = () => opts.store ?? browserKeyStore();
  const usage = () => opts.usage ?? browserUsage();
  const checks = opts.checks ?? LIVE_CHECKS;
  const drafting = opts.drafting ?? CHAT_FROM_BROWSER;
  const transport = { http: opts.http, errorsReadable: opts.errorsReadable };
  return {
    jev: () => {
      const s = store();
      return checks && s.currentKey() !== null ? openRouterJevCall({ model: playground.jevModel, store: s, ...transport }) : null;
    },
    chat: () => {
      const s = store();
      if (!drafting || s.currentKey() === null) return null;
      return (call, signal) => chatComplete(call, { ledger: COUNTED_BY_DRAFTER, store: s, signal, ...transport });
    },
    ledger: (model) => usage().forModel(model),
    maxTokens: playground.maxTokens,
    jevModel: playground.jevModel,
    judgeModel: playground.judgeModel,
    checks,
    drafting,
  };
}
