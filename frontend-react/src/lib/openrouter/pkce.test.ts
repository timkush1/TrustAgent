import { afterEach, describe, expect, it } from 'vitest';
import { createKeyStore, PKCE_TTL_MS, type KeyStore } from './keyStore';
import { authUrl, base64Url, challengeS256, KEY_LABEL, PKCE_VERIFIER_BYTES, uriComponent, VERIFIER_SHAPE, verifierFromBytes } from './pkce';
import { createVerifier } from './random';
import { MemoryStorage } from './testing';

const stores: KeyStore[] = [];
function make(storage: Storage, now: () => number): KeyStore {
  const s = createKeyStore({ storage: () => storage, now, target: null, setTimer: () => 0, clearTimer: () => {} });
  stores.push(s);
  return s;
}

afterEach(() => {
  for (const s of stores.splice(0)) s.dispose();
  window.localStorage.clear();
});

describe('verifier', () => {
  it('is 43 characters of the base64url alphabet, with no padding', () => {
    for (let i = 0; i < 50; i += 1) {
      const v = createVerifier();
      expect(v).toHaveLength(43);
      expect(v).toMatch(VERIFIER_SHAPE);
      expect(v).not.toContain('=');
    }
  });

  it('comes from 32 fresh random bytes each time', () => {
    const seen: number[] = [];
    const v = createVerifier((bytes) => {
      seen.push(bytes.length);
      bytes.fill(0xff);
      return bytes;
    });
    expect(seen).toEqual([PKCE_VERIFIER_BYTES]);
    expect(v).toBe(`${'_'.repeat(42)}8`);
    expect(new Set(Array.from({ length: 20 }, () => createVerifier())).size).toBe(20);
  });

  it('refuses any other number of bytes', () => {
    expect(() => verifierFromBytes(new Uint8Array(31))).toThrow(RangeError);
    expect(() => verifierFromBytes(new Uint8Array(33))).toThrow(RangeError);
  });

  it('encodes base64url without padding for every remainder', () => {
    expect(base64Url(new Uint8Array([]))).toBe('');
    expect(base64Url(new Uint8Array([0xfb]))).toBe('-w');
    expect(base64Url(new Uint8Array([0xfb, 0xff]))).toBe('-_8');
    expect(base64Url(new Uint8Array([0xfb, 0xff, 0xbf]))).toBe('-_-_');
  });
});

describe('challenge and auth URL', () => {
  it('derives the RFC 7636 Appendix B challenge', () => {
    expect(challengeS256('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
    expect(() => challengeS256('')).toThrow(RangeError);
  });

  it('puts the parameters in order, percent-encoded like Go', () => {
    const url = authUrl({ callbackUrl: 'http://localhost:24174/TrustAgent/connect/callback/', challenge: 'abc', keyLabel: KEY_LABEL });
    expect(url).toBe(
      'https://openrouter.ai/auth?callback_url=http%3A%2F%2Flocalhost%3A24174%2FTrustAgent%2Fconnect%2Fcallback%2F&code_challenge=abc&code_challenge_method=S256&key_label=TrustAgent%20demo',
    );
    expect(authUrl({ callbackUrl: 'x', challenge: 'y' })).toBe('https://openrouter.ai/auth?callback_url=x&code_challenge=y&code_challenge_method=S256');
    expect(() => authUrl({ callbackUrl: '', challenge: 'y' })).toThrow(RangeError);
  });

  it("encodes ! ' ( ) * and non-ASCII, which encodeURIComponent would not or would differently", () => {
    expect(uriComponent("a!'()*~-._ z")).toBe('a%21%27%28%29%2A~-._%20z');
    expect(uriComponent('é')).toBe('%C3%A9');
  });
});

describe('pending sign-in', () => {
  it('is taken once: a second take finds nothing', () => {
    const storage = new MemoryStorage();
    const store = make(storage, () => 1_000);
    const verifier = createVerifier();
    expect(store.savePending(verifier, 'app/playground/#live')).toBe(true);
    expect(store.hasPending()).toBe(true);
    expect(store.takePending()).toEqual({ status: 'ok', verifier, returnPath: 'app/playground/#live', created_at: 1_000 });
    expect(store.takePending()).toEqual({ status: 'missing' });
    expect(store.hasPending()).toBe(false);
    expect(storage.length).toBe(0);
  });

  it('expires after 10 minutes and is removed by the take', () => {
    const storage = new MemoryStorage();
    let t = 5_000;
    const store = make(storage, () => t);
    store.savePending(createVerifier(), null);
    t += PKCE_TTL_MS;
    // Exactly 10 minutes is still in time.
    const fresh = make(storage, () => t);
    expect(fresh.takePending().status).toBe('ok');
    fresh.savePending(createVerifier(), null);
    t += PKCE_TTL_MS + 1;
    expect(fresh.takePending()).toEqual({ status: 'expired' });
    expect(fresh.takePending()).toEqual({ status: 'missing' });
    expect(storage.length).toBe(0);
  });

  it('is refused when storage is blocked, so Connect never starts', () => {
    const store = make(
      {
        get length() {
          return 0;
        },
        clear() {},
        key: () => null,
        getItem: () => {
          throw new DOMException('blocked', 'SecurityError');
        },
        setItem: () => {
          throw new DOMException('blocked', 'SecurityError');
        },
        removeItem: () => {},
      },
      () => 0,
    );
    expect(store.getSnapshot().storage_blocked).toBe(true);
    expect(store.savePending(createVerifier(), null)).toBe(false);
    expect(store.takePending()).toEqual({ status: 'missing' });
  });

  it('writes nothing to localStorage or cookies', () => {
    const store = make(window.sessionStorage, () => 0);
    store.savePending(createVerifier(), 'app/playground/#live');
    store.takePending();
    expect(window.localStorage.length).toBe(0);
    expect(document.cookie).toBe('');
  });
});
