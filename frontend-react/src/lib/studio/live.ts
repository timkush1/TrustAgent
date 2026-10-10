/**
 * What the static Studio needs from live mode (D107): the tab's OpenRouter transports and caps.
 * The key store, the ledger and the Jev and chat clients belong to WEB-LIVE (lib/openrouter); the
 * Studio plane (data/replay/replayStudioPlane.ts) reads them only through this seam, at the moment
 * an action runs, so a key connected after the page opened is used and a forgotten one is not.
 *
 * Until the live transport lands (CP-LIVE) there is no client: `jev()` and `chat()` answer null,
 * and the page says a key is needed instead of calling anything. Tests pass their own StudioLive
 * (fake clients and a fake ledger) to createReplayStudioPlane.
 */
import { playground } from '@/config/runtime';
import { CHAT_FROM_BROWSER, LIVE_CHECKS } from '@/domain/liveScenarios';
import { NOOP_LEDGER } from '@/lib/gate/runner';
import type { JevCall, UsageLedger } from '@/lib/openrouter/types';
import type { ChatSend } from './drafter';

export interface StudioLive {
  /** The decision model through the tab's key; null without a key, or when live checks are off. */
  jev(): JevCall | null;
  /** Chat completions through the tab's key (Draft with AI); null without a key, or when chat from the browser is off. */
  chat(): ChatSend | null;
  /** The tab's caps, shared with the live Playground (D101). */
  ledger(): UsageLedger;
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

/** The live mode of this build: no client until the tab's transport lands (CP-LIVE). */
export function studioLive(): StudioLive {
  return {
    jev: () => null,
    chat: () => null,
    ledger: () => NOOP_LEDGER,
    maxTokens: playground.maxTokens,
    jevModel: playground.jevModel,
    judgeModel: playground.judgeModel,
    checks: LIVE_CHECKS,
    drafting: CHAT_FROM_BROWSER,
  };
}
