import { useMatches } from '@tanstack/react-router';
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
 * banner sits under it. The time-window picker joins when Overview does (P3).
 */
export function TopBar() {
  const title = usePageTitle();
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-bg/95 backdrop-blur-sm">
      <div className="flex h-12 items-center gap-3 px-6">
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold text-fg">{title}</h1>
        <ModeBadge />
        <HealthPill />
        <ThemeToggle />
      </div>
      <Banner />
    </header>
  );
}
