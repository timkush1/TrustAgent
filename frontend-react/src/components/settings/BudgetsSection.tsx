import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

/**
 * Settings → budgets (SPEC §3.3). WEB-SV replaces this placeholder with the section (P4-PLAN §13).
 */
export function BudgetsSection() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[5].question}
      phase="P4"
      phaseName="security, credentials, Studio, ROI"
      planned={[
        "Daily spend caps with today's spend",
        'Prices used for estimates, with your overrides',
      ]}
      meanwhile={
        <p>
          Today configuration comes from environment variables (see{' '}
          <code className="font-mono text-xs text-fg">.env.example</code>) and{' '}
          <code className="font-mono text-xs text-fg">config/policies.yaml</code>.
        </p>
      }
    />
  );
}
