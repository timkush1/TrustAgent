/**
 * The landing's lazy parts: the sections below the fold, the glass-box panel and the footer's
 * source list. Each is its own chunk, outside the 110 KB first load (npm run size). They start
 * loading as soon as the page renders, each behind a placeholder of about its height, and
 * lazyPartsLoaded() tells the hash scroll when the page has its final layout.
 */
import { Component, lazy, Suspense, type ReactNode } from 'react';
import { loaders } from './lazyLoaders';

export const WriteChecks = lazy(() => loaders.checks().then((m) => ({ default: m.WriteChecks })));
export const TryIt = lazy(() => loaders.try().then((m) => ({ default: m.TryIt })));
export const Compare = lazy(() => loaders.compare().then((m) => ({ default: m.Compare })));
export const SelfHost = lazy(() => loaders.selfHost().then((m) => ({ default: m.SelfHost })));
export const Sources = lazy(() => loaders.sources().then((m) => ({ default: m.Sources })));
export const GlassBox = lazy(() => loaders.glassBox().then((m) => ({ default: m.GlassBox })));

/** Holds a lazy section's place, about its height, so the page does not jump when it arrives. */
function Placeholder({ minHeight, section }: { minHeight: string; section: boolean }) {
  return <div data-lazy-placeholder aria-hidden="true" className={section ? 'border-t border-border' : undefined} style={{ minHeight }} />;
}

/**
 * A part whose chunk failed to load (a deploy replaced it, the network dropped) says so in its
 * place instead of taking the whole page down; reloading fetches the current chunks.
 */
class PartBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div role="status" className="mx-auto w-full max-w-6xl border-t border-border px-6 py-10 text-sm text-fg-muted max-sm:px-4">
        This part of the page could not be loaded.{' '}
        <button type="button" onClick={() => window.location.reload()} className="font-medium text-fg underline underline-offset-2">
          Reload the page
        </button>
      </div>
    );
  }
}

export function LazyPart({ minHeight, section = true, children }: { minHeight: string; section?: boolean; children: ReactNode }) {
  return (
    <PartBoundary>
      <Suspense fallback={<Placeholder minHeight={minHeight} section={section} />}>{children}</Suspense>
    </PartBoundary>
  );
}
