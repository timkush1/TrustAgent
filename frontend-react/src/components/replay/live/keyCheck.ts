/**
 * The free key check (`GET /api/v1/key`, never counted; D100, D101) once per key per page load:
 * PasteKey runs it before it accepts a key, and the live panel reads the same answer to advise a
 * key without a credit limit (`key_no_limit`). A key OpenRouter refuses is forgotten. Owned by
 * WEB-LIVE (LV-6).
 */
import { useEffect, useState } from 'react';
import { errorKind } from '@/lib/openrouter/errors';
import { getKeyInfo, type KeyInfo } from '@/lib/openrouter/keyInfo';
import type { KeyStore } from '@/lib/openrouter/keyStore';

let cache: { key: string; promise: Promise<KeyInfo> } | null = null;

/** The key's info, asked once per key; a failed check is asked again next time. */
export function checkKeyOnce(key: string, store: KeyStore): Promise<KeyInfo> {
  if (cache === null || cache.key !== key) {
    const promise = getKeyInfo(key, { track: store.track });
    cache = { key, promise };
    promise.catch((err: unknown) => {
      if (cache?.promise === promise) cache = null;
      // OpenRouter refused the key: it is forgotten, as after a refused check (D100).
      if (errorKind(err) === 'auth' && store.currentKey() === key) store.forget();
    });
  }
  return cache.promise;
}

/** Forgets the cached answer (tests). */
export function resetKeyCheckForTests(): void {
  cache = null;
}

/** The connected key's info once it is known (null before, without a key, or when the check failed). */
export function useKeyInfo(key: string | null, store: KeyStore): KeyInfo | null {
  const [known, setKnown] = useState<{ key: string; info: KeyInfo } | null>(null);
  useEffect(() => {
    if (key === null) return;
    let live = true;
    checkKeyOnce(key, store).then(
      (info) => {
        if (live) setKnown({ key, info });
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, [key, store]);
  return key !== null && known?.key === key ? known.info : null;
}
