/**
 * The tab's OpenRouter key (D102): the only module that knows the sessionStorage names of the key
 * and of a pending PKCE sign-in (ESLint bans those literals everywhere else).
 *
 * - `ta.openrouter.key` `{key, stored_at}`: the key, forgotten 2 h after it was stored;
 * - `ta.pkce.verifier` `{verifier, created_at}` and `ta.pkce.return`: a Connect that left for
 *   openrouter.ai and has not come back yet (one-shot, 10 min).
 *
 * Nothing is written to localStorage, cookies or IndexedDB. A bfcache restore (`pageshow` with
 * `persisted`) and a tab that becomes visible again re-read sessionStorage and apply the TTL, so a
 * page restored after Forget key (on another page of this tab) or after the 2 h never shows the old
 * key. When the browser refuses sessionStorage, the key lives in memory until the page is left and
 * the snapshot says `storage_blocked` (Connect is then off: the verifier would not survive the
 * redirect). Forget key aborts every request in flight first. Owned by WEB-LIVE (LV-1).
 */
import type { BrowserKeySnapshot, BrowserKeyStore } from './types';

/** The key is forgotten this long after it was stored (D102). */
export const KEY_TTL_MS = 2 * 60 * 60 * 1000;
/** A Connect sign-in must come back within this time (D102, D105). */
export const PKCE_TTL_MS = 10 * 60 * 1000;
/** What an OpenRouter key looks like (D102). */
export const KEY_SHAPE = /^sk-or-[A-Za-z0-9_-]{16,}$/;

const KEY_NAME = 'ta.openrouter.key';
const VERIFIER_NAME = 'ta.pkce.verifier';
const RETURN_NAME = 'ta.pkce.return';
const PROBE_NAME = 'ta.openrouter.probe';

/** A clock running backwards by more than this makes a stored time untrustworthy (the key is dropped). */
const CLOCK_SKEW_MS = 60 * 1000;
/** setTimeout's largest delay. */
const MAX_TIMER_MS = 2 ** 31 - 1;

/** The result of taking the pending Connect sign-in. */
export type PendingTake =
  | { status: 'ok'; verifier: string; returnPath: string | null; created_at: number }
  | { status: 'expired' }
  | { status: 'missing' };

/** The key store with the PKCE pending state the static callback needs (LV-4, LV-5). */
export interface KeyStore extends BrowserKeyStore {
  /** The key to send now (the TTL applied first), or null. */
  currentKey(): string | null;
  /**
   * Remembers a Connect sign-in until the callback takes it. False when storage is blocked: the
   * verifier would not survive the redirect, so Connect must not start.
   */
  savePending(verifier: string, returnPath: string | null): boolean;
  /** Takes the pending sign-in once: both names are removed whatever the outcome. */
  takePending(): PendingTake;
  /** Whether a sign-in is waiting (without taking it), for the callback's "already connected" state. */
  hasPending(): boolean;
  /** Re-reads sessionStorage and applies the TTL (also run on pageshow and visibilitychange). */
  refresh(): void;
  /** Removes the listeners and the expiry timer (tests). */
  dispose(): void;
}

export interface KeyStoreOptions {
  /** Returns the tab's sessionStorage; may throw (a browser that blocks storage). */
  storage?: () => Storage;
  now?: () => number;
  /** Where to listen for pageshow and visibilitychange; null listens nowhere. */
  target?: Pick<Window, 'addEventListener' | 'removeEventListener'> | null;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const isTime = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v) && v >= 0;

/** Whether `key` has the OpenRouter key shape. */
export function isKeyShape(key: string): boolean {
  return KEY_SHAPE.test(key);
}

const NO_KEY_BLOCKED: BrowserKeySnapshot = Object.freeze({ key: null, state: 'none', stored_at: null, storage_blocked: true });
const NO_KEY: BrowserKeySnapshot = Object.freeze({ key: null, state: 'none', stored_at: null, storage_blocked: false });

/** The snapshot before any store exists (server render, prerender). */
export const SERVER_KEY_SNAPSHOT: BrowserKeySnapshot = NO_KEY;

