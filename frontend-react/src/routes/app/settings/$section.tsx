import { createFileRoute, Link, notFound } from '@tanstack/react-router';
import { useEffect, useRef } from 'react';
import { SECTION_COMPONENTS } from '@/components/settings/sectionComponents';
import { isSettingsSection, SETTINGS_SECTIONS } from '@/components/settings/sections';

function SettingsPage() {
  const { section } = Route.useParams();
  const Section = isSettingsSection(section) ? SECTION_COMPONENTS[section] : null;
  const nav = useRef<HTMLElement>(null);
  // One row that scrolls sideways on a phone, rather than wrapping onto two: keep the open section
  // in view when the row is narrower than its tabs.
  useEffect(() => {
    const active = nav.current?.querySelector<HTMLElement>('[aria-current="page"]');
    if (active && typeof active.scrollIntoView === 'function') active.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }, [section]);
  return (
    <div className="space-y-6">
      <nav ref={nav} aria-label="Settings sections" className="-mx-1 flex gap-1 overflow-x-auto border-b border-border px-1 pb-2 [scrollbar-width:thin]">
        {SETTINGS_SECTIONS.map((s) => (
          <Link
            key={s.id}
            to="/app/settings/$section"
            params={{ section: s.id }}
            className="shrink-0 rounded-md px-2.5 py-1 text-sm whitespace-nowrap text-fg-muted hover:bg-surface-hover hover:text-fg"
            activeProps={{ className: 'bg-surface-hover font-medium text-fg', 'aria-current': 'page' }}
          >
            {s.label}
          </Link>
        ))}
      </nav>
      {Section ? <Section /> : null}
    </div>
  );
}

export const Route = createFileRoute('/app/settings/$section')({
  beforeLoad: ({ params }) => {
    if (!isSettingsSection(params.section)) throw notFound();
  },
  staticData: { title: 'Settings' },
  component: SettingsPage,
});
