/**
 * `import { examplePolicy, outcomes } from 'virtual:trustagent/landing-data'`: the landing-only
 * data, as small as the page needs it: contracts/policies/example.policies.yaml as one string
 * (instead of a `?raw` import) and the parts of contracts/ui/outcomes.v1.json the landing reads
 * (outcomes without next steps, reasons, rules). Owned by WEB-LANDING; the module type is declared
 * in src/env.d.ts.
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Plugin } from 'vite';

export const LANDING_DATA_MODULE = 'virtual:trustagent/landing-data';

interface OutcomesDoc {
  outcomes: { id: string; label: string; tone: string; headline: string }[];
  reasons: { match: string; copy: string }[];
  rules: { when: Record<string, string[]>; outcome: string }[];
}

/** The landing's subset of outcomes.v1 (only the fields env.d.ts declares). */
export function outcomesSubset(doc: OutcomesDoc): OutcomesDoc {
  return {
    outcomes: doc.outcomes.map(({ id, label, tone, headline }) => ({ id, label, tone, headline })),
    reasons: doc.reasons.map(({ match, copy }) => ({ match, copy })),
    rules: doc.rules.map(({ when, outcome }) => ({ when, outcome })),
  };
}

export function landingDataPlugin(contractsDir: string): Plugin {
  const policyFile = join(contractsDir, 'policies/example.policies.yaml');
  const outcomesFile = join(contractsDir, 'ui/outcomes.v1.json');
  const resolved = `\0${LANDING_DATA_MODULE}`;
  return {
    name: 'trustagent:landing-data',
    resolveId(source) {
      return source === LANDING_DATA_MODULE ? resolved : undefined;
    },
    load(id) {
      if (id !== resolved) return undefined;
      this.addWatchFile(policyFile);
      this.addWatchFile(outcomesFile);
      const examplePolicy = readFileSync(policyFile, 'utf8');
      const outcomes = outcomesSubset(JSON.parse(readFileSync(outcomesFile, 'utf8')) as OutcomesDoc);
      return `export const examplePolicy = ${JSON.stringify(examplePolicy)};\nexport const outcomes = ${JSON.stringify(outcomes)};\n`;
    },
  };
}
