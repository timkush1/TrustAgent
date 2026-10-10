/**
 * The tab's OpenRouter key for components (D102): `useSyncExternalStore` over the key store.
 * Owned by WEB-LIVE (LV-1).
 *
 * Step 0b stub: the frozen return shape; there is never a key yet.
 */
import type { BrowserKeyState } from './types';

export interface BrowserKey {
  key: string | null;
  state: BrowserKeyState;
  /** Aborts in-flight requests and clears the key, verifier and return path (usage stays). */
  forget: () => void;
}

const NO_KEY: BrowserKey = { key: null, state: 'none', forget: () => {} };

export function useBrowserKey(): BrowserKey {
  return NO_KEY;
}
