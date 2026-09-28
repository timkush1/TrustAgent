import { createFileRoute } from '@tanstack/react-router';
import { Term } from '@/components/common/Term';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

function PoliciesPage() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[3].question}
      phase="P3"
      phaseName="integration"
      planned={[
        'One card per route: how it is matched, its mode and what it is currently acting as',
        'Block readiness, with one-click fixes when blocking is downgraded',
        'Preview impact (free — replays past answers) before saving, then versions and rollback',
        'A YAML editor for advanced changes',
      ]}
      meanwhile={
        <p>
          Today every <Term id="route">route</Term> and its <Term id="policy">policy</Term> come from{' '}
          <code className="font-mono text-xs text-fg">config/policies.yaml</code>, read when the proxy starts.
        </p>
      }
    />
  );
}

export const Route = createFileRoute('/app/policies')({
  staticData: { title: 'Policies' },
  component: PoliciesPage,
});
