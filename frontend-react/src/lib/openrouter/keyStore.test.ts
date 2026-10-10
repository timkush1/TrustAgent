import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { createKeyStore, KEY_TTL_MS, PKCE_TTL_MS, type KeyStore, type KeyStoreOptions } from './keyStore';
import { useBrowserKey } from './useBrowserKey';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const OTHER = 'sk-or-v1-e2e-ffffffffffffffffffffffffffffffff';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

/** A Map-backed Storage. */
class MemoryStorage implements Storage {
  readonly map = new Map<string, string>();
  get length() {
    return this.map.size;
  }
  clear() {
    this.map.clear();
  }
  getItem(k: string) {
    return this.map.get(k) ?? null;
  }
  key(i: number) {
    return [...this.map.keys()][i] ?? null;
  }
  removeItem(k: string) {
    this.map.delete(k);
  }
  setItem(k: string, v: string) {
    this.map.set(k, String(v));
  }
}

/** A clock and a timer queue the test advances by hand. */
function fakeTime(start = 1_760_000_000_000) {
  let t = start;
  const timers = new Map<number, { at: number; fn: () => void }>();
  let next = 1;
  return {
    now: () => t,
    setTimer: (fn: () => void, ms: number) => {
      const id = next++;
      timers.set(id, { at: t + ms, fn });
      return id;
    },
    clearTimer: (h: unknown) => {
      timers.delete(h as number);
    },
    advance(ms: number) {
      t += ms;
      for (const [id, tm] of [...timers]) {
        if (tm.at <= t) {
          timers.delete(id);
          tm.fn();
        }
      }
    },
    pending: () => timers.size,
  };
}

const stores: KeyStore[] = [];
function make(opts: KeyStoreOptions): KeyStore {
  const s = createKeyStore({ target: null, ...opts });
  stores.push(s);
  return s;
}

afterEach(() => {
  for (const s of stores.splice(0)) s.dispose();
  window.sessionStorage.clear();
});

