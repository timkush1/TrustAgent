/**
 * Words and examples the landing page takes from the contracts, so the page shows the same refund
 * example, outcome words, commands and limits as the product (SPEC §8.1).
 */
import commandsDoc from '@contracts/ui/commands.v1.json';
import type { copy as contractCopy } from '@contracts/ui/copy.v1.json';
import outcomesDoc from '@contracts/ui/outcomes.v1.json';
import commitments from '@contracts/evaluators/commitments.v1.json';
import examplePolicy from '@contracts/policies/example.policies.yaml?raw';
// Only this scenario, not the whole pack (vite.site.ts scenarioModules): it keeps the landing small.
import refundScenario from 'virtual:trustagent/scenario/refund-contradiction-flag';
// Only the words the page shows, not the whole tree (vite.site.ts copyModules): the dashboard's
// copy grows every phase and would eat the landing's 110 KB budget. Keep the list and LandingCopy
// in step; landing.test.tsx checks them against the contract.
import { copy as landingCopy } from 'virtual:trustagent/copy/product,tiers,value_labels,limits,playground_static,savings,setup.step_connect.privacy';

type ContractCopy = typeof contractCopy;
/** The parts of contracts/ui/copy.v1.json the landing page reads. */
export type LandingCopy = Pick<ContractCopy, 'product' | 'tiers' | 'value_labels' | 'limits' | 'playground_static' | 'savings'> & {
  setup: { step_connect: Pick<ContractCopy['setup']['step_connect'], 'privacy'> };
};
export const copy = landingCopy as LandingCopy;

const base = import.meta.env.BASE_URL;
export const APP_MODE: 'full' | 'static' = import.meta.env.VITE_APP_MODE === 'static' ? 'static' : 'full';
/**
 * "Try the demo": the replay's Traffic page on the static site (a real file there, see
 * vite.site.ts STATIC_APP_PAGES), the dashboard next to /welcome in the full build.
 */
export const DEMO_HREF = APP_MODE === 'static' ? `${base}app/traffic/` : `${base}app`;
export const REPO_URL: string = (import.meta.env.VITE_REPO_URL as string | undefined) || 'https://github.com/timkush1/TrustAgent';
export const RECORDING_URL = `${base}demo/recorded/manifest.json`;

export function command(id: string): string {
  const c = commandsDoc.commands.find((x) => x.id === id);
  if (!c) throw new Error(`contracts/ui/commands.v1.json has no command ${id}`);
  return c.command;
}

export function outcome(id: string): { label: string; tone: string; headline: string } {
  const o = outcomesDoc.outcomes.find((x) => x.id === id);
  if (!o) throw new Error(`contracts/ui/outcomes.v1.json has no outcome ${id}`);
  return o;
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

/** The built-in "Unapproved commitments" check: what a plain-English rule becomes. */
export const COMMITMENTS = {
  ref: `${commitments.id}@${commitments.version}`,
  /** The allowed_commitments the example policy gives support-bot. */
  exampleAllowed: /allowed_commitments:\s*"([^"]+)"/.exec(examplePolicy)?.[1] ?? commitments.params.allowed_commitments.default,
  name: commitments.name,
  instructions: commitments.question.instructions,
  options: Object.entries(commitments.question.criteria).map(([key, c]) => ({ key, what: c.what, risky: key in commitments.signal.risk_weights })),
};
