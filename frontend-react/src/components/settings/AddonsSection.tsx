import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

/**
 * Settings → add-ons (SPEC §3.3). WEB-ON replaces this placeholder with the section (P4-PLAN §10).
 */
export function AddonsSection() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[5].question}
      phase="P4"
      phaseName="security, credentials, Studio, ROI"
      planned={[
        'The knowledge base and a local LLM',
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
