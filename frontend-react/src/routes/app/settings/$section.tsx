import { createFileRoute, Link, notFound } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';
import { isSettingsSection, SETTINGS_SECTIONS } from '@/components/settings/sections';

function SettingsPage() {
  const { section } = Route.useParams();
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
      <PhasePlaceholder
        question={NAV_ITEMS[5].question}
        phase="P4"
        phaseName="security, credentials, Studio, ROI"
        planned={SETTINGS_SECTIONS.map((s) => (s.id === section ? `${s.label} (this section): ${s.planned}` : `${s.label}: ${s.planned}`))}
        meanwhile={
          <p>
            Today configuration comes from environment variables (see{' '}
            <code className="font-mono text-xs text-fg">.env.example</code>) and{' '}
            <code className="font-mono text-xs text-fg">config/policies.yaml</code>.
          </p>
        }
      />
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
