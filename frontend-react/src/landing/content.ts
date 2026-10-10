/**
 * Words and examples the landing page takes from the contracts, so the page shows the same refund
 * example, outcome words, commands and limits as the product (SPEC §8.1). This module is part of
 * the first load (110 KB budget), so it carries only what the hero and the sections above the fold
 * read; a lazy section imports its own words (sectionCopy.ts).
 */
import commandsDoc from '@contracts/ui/commands.v1.json';
import type { copy as contractCopy } from '@contracts/ui/copy.v1.json';
// The parts of contracts/ui/outcomes.v1.json the landing reads (vite/plugins/landingData.ts).
import { outcomes as outcomesDoc } from 'virtual:trustagent/landing-data';
// Only this scenario, not the whole pack (vite.site.ts scenarioModules): it keeps the landing small.
import refundScenario from 'virtual:trustagent/scenario/refund-contradiction-flag';
// Only the words the first load shows, not the whole tree (vite.site.ts copyModules): the
// dashboard's copy grows every phase and would eat the landing's 110 KB budget. Keep the list and
// LandingCopy in step; landing.test.tsx checks them against the contract.
import { copy as landingCopy } from 'virtual:trustagent/copy/product,tiers,value_labels';

type ContractCopy = typeof contractCopy;
/** The parts of contracts/ui/copy.v1.json the landing's first load reads. */
export type LandingCopy = Pick<ContractCopy, 'product' | 'tiers' | 'value_labels'>;
export const copy = landingCopy as LandingCopy;

export const BASE: string = import.meta.env.BASE_URL;
export const APP_MODE: 'full' | 'static' = import.meta.env.VITE_APP_MODE === 'static' ? 'static' : 'full';
/**
 * "Try the demo": the replay's Traffic page on the static site (a real file there, see
 * vite.site.ts STATIC_APP_PAGES), the dashboard next to /welcome in the full build.
 */
export const DEMO_HREF = APP_MODE === 'static' ? `${BASE}app/traffic/` : `${BASE}app`;
export const REPO_URL: string = (import.meta.env.VITE_REPO_URL as string | undefined) || 'https://github.com/timkush1/TrustAgent';

export function command(id: string): string {
  const c = commandsDoc.commands.find((x) => x.id === id);
  if (!c) throw new Error(`contracts/ui/commands.v1.json has no command ${id}`);
  return c.command;
}

/** Every outcome id, in contract order (the order the dashboard lists them). */
export const OUTCOME_IDS: readonly string[] = outcomesDoc.outcomes.map((o) => o.id);

export function outcome(id: string): { label: string; tone: string; headline: string } {
  const o = outcomesDoc.outcomes.find((x) => x.id === id);
  if (!o) throw new Error(`contracts/ui/outcomes.v1.json has no outcome ${id}`);
  return o;
}

/** The first matching rule of ui/outcomes.v1.json, as the dashboard decides a trace's outcome. */
export function outcomeOf(t: { gate_status: string; action: string | null; would_action: string | null }): string {
  const fields: Record<string, string | null> = { gate_status: t.gate_status, action: t.action, would_action: t.would_action };
  for (const rule of outcomesDoc.rules) {
    if (Object.entries(rule.when).every(([k, list]) => list.includes(fields[k] ?? ''))) return rule.outcome;
  }
  return 'passed';
}

/** Plain words for a decision reason ("contradiction:critical" → "Contradicts your documents"). */
export function reasonWords(reason: string): string | null {
  for (const r of outcomesDoc.reasons) {
    const pattern = r.match.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*');
    if (new RegExp(`^${pattern}$`).test(reason)) return r.copy;
  }
  return null;
}

/** The refund example of the hero card: the scenario pack's refund-contradiction-flag. */
export const REFUND = (() => {
  const s = refundScenario;
  if (!s.judge) throw new Error('contracts/demo/scenarios.v1.json lost the refund example\'s judge claims');
  const system = s.request.messages.find((m) => m.role === 'system')?.content ?? '';
  const doc = /<document id="([^"]+)">([\s\S]*?)<\/document>/.exec(system);
  const claimed = s.judge.claims[0];
  const evidence = claimed.evidence[0];
  const docText = doc?.[2] ?? evidence;
  // The number the evidence gives, found the same way the detector would read it.
  const evidenceNumber = /\d+\s+days/.exec(evidence)?.[0] ?? '';
  return {
    id: s.id,
    route: s.route,
    model: s.request.model,
    question: s.request.messages.find((m) => m.role === 'user')?.content ?? '',
    answer: s.response,
    quote: claimed.quote,
    docId: doc?.[1] ?? 'policy',
    docText,
    evidence,
    evidenceNumber,
    reason: reasonWords(s.expect.reasons[0]) ?? 'Contradicts your documents',
  };
})();
