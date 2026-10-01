/**
 * Left navigation: an icon rail below 1024 px, labelled items above. Settings sits at the bottom.
 */
import { Link } from '@tanstack/react-router';
import { useRuntimeConfig } from '@/app/services';
import { StaticNavRail } from '@/components/replay/StaticNavRail';
import { Tooltip } from '@/components/ui/tooltip';
import { useSystemStatus } from '@/data/useSystem';
import { cn } from '@/lib/cn';
import { KNOWLEDGE_NAV_ITEM, NAV_ITEMS, type NavItem } from './nav';

const itemClass =
  'flex h-9 items-center gap-2.5 rounded-md px-2.5 text-sm text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg max-lg:w-9 max-lg:justify-center max-lg:px-0';

function NavLink({ item }: { item: NavItem }) {
  const Icon = item.icon;
  return (
    <Tooltip label={item.label}>
      <Link
        to={item.to}
        aria-label={item.label}
        className={itemClass}
        activeProps={{ className: 'bg-surface-hover font-medium text-fg', 'aria-current': 'page' }}
      >
        <Icon aria-hidden="true" className="size-4 shrink-0" />
        <span className="max-lg:sr-only">{item.label}</span>
      </Link>
    </Tooltip>
  );
}

export function NavRail() {
  return useRuntimeConfig().appMode === 'static' ? <StaticNavRail /> : <FullNavRail />;
}

function FullNavRail() {
  const knowledge = useSystemStatus().status?.knowledge.enabled === true;
  const main = NAV_ITEMS.filter((i) => i.to !== '/app/settings').flatMap((i) =>
    knowledge && i.to === '/app/playground' ? [i, KNOWLEDGE_NAV_ITEM] : [i],
  );
  const settings = NAV_ITEMS.find((i) => i.to === '/app/settings');
  return (
    <nav
      aria-label="Main"
      className="sticky top-0 flex h-dvh flex-col gap-1 border-r border-border bg-surface-sunken px-2 py-3 max-lg:items-center lg:w-52"
    >
      <Link
        to="/app"
        className={cn('mb-3 flex h-9 items-center gap-2 rounded-md px-2.5 text-base font-semibold text-fg', 'max-lg:px-0')}
      >
        <img src={`${import.meta.env.BASE_URL}favicon.svg`} alt="" className="size-5" />
        <span className="max-lg:sr-only">TrustAgent</span>
      </Link>
      {main.map((item) => (
        <NavLink key={item.to} item={item} />
      ))}
      <div className="mt-auto">{settings ? <NavLink item={settings} /> : null}</div>
    </nav>
  );
}
