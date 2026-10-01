import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

/**
 * Settings → models (SPEC §3.3). WEB-ON replaces this placeholder with the section (P4-PLAN §10).
 */
export function ModelsSection() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[5].question}
      phase="P4"
      phaseName="security, credentials, Studio, ROI"
      planned={[
        'The fast-check backend and the deep-audit judge',
        'Test the fast check with one probe',
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