describe('key store', () => {
  let storage: MemoryStorage;
  let time: ReturnType<typeof fakeTime>;
  beforeEach(() => {
    storage = new MemoryStorage();
    time = fakeTime();
  });
  const opts = () => ({ storage: () => storage, now: time.now, setTimer: time.setTimer, clearTimer: time.clearTimer });

  it('stores a key with the OpenRouter shape and refuses anything else', () => {
    const s = make(opts());
    for (const bad of ['', 'sk-or-short', 'sk-ant-0123456789abcdef0123', `${KEY} x`, NO_PATTERN, 'sk-or-v1-<script>alert(1)</script>']) {
      expect(s.set(bad)).toBe(false);
    }
    expect(s.getSnapshot()).toEqual({ key: null, state: 'none', stored_at: null, storage_blocked: false });
    expect(storage.length).toBe(0);

    expect(s.set(`  ${KEY}\n`)).toBe(true);
    expect(s.getSnapshot()).toEqual({ key: KEY, state: 'connected', stored_at: time.now(), storage_blocked: false });
    expect(s.currentKey()).toBe(KEY);
    // Only the key entry, in sessionStorage.
    expect([...storage.map.keys()]).toEqual(['ta.openrouter.key']);
  });

  it('keeps one snapshot object until something changes (useSyncExternalStore)', () => {
    const s = make(opts());
    const a = s.getSnapshot();
    s.refresh();
    expect(s.getSnapshot()).toBe(a);
    s.set(KEY);
    const b = s.getSnapshot();
    expect(b).not.toBe(a);
    s.refresh();
    expect(s.getSnapshot()).toBe(b);
  });

  it('forgets the key 2 h after it was stored, on its own timer and on any read', () => {
    const s = make(opts());
    let notified = 0;
    s.subscribe(() => notified++);
    s.set(KEY);
    notified = 0;

    time.advance(KEY_TTL_MS - 1000);
    expect(s.currentKey()).toBe(KEY);
    expect(notified).toBe(0);

    time.advance(1000 + 50);
    expect(s.getSnapshot().key).toBeNull();
    expect(notified).toBe(1);
    expect(storage.length).toBe(0);
    expect(time.pending()).toBe(0);
  });

  it('applies the TTL to a key a previous page stored, and drops a key from the future', () => {
    storage.setItem('ta.openrouter.key', JSON.stringify({ key: KEY, stored_at: time.now() - KEY_TTL_MS }));
    expect(make(opts()).getSnapshot().key).toBeNull();
    expect(storage.length).toBe(0);

    storage.setItem('ta.openrouter.key', JSON.stringify({ key: KEY, stored_at: time.now() + 10 * 60 * 1000 }));
    expect(make(opts()).getSnapshot().key).toBeNull();

    storage.setItem('ta.openrouter.key', JSON.stringify({ key: KEY, stored_at: time.now() - 1000 }));
    expect(make(opts()).getSnapshot()).toMatchObject({ key: KEY, state: 'connected' });
  });

  it('drops a stored entry that is not a key record', () => {
    for (const raw of ['not json', '[]', 'null', JSON.stringify({ key: NO_PATTERN, stored_at: time.now() }), JSON.stringify({ key: KEY })]) {
      storage.setItem('ta.openrouter.key', raw);
      expect(make(opts()).getSnapshot().key).toBeNull();
      expect(storage.getItem('ta.openrouter.key')).toBeNull();
    }
  });

  it('re-reads sessionStorage on a bfcache restore, so a key forgotten elsewhere in the tab is gone', () => {
    const target = new EventTarget() as unknown as Window;
    const page = make({ ...opts(), target });
    page.set(KEY);
    // Another page of this tab forgets the key while this one sits in the bfcache.
    make(opts()).forget();
    expect(page.getSnapshot().key).toBe(KEY);

    // A normal load builds a new store; only a restore re-reads.
    target.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: false }));
    expect(page.getSnapshot().key).toBe(KEY);
    target.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }));
    expect(page.getSnapshot().key).toBeNull();
  });

  it('re-applies the TTL on a bfcache restore after the 2 h passed while frozen', () => {
    const target = new EventTarget() as unknown as Window;
    const frozen = fakeTime();
    // A frozen page runs no timers: only the clock moves.
    const page = make({ storage: () => storage, now: frozen.now, setTimer: () => 0, clearTimer: () => {}, target });
    page.set(KEY);
    frozen.advance(KEY_TTL_MS + 1);
    expect(page.getSnapshot().key).toBe(KEY);
    target.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }));
    expect(page.getSnapshot().key).toBeNull();
  });

  it('falls back to memory when the browser blocks storage, and refuses to start Connect', () => {
    const s = make({
      ...opts(),
      storage: () => {
        throw new DOMException('The operation is insecure.', 'SecurityError');
      },
    });
    expect(s.getSnapshot()).toEqual({ key: null, state: 'none', stored_at: null, storage_blocked: true });
    expect(s.set(KEY)).toBe(true);
    expect(s.getSnapshot()).toMatchObject({ key: KEY, state: 'memory', storage_blocked: true });
    expect(s.savePending('verifier-0123456789', 'app/playground/#live')).toBe(false);
    expect(s.hasPending()).toBe(false);
    s.forget();
    expect(s.getSnapshot()).toMatchObject({ key: null, state: 'none', storage_blocked: true });
  });

  it('falls back to memory when a write throws, keeping the key it already had', () => {
    const s = make(opts());
    s.set(KEY);
    storage.setItem = () => {
      throw new DOMException('quota', 'QuotaExceededError');
    };
    expect(s.set(OTHER)).toBe(true);
    expect(s.getSnapshot()).toMatchObject({ key: OTHER, state: 'memory', storage_blocked: true });
    // Nothing is left behind in storage that Forget could not reach.
    expect(storage.getItem('ta.openrouter.key')).toBeNull();
    s.forget();
    expect(s.getSnapshot().key).toBeNull();
  });

  it('treats a failing probe write as blocked storage', () => {
    storage.setItem = () => {
      throw new DOMException('quota', 'QuotaExceededError');
    };
    expect(make(opts()).getSnapshot().storage_blocked).toBe(true);
  });

  it('Forget aborts every request in flight and clears the key, verifier and return path, but keeps usage', () => {
    const s = make(opts());
    s.set(KEY);
    expect(s.savePending('verifier-0123456789', 'app/playground/#live')).toBe(true);
    storage.setItem('ta.playground.usage', JSON.stringify({ calls: 3, rejected: 0, input_tokens: 900, cost_usd: 0.0001, cost_source: 'vendor' }));

    const a = new AbortController();
    const b = new AbortController();
    const done = new AbortController();
    s.track(a);
    s.track(b);
    s.track(done)();

    s.forget();
    expect(a.signal.aborted && b.signal.aborted).toBe(true);
    expect((a.signal.reason as DOMException).name).toBe('AbortError');
    expect(done.signal.aborted).toBe(false);
    expect(s.getSnapshot().key).toBeNull();
    expect(s.hasPending()).toBe(false);
    expect([...storage.map.keys()]).toEqual(['ta.playground.usage']);

    // A request tracked after Forget is not aborted by it.
    const later = new AbortController();
    s.track(later);
    expect(later.signal.aborted).toBe(false);
  });

  it('hands a pending Connect sign-in over once, within 10 minutes', () => {
    const s = make(opts());
    expect(s.takePending()).toEqual({ status: 'missing' });

    expect(s.savePending('verifier-0123456789', 'app/playground/?scenario=grounded-pass#live')).toBe(true);
    expect(s.hasPending()).toBe(true);
    expect(s.takePending()).toEqual({
      status: 'ok',
      verifier: 'verifier-0123456789',
      returnPath: 'app/playground/?scenario=grounded-pass#live',
      created_at: time.now(),
    });
    expect(s.takePending()).toEqual({ status: 'missing' });

    s.savePending('verifier-0123456789', null);
    time.advance(PKCE_TTL_MS + 1);
    expect(s.takePending()).toEqual({ status: 'expired' });
    // Expired is one-shot too.
    expect(s.takePending()).toEqual({ status: 'missing' });
    expect(storage.length).toBe(0);
  });

  it('writes nothing to localStorage or cookies', () => {
    window.localStorage.clear();
    const s = make({ target: window });
    s.set(KEY);
    s.savePending('verifier-0123456789', 'app/playground/#live');
    expect(window.sessionStorage.getItem('ta.openrouter.key')).toContain(KEY);
    expect(window.localStorage.length).toBe(0);
    expect(document.cookie).not.toContain(KEY);
  });
});

describe('useBrowserKey', () => {
  it('follows the store: set, forget and the snapshot shape', () => {
    const storage = new MemoryStorage();
    const s = make({ storage: () => storage });
    const { result } = renderHook(() => useBrowserKey(s));
    expect(result.current).toMatchObject({ key: null, state: 'none', storageBlocked: false });

    act(() => {
      expect(result.current.set('nope')).toBe(false);
    });
    expect(result.current.key).toBeNull();

    act(() => {
      result.current.set(KEY);
    });
    expect(result.current).toMatchObject({ key: KEY, state: 'connected' });
    const first = result.current;

    act(() => {
      result.current.forget();
    });
    expect(result.current).toMatchObject({ key: null, state: 'none' });
    expect(result.current).not.toBe(first);
  });
});
