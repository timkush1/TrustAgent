/**
 * What the static Studio page knows about the tab's live mode (D101, D107, D108): whether this build
 * may call OpenRouter from the browser, whether a key is connected in this tab, the models, the caps
 * and the tab's call counter. Null in the full build, where the gateway does every call.
 *
 * The key comes from the live mode's key store (lib/openrouter useBrowserKey) and the counter from
 * its ledger (useUsage), the same ones the live Playground shows: drafts and checks count against one
 * "{n} of {max} calls". The page holds the key only to keep it out of anything it exports.
 */
import { useIsStatic } from '@/components/replay/useReplay';
import { playground } from '@/config/runtime';
import { CHAT_FROM_BROWSER, LIVE_CHECKS } from '@/domain/liveScenarios';
import { useBrowserKey } from '@/lib/openrouter/useBrowserKey';
import { browserUsage } from '@/lib/openrouter/usage';
import { useUsage } from '@/lib/openrouter/useUsage';

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
  /** Billable calls this tab made so far (checks and drafts, here and on the Playground). */
  used: number;
  /** Calls reserved and not answered yet (they count against the cap). */
  inflight: number;
  /** OpenRouter refused too many calls in a row; the next action the viewer asks for resumes (Retry). */
  paused: boolean;
  /** Ends the pause: called when the viewer asks for a call again. */
  resume: () => void;
}

export function useStudioLive(): StudioLiveView | null {
  const isStatic = useIsStatic();
  const browserKey = useBrowserKey();
  const usage = useUsage();
  if (!isStatic) return null;
  return {
    checks: LIVE_CHECKS,
    drafting: CHAT_FROM_BROWSER,
    connected: browserKey.state !== 'none' && browserKey.key !== null,
    key: browserKey.key,
    judgeModel: playground.judgeModel,
    jevModel: playground.jevModel,
    maxCalls: usage.maxCalls,
    maxTokens: usage.maxTokens,
    used: usage.calls,
    inflight: usage.inflight,
    paused: usage.paused,
    resume: resumeUsage,
  };
}

function resumeUsage(): void {
  const store = browserUsage();
  if (store.getSnapshot().paused) store.resume();
}

/** Calls left in this tab (the ones in flight already count). */
export function callsLeft(live: Pick<StudioLiveView, 'maxCalls' | 'used' | 'inflight'>): number {
  return Math.max(0, live.maxCalls - live.used - live.inflight);
}
