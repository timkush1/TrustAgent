/**
 * The Traffic list's Live toggle (SPEC §3.3). Live is the default; `?live=0` is paused and
 * `?live=1` asks for live explicitly. The URL is the source of truth and streamStore follows it:
 * pausing freezes the visible order while rows already on screen keep updating.
 *
 * Pausing happens only on scroll-away, Space, or the button. Resuming (the button or the
 * "Paused · N new · Resume" pill, or Space) returns to the top of the list.
 */
import { useNavigate } from '@tanstack/react-router';
import { useCallback, useEffect } from 'react';
import { useStreamStore } from '@/data/stream/streamStore';

/** Scrolling further than this from the top of the page pauses the list. */
export const SCROLL_AWAY_PX = 160;

/** Space on one of these belongs to the element, not to the Live toggle. */
const OWNS_SPACE = [
  'a[href]',
  'button',
  'input',
  'select',
  'textarea',
  'summary',
  '[contenteditable]:not([contenteditable="false"])',
  '[role="button"]',
  '[role="link"]',
  '[role="checkbox"]',
  '[role="switch"]',
  '[role="option"]',
  '[role="menuitem"]',
  '[role="tab"]',
  '[role="dialog"]',
].join(',');

export interface LiveMode {
  live: boolean;
  setLive(live: boolean): void;
}

export function useLiveMode(liveParam: 0 | 1 | undefined): LiveMode {
  const navigate = useNavigate({ from: '/app/traffic/' });
  const paused = useStreamStore((s) => s.paused);
  const wantPaused = liveParam === 0;

  // Entering the page, or back/forward between ?live values.
  useEffect(() => {
    const store = useStreamStore.getState();
    if (wantPaused && !store.paused) store.pause();
    else if (!wantPaused && store.paused) store.resume();
  }, [wantPaused]);

  const setLive = useCallback(
    (live: boolean) => {
      const store = useStreamStore.getState();
      if (live === !store.paused) return;
      if (live) store.resume();
      else store.pause();
      void navigate({
        search: (prev) => ({ ...prev, live: live ? undefined : 0 }),
        replace: true,
        resetScroll: false,
      });
      if (live) window.scrollTo({ top: 0 });
    },
    [navigate],
  );

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== ' ' || e.repeat || e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.target instanceof Element && e.target.closest(OWNS_SPACE)) return;
      e.preventDefault();
      setLive(useStreamStore.getState().paused);
    };
    const onScroll = () => {
      if (window.scrollY > SCROLL_AWAY_PX && !useStreamStore.getState().paused) setLive(false);
    };
    window.addEventListener('keydown', onKey);
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', onScroll);
    };
  }, [setLive]);

  return { live: !paused, setLive };
}
