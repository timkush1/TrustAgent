/**
 * Whether the replay's tour (ReplayGuide, D117) was hidden: a non-secret preference kept in
 * localStorage `ta.replay.guide`, so the tour opens on a first visit and stays hidden once the
 * reader hides it. Storage may be blocked (a private window, a strict browser): the tour then
 * shows, and hiding it lasts for this page view.
 */
import { create } from 'zustand';
import { useReplayStore } from '@/data/replay/replayStore';
import { useIsStatic } from './useReplay';

export const GUIDE_STORAGE_KEY = 'ta.replay.guide';
const HIDDEN = 'hidden';

function readHidden(): boolean {
  try {
    return window.localStorage.getItem(GUIDE_STORAGE_KEY) === HIDDEN;
  } catch {
    return false;
  }
}

interface GuideState {
  hidden: boolean;
  hide(): void;
  /** Reads the stored preference again (tests, another tab). */
  reload(): void;
}

export const useGuideState = create<GuideState>()((set) => ({
  hidden: readHidden(),
  hide() {
    try {
      window.localStorage.setItem(GUIDE_STORAGE_KEY, HIDDEN);
    } catch {
      // Not kept: hidden for this page view only.
    }
    set({ hidden: true });
  },
  reload() {
    set({ hidden: readHidden() });
  },
}));

/** Whether the tour shows: the static site, a featured answer to start from, and not hidden. */
export function useGuideOpen(): boolean {
  const isStatic = useIsStatic();
  const featured = useReplayStore((s) => s.featuredTraceId);
  const hidden = useGuideState((s) => s.hidden);
  return isStatic && featured !== null && !hidden;
}
