/**
 * The live panel's run: one check at a time through runLiveCheck with this tab's key, caps and the
 * build's Jev model, cancelled by Forget key (each request is tracked by the key store) and when
 * the panel goes away. A cancel or a call that never had a key shows nothing; every other failure
 * keeps its error and the words the panel shows for it (liveErrorView). Owned by WEB-LIVE (LV-6).
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { playground } from '@/config/runtime';
import { runLiveCheck, type LiveCheckResult, type LiveInput } from '@/lib/gate/liveCheck';
import { openRouterJevCall } from '@/lib/openrouter/decisions';
import { liveErrorView, type LiveErrorView } from '@/lib/openrouter/errors';
import type { KeyStore } from '@/lib/openrouter/keyStore';
import type { UsageStore } from '@/lib/openrouter/usage';

export type LivePhase =
  | { kind: 'idle' }
  | { kind: 'running'; input: LiveInput }
  | { kind: 'done'; input: LiveInput; check: LiveCheckResult }
  | { kind: 'failed'; input: LiveInput; error: unknown; view: LiveErrorView };

export interface LiveCheckControls {
  phase: LivePhase;
  run: (input: LiveInput) => void;
  /** Back to idle (a screen was dismissed); a running check keeps running. */
  clear: () => void;
}

export function useLiveCheck(store: KeyStore, usage: UsageStore, maxTokens: number = playground.maxTokens): LiveCheckControls {
  const [phase, setPhase] = useState<LivePhase>({ kind: 'idle' });
  const current = useRef<AbortController | null>(null);

  useEffect(
    () => () => {
      current.current?.abort(new DOMException('the panel closed', 'AbortError'));
    },
    [],
  );

  const run = useCallback(
    (input: LiveInput) => {
      if (current.current !== null) return;
      const controller = new AbortController();
      current.current = controller;
      setPhase({ kind: 'running', input });
      runLiveCheck(input, {
        call: openRouterJevCall({ store }),
        ledger: usage.forModel(playground.jevModel),
        maxTokens,
        signal: controller.signal,
      })
        .then(
          (check) => setPhase({ kind: 'done', input, check }),
          (error: unknown) => {
            const view = liveErrorView(error);
            setPhase(view.silent ? { kind: 'idle' } : { kind: 'failed', input, error, view });
          },
        )
        .finally(() => {
          if (current.current === controller) current.current = null;
        });
    },
    [store, usage, maxTokens],
  );

  const clear = useCallback(() => {
    setPhase((p) => (p.kind === 'running' ? p : { kind: 'idle' }));
  }, []);

  return { phase, run, clear };
}
