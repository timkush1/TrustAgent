/**
 * The P3 golden trace details (contracts/fixtures/api/TraceDetail.<scenario>.json, validated by
 * `make contracts-check`): payments-fallback, whose contradicted answer was replaced by a fallback
 * answer that passed its own check (attempts 0 and 1), and help-center-link-block, a buffered
 * stream blocked because its link is not in the article (a detector finding). Both were derived
 * from the scenario pack's `expect` blocks with the TypeScript decision core, as the demo stack
 * would store them.
 */
import helpCenterLinkBlock from '@contracts/fixtures/api/TraceDetail.help-center-link-block.json';
import paymentsFallback from '@contracts/fixtures/api/TraceDetail.payments-fallback.json';
import type { TraceDetail } from '@/types/api';

export type P3ScenarioId = 'payments-fallback' | 'help-center-link-block';

const FIXTURES: Record<P3ScenarioId, unknown> = {
  'payments-fallback': paymentsFallback,
  'help-center-link-block': helpCenterLinkBlock,
};

/** A fresh copy of the fixture, safe to change in a test. */
export function p3Detail(id: P3ScenarioId): TraceDetail {
  return structuredClone(FIXTURES[id]) as TraceDetail;
}
