/**
 * The Overview "Savings" tab (SPEC §3.3, §7.4): the savings strip, then what the full panel will
 * show. WEB-SV replaces the placeholder with the panel (P4-PLAN §13 SV-2).
 */
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';
import { SavingsStrip } from '@/components/overview/SavingsStrip';
import type { TimeWindow } from '@/types/api';

export function SavingsPanel({ timeWindow }: { timeWindow: TimeWindow }) {
  return (
    <div className="space-y-5">
      <SavingsStrip timeWindow={timeWindow} />
      <PhasePlaceholder
        question={NAV_ITEMS[0].question}
        phase="P4"
        phaseName="security, credentials, Studio, ROI"
        planned={[
          'Your measured cost per 1,000 answers over time, against what an LLM would have cost for the same checks',
          'A component table where every number says whether it is measured, an estimate, vendor or simulated',
          'Fast-check p50 / p95 and how often the deadline was missed',
          'Sampling status: uniform audits today and budget caps reached',
        ]}
      />
    </div>
  );
}
