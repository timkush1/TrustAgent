import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { INITIAL_REPLAY, useReplayStore } from '@/data/replay/replayStore';
import { useNow } from './useNow';

afterEach(() => {
  useReplayStore.getState().set(INITIAL_REPLAY);
  vi.useRealTimers();
});

describe('useNow', () => {
  it('is the wall clock, refreshed on its interval', () => {
    vi.useFakeTimers();
    vi.setSystemTime(Date.parse('2026-09-30T10:00:00Z'));
    const { result } = renderHook(() => useNow(1_000));
    expect(result.current).toBe(Date.parse('2026-09-30T10:00:00Z'));
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(result.current).toBe(Date.parse('2026-09-30T10:00:01Z'));
  });

  it('is the tape clock while a recording replays, so replayed rows are not days old', () => {
    useReplayStore.getState().set({ status: 'ready', recordedAt: '2026-09-28T09:00:00.000Z', position: 90_000 });
    const { result } = renderHook(() => useNow());
    expect(result.current).toBe(Date.parse('2026-09-28T09:01:30.000Z'));
    act(() => {
      useReplayStore.getState().set({ position: 120_000 });
    });
    expect(result.current).toBe(Date.parse('2026-09-28T09:02:00.000Z'));
  });
});
