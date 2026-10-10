/**
 * The tab's OpenRouter key for components (D102): `useSyncExternalStore` over the key store, so
 * every component sees the same key, a Forget key or the 2 h expiry at once, and a bfcache restore
 * re-reads sessionStorage. Owned by WEB-LIVE (LV-1).
 */
import { useMemo, useSyncExternalStore } from 'react';
import { browserKeyStore, SERVER_KEY_SNAPSHOT, type KeyStore } from './keyStore';
import type { BrowserKeyState } from './types';

export interface BrowserKey {
  key: string | null;
  state: BrowserKeyState;
  /** Aborts in-flight requests and clears the key, verifier and return path (usage stays). */
  forget: () => void;
  /** Stores a pasted key; false when it does not have the OpenRouter key shape. */
  set: (key: string) => boolean;
  /** This browser refuses sessionStorage: the key lives in memory only and Connect is off. */
  storageBlocked: boolean;
}

const serverSnapshot = () => SERVER_KEY_SNAPSHOT;

export function useBrowserKey(store: KeyStore = browserKeyStore()): BrowserKey {
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot, serverSnapshot);
  return useMemo(
    () => ({ key: snap.key, state: snap.state, forget: store.forget, set: store.set, storageBlocked: snap.storage_blocked }),
    [snap, store],
  );
}
