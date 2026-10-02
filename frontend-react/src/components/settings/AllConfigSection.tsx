import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

/**
 * Settings → all configuration (SPEC §3.3). WEB-ON replaces this placeholder with the section (P4-PLAN §10).
 */
export function AllConfigSection() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[5].question}
      phase="P4"
      phaseName="security, credentials, Studio, ROI"
      planned={[
        'Every setting, where it comes from, read-only',
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
