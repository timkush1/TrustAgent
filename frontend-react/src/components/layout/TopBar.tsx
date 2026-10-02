import { useMatches } from '@tanstack/react-router';
import { useRuntimeConfig } from '@/app/services';
import { ReplayStatus } from '@/components/replay/ReplayStatus';
import { Banner } from './Banner';
import { HealthPill } from './HealthPill';
import { ModeBadge } from './ModeBadge';
import { ThemeToggle } from './ThemeToggle';

/** The deepest matched route's `staticData.title`. */
function usePageTitle(): string {
  const matches = useMatches();
  for (let i = matches.length - 1; i >= 0; i -= 1) {
    const title = matches[i].staticData?.title;
    if (title) return title;
  }
  return 'TrustAgent';
}

/**
 * Top bar (SPEC §3.3): page title, demo/read-only badge, Health pill, theme toggle; the single
 * banner sits under it. The time window is the Overview's own control, in its page header
 * (DESIGN.md §12a), until the top bar grows one.
 */
export function TopBar() {
  const title = usePageTitle();
  // The static build replays a recording: no gateway health, no demo/read-only badge (its banner says it).
  const isStatic = useRuntimeConfig().appMode === 'static';
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-bg/95 backdrop-blur-sm">
      <div className="flex h-12 items-center gap-3 px-6 max-sm:gap-2 max-sm:px-4">
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold text-fg">{title}</h1>
        {isStatic ? <ReplayStatus /> : <ModeBadge />}
        {isStatic ? null : <HealthPill />}
        <ThemeToggle />
      </div>
      <Banner />
    </header>
  );
}
