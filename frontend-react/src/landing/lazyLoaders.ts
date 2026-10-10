/**
 * The landing's lazy chunks (lazyParts.tsx): each download starts once, and lazyPartsLoaded() says
 * when all of them are in, so the hash scroll lands on the final layout.
 */

/** Starts a chunk's download once and hands every caller the same promise. */
function once<T>(load: () => Promise<T>): () => Promise<T> {
  let p: Promise<T> | undefined;
  return () => (p ??= load());
}

export const loaders = {
  checks: once(() => import('./WriteChecks')),
  try: once(() => import('./TryIt')),
  compare: once(() => import('./Compare')),
  selfHost: once(() => import('./SelfHost')),
  sources: once(() => import('./Sources')),
  glassBox: once(() => import('./GlassBox')),
};

/** Resolves when every lazy part of the page has loaded (a failed chunk counts as settled). */
export function lazyPartsLoaded(): Promise<void> {
  return Promise.allSettled(Object.values(loaders).map((load) => load())).then(() => undefined);
}
