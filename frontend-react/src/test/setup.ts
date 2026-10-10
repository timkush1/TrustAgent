import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

// jsdom has no matchMedia; sonner and the theme code query it. Always "no match" (light, motion ok).
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  window.matchMedia = (query: string): MediaQueryList => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  });
}

// jsdom does not implement scrolling; the router restores scroll positions on navigation.
if (typeof window !== 'undefined') window.scrollTo = () => {};

// No test reaches the network (Rule 6, D138): an absolute fetch that nothing mocked (msw, a
// fetchImpl, vi.spyOn or vi.stubGlobal) is refused, and the test fails even when the code under test
// catches the error.
const unmockedFetches: string[] = [];
const realFetch = globalThis.fetch;
if (typeof realFetch === 'function') {
  globalThis.fetch = function guardedFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    if (/^[a-z][a-z0-9+.-]*:/i.test(url) && !/^(data|blob):/i.test(url)) {
      unmockedFetches.push(url);
      return Promise.reject(new TypeError(`unmocked fetch to ${url}: mock it with msw, a fetchImpl, vi.spyOn or vi.stubGlobal`));
    }
    return realFetch(input, init);
  } as typeof fetch;
}

afterEach(() => {
  cleanup();
  const seen = unmockedFetches.splice(0);
  if (seen.length > 0) throw new Error(`this test fetched the network without a mock: ${seen.join(', ')}`);
});
