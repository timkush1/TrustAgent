/**
 * What the static Studio page knows about the tab's live mode (D101, D107, D108): whether this build
 * may call OpenRouter from the browser, whether a key is connected in this tab, the models and the
 * caps. Null in the full build, where the gateway does every call.
 *
 * The key comes from WEB-LIVE's key store (useBrowserKey); the page holds it only to keep it out of
 * anything it exports. `used` is the tab's count of billable calls once the ledger exposes it
 * (CP-LIVE); null hides the counter rather than showing a made-up number.
 */
import { useIsStatic } from '@/components/replay/useReplay';
import { playground } from '@/config/runtime';
import { CHAT_FROM_BROWSER, LIVE_CHECKS } from '@/domain/liveScenarios';
import { useBrowserKey } from '@/lib/openrouter/useBrowserKey';

export interface StudioLiveView {
  /** This build tests checks live from the browser (scope L). */
  checks: boolean;
  /** This build drafts with AI from the browser. */
  drafting: boolean;
  /** A key is connected in this tab. */
  connected: boolean;
  /** The tab's key, to keep out of exports (never shown). */
  key: string | null;
  judgeModel: string;
  jevModel: string;
  maxCalls: number;
  maxTokens: number;
  /** Billable calls this tab made so far; null while unknown. */
  used: number | null;
}

export function useStudioLive(): StudioLiveView | null {
  const isStatic = useIsStatic();
  const browserKey = useBrowserKey();
  if (!isStatic) return null;
  return {
    checks: LIVE_CHECKS,
    drafting: CHAT_FROM_BROWSER,
    connected: browserKey.state !== 'none' && browserKey.key !== null,
    key: browserKey.key,
    judgeModel: playground.judgeModel,
    jevModel: playground.jevModel,
    maxCalls: playground.maxCalls,
    maxTokens: playground.maxTokens,
    used: null,
  };
}

/** Calls left in this tab, or null while the count is unknown. */
export function callsLeft(live: StudioLiveView): number | null {
  return live.used === null ? null : Math.max(0, live.maxCalls - live.used);
}
