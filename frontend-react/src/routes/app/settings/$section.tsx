import { createFileRoute, Link, notFound } from '@tanstack/react-router';
import { SECTION_COMPONENTS } from '@/components/settings/sectionComponents';
import { isSettingsSection, SETTINGS_SECTIONS } from '@/components/settings/sections';

function SettingsPage() {
  const { section } = Route.useParams();
  const Section = isSettingsSection(section) ? SECTION_COMPONENTS[section] : null;
  return (
    <div className="space-y-6">
      <nav aria-label="Settings sections" className="flex flex-wrap gap-1 border-b border-border pb-2">
        {SETTINGS_SECTIONS.map((s) => (
          <Link
            key={s.id}
            to="/app/settings/$section"
            params={{ section: s.id }}
            className="rounded-md px-2.5 py-1 text-sm text-fg-muted hover:bg-surface-hover hover:text-fg"
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