export function createKeyStore(opts: KeyStoreOptions = {}): KeyStore {
  const now = opts.now ?? Date.now;
  const getStorage =
    opts.storage ??
    ((): Storage => {
      if (typeof window === 'undefined') throw new Error('no window');
      return window.sessionStorage;
    });
  const target = opts.target === undefined ? (typeof window === 'undefined' ? null : window) : opts.target;
  const setTimer = opts.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms));
  const clearTimer = opts.clearTimer ?? ((h: unknown) => clearTimeout(h as ReturnType<typeof setTimeout>));

  const listeners = new Set<() => void>();
  const inflight = new Set<AbortController>();
  /** The in-memory fallback, used once storage refused to work. */
  const memory = new Map<string, string>();
  let blocked = false;
  let snapshot: BrowserKeySnapshot = NO_KEY;
  let timer: unknown = null;

  // Storage access: any exception switches this page to the in-memory fallback for good, keeping
  // what storage still lets us read.
  const fallBack = (s: Storage | null): void => {
    if (blocked) return;
    blocked = true;
    if (s === null) return;
    for (const name of [KEY_NAME, VERIFIER_NAME, RETURN_NAME]) {
      try {
        const v = s.getItem(name);
        if (v !== null && !memory.has(name)) memory.set(name, v);
        s.removeItem(name);
      } catch {
        // Unreadable: nothing to keep.
      }
    }
  };
  const storage = (): Storage | null => {
    if (blocked) return null;
    try {
      return getStorage();
    } catch {
      fallBack(null);
      return null;
    }
  };
  const read = (name: string): string | null => {
    const s = storage();
    if (s === null) return memory.get(name) ?? null;
    try {
      return s.getItem(name);
    } catch {
      fallBack(null);
      return memory.get(name) ?? null;
    }
  };
  const write = (name: string, value: string): void => {
    const s = storage();
    if (s !== null) {
      try {
        s.setItem(name, value);
        return;
      } catch {
        fallBack(s);
      }
    }
    memory.set(name, value);
  };
  const remove = (name: string): void => {
    memory.delete(name);
    const s = storage();
    if (s === null) return;
    try {
      s.removeItem(name);
    } catch {
      fallBack(null);
    }
  };
  const parse = (raw: string | null): Obj | null => {
    if (raw === null) return null;
    try {
      const v: unknown = JSON.parse(raw);
      return isObj(v) ? v : null;
    } catch {
      return null;
    }
  };

  // Probe once: a browser that blocks storage throws on access or on the first write.
  {
    const s = storage();
    if (s !== null) {
      try {
        s.setItem(PROBE_NAME, '1');
        s.removeItem(PROBE_NAME);
      } catch {
        fallBack(s);
      }
    }
  }

  const emit = (): void => {
    for (const l of [...listeners]) l();
  };

  const schedule = (expiresAt: number | null): void => {
    if (timer !== null) clearTimer(timer);
    timer = null;
    if (expiresAt === null) return;
    timer = setTimer(() => {
      timer = null;
      refresh();
    }, Math.min(MAX_TIMER_MS, Math.max(0, expiresAt - now()) + 50));
  };

  /** The stored key if it is well formed and inside its TTL; anything else is removed. */
  const readKey = (): { key: string; stored_at: number } | null => {
    const raw = read(KEY_NAME);
    if (raw === null) return null;
    const v = parse(raw);
    const t = now();
    if (
      v === null ||
      typeof v.key !== 'string' ||
      !isKeyShape(v.key) ||
      !isTime(v.stored_at) ||
      v.stored_at > t + CLOCK_SKEW_MS ||
      t - v.stored_at >= KEY_TTL_MS
    ) {
      remove(KEY_NAME);
      return null;
    }
    return { key: v.key, stored_at: v.stored_at };
  };

  const setSnapshot = (next: BrowserKeySnapshot): void => {
    const cur = snapshot;
    if (cur.key === next.key && cur.state === next.state && cur.stored_at === next.stored_at && cur.storage_blocked === next.storage_blocked) return;
    snapshot = next;
    emit();
  };

  function refresh(): void {
    const k = readKey();
    if (k === null) {
      schedule(null);
      setSnapshot(blocked ? NO_KEY_BLOCKED : NO_KEY);
      return;
    }
    schedule(k.stored_at + KEY_TTL_MS);
    setSnapshot({ key: k.key, state: blocked ? 'memory' : 'connected', stored_at: k.stored_at, storage_blocked: blocked });
  }

  const onPageShow = (e: Event): void => {
    if ((e as PageTransitionEvent).persisted) refresh();
  };
  const onVisibility = (): void => {
    if (typeof document === 'undefined' || document.visibilityState === 'visible') refresh();
  };
  target?.addEventListener('pageshow', onPageShow);
  target?.addEventListener('visibilitychange', onVisibility);

  refresh();

  const store: KeyStore = {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => snapshot,
    set(key) {
      const k = key.trim();
      if (!isKeyShape(k)) return false;
      write(KEY_NAME, JSON.stringify({ key: k, stored_at: now() }));
      refresh();
      return true;
    },
    forget() {
      for (const c of [...inflight]) c.abort(new DOMException('the key was forgotten', 'AbortError'));
      inflight.clear();
      remove(KEY_NAME);
      remove(VERIFIER_NAME);
      remove(RETURN_NAME);
      refresh();
    },
    track(controller) {
      inflight.add(controller);
      return () => {
        inflight.delete(controller);
      };
    },
    currentKey() {
      refresh();
      return snapshot.key;
    },
    savePending(verifier, returnPath) {
      if (blocked) return false;
      write(VERIFIER_NAME, JSON.stringify({ verifier, created_at: now() }));
      if (returnPath === null) remove(RETURN_NAME);
      else write(RETURN_NAME, returnPath);
      // A write that failed just now fell back to memory, which a redirect does not survive.
      if (blocked) {
        remove(VERIFIER_NAME);
        remove(RETURN_NAME);
        refresh();
        return false;
      }
      return true;
    },
    takePending() {
      const v = parse(read(VERIFIER_NAME));
      const returnPath = read(RETURN_NAME);
      remove(VERIFIER_NAME);
      remove(RETURN_NAME);
      if (v === null || typeof v.verifier !== 'string' || v.verifier === '' || !isTime(v.created_at)) return { status: 'missing' };
      const age = now() - v.created_at;
      if (age > PKCE_TTL_MS || age < -CLOCK_SKEW_MS) return { status: 'expired' };
      return { status: 'ok', verifier: v.verifier, returnPath, created_at: v.created_at };
    },
    hasPending() {
      return read(VERIFIER_NAME) !== null;
    },
    refresh,
    dispose() {
      schedule(null);
      target?.removeEventListener('pageshow', onPageShow);
      target?.removeEventListener('visibilitychange', onVisibility);
      listeners.clear();
    },
  };
  return store;
}

let shared: KeyStore | null = null;

/** This tab's key store (created on first use, never during a server render). */
export function browserKeyStore(): KeyStore {
  shared ??= createKeyStore();
  return shared;
}
