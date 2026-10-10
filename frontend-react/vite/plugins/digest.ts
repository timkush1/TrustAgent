/**
 * `import { digest } from 'virtual:trustagent/tape-digest'`: the landing's tape card, built at
 * build time from the chosen dataset's manifest (kind, label, date, snapshot, judge, outcome counts
 * and five examples), so the landing never fetches the manifest (LD-5). The dataset is the one
 * vite/plugins/dataset.ts copies (the same VITE_DEMO_DATASET rule), and outcomes follow the rules of
 * contracts/ui/outcomes.v1.json, as the dashboard decides them. Owned by WEB-LANDING; the module
 * type is declared in src/env.d.ts.
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Plugin } from 'vite';
import { chooseDataset } from './dataset.ts';

export const DIGEST_MODULE = 'virtual:trustagent/tape-digest';

/** The number of example requests the card lists. */
export const DIGEST_EXAMPLES = 5;

/** The trace fields the card reads (src/env.d.ts TapeDigestTrace). */
export interface DigestTrace {
  trace_id: string;
  created_at: string;
  gate_status: string;
  action: string | null;
  would_action: string | null;
  answer_source: string | null;
  question_preview: string | null;
  response_preview: string | null;
}

/** src/env.d.ts TapeDigest. */
export interface TapeDigest {
  kind: 'mock' | 'recorded';
  label: string;
  recorded_at: string;
  snapshot: string | null;
  judge: string | null;
  outcomes: Record<string, number>;
  examples: DigestTrace[];
}

interface ManifestTrace extends DigestTrace {
  verdict?: string | null;
  audit?: { verdict?: string | null } | null;
}

interface Manifest {
  kind: unknown;
  label: unknown;
  recorded_at: unknown;
  decision_model?: { snapshot?: string | null } | null;
  judge?: { provider?: string; model?: string } | null;
  traces: unknown;
}

export interface OutcomeRules {
  outcomes: { id: string }[];
  rules: { when: Record<string, string[]>; outcome: string }[];
}

/** The first rule of outcomes.v1.json a trace matches (the dashboard's outcomeOf). */
export function outcomeOf(t: Pick<DigestTrace, 'gate_status' | 'action' | 'would_action'>, rules: OutcomeRules['rules']): string {
  const fields: Record<string, string | null> = { gate_status: t.gate_status, action: t.action, would_action: t.would_action };
  for (const rule of rules) {
    if (Object.entries(rule.when).every(([k, list]) => list.includes(fields[k] ?? ''))) return rule.outcome;
  }
  return 'passed';
}

const CAUGHT = new Set(['flag', 'block', 'fallback']);

/** The replay's featured trace (replayDataSource.ts featuredTrace): the first caught answer the audit found contradicted. */
function featured(traces: readonly ManifestTrace[]): ManifestTrace | undefined {
  const caught = traces.filter((t) => t.action !== null && CAUGHT.has(t.action));
  return caught.find((t) => t.verdict === 'contradicted' || t.audit?.verdict === 'HALLUCINATED') ?? caught[0];
}

/**
 * Which outcomes the examples show first when the tape has more than five: the featured trace's
 * outcome, a pass, then what the gateway does with a risky answer, then the rest in contract order.
 */
const PREFERRED = ['passed', 'blocked', 'not_checked', 'replaced', 'would_block', 'would_flag', 'error'];

const pick = (t: ManifestTrace): DigestTrace => ({
  trace_id: t.trace_id,
  created_at: t.created_at,
  gate_status: t.gate_status,
  action: t.action,
  would_action: t.would_action,
  answer_source: t.answer_source,
  question_preview: t.question_preview,
  response_preview: t.response_preview,
});

/**
 * The digest of a recording manifest. Examples: one distinct request (same question and answer) per
 * outcome, the featured trace for its own outcome and otherwise the first the tape showed, at most
 * five, oldest first. A request the gateway is still checking is never an example.
 */
export function buildDigest(manifest: Manifest, rules: OutcomeRules): TapeDigest {
  if (manifest.kind !== 'mock' && manifest.kind !== 'recorded') throw new Error(`the recording manifest has kind ${JSON.stringify(manifest.kind)}`);
  if (typeof manifest.label !== 'string' || typeof manifest.recorded_at !== 'string' || !Array.isArray(manifest.traces)) {
    throw new Error('the recording manifest has no label, recorded_at or traces');
  }
  const traces = [...(manifest.traces as ManifestTrace[])].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
  const counts: Record<string, number> = {};
  const firstByOutcome = new Map<string, ManifestTrace>();
  for (const t of traces) {
    const o = outcomeOf(t, rules.rules);
    counts[o] = (counts[o] ?? 0) + 1;
    if (o !== 'checking' && !firstByOutcome.has(o)) firstByOutcome.set(o, t);
  }
  const star = featured(traces);
  if (star) firstByOutcome.set(outcomeOf(star, rules.rules), star);

  const contractOrder = rules.outcomes.map((o) => o.id);
  const starOutcome = star ? outcomeOf(star, rules.rules) : null;
  const order = [...new Set([...(starOutcome ? [starOutcome] : []), ...PREFERRED, ...contractOrder])];
  const chosen = order
    .filter((o) => firstByOutcome.has(o))
    .slice(0, DIGEST_EXAMPLES)
    .map((o) => firstByOutcome.get(o) as ManifestTrace)
    .sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));

  const judge = manifest.judge?.provider && manifest.judge.model ? `${manifest.judge.provider}/${manifest.judge.model}` : null;
  return {
    kind: manifest.kind,
    label: manifest.label,
    recorded_at: manifest.recorded_at,
    snapshot: manifest.decision_model?.snapshot ?? null,
    judge,
    outcomes: counts,
    examples: chosen.map(pick),
  };
}

const here = (path: string) => fileURLToPath(new URL(path, import.meta.url));

export interface DigestOptions {
  /** frontend-react/ (the dataset lives in its demo-data/). */
  webDir?: string;
  /** contracts/ (for ui/outcomes.v1.json). */
  contractsDir?: string;
  /** VITE_DEMO_DATASET. */
  dataset?: string | undefined;
}

export function digestPlugin(options: DigestOptions = {}): Plugin {
  const webDir = options.webDir ?? here('../../');
  const contractsDir = options.contractsDir ?? here('../../../contracts/');
  const dataset = 'dataset' in options ? options.dataset : process.env.VITE_DEMO_DATASET;
  const resolved = `\0${DIGEST_MODULE}`;
  return {
    name: 'trustagent:tape-digest',
    resolveId(source) {
      return source === DIGEST_MODULE ? resolved : undefined;
    },
    load(id) {
      if (id !== resolved) return undefined;
      const outcomesFile = join(contractsDir, 'ui/outcomes.v1.json');
      let digest: TapeDigest;
      try {
        const { dir } = chooseDataset(join(webDir, 'demo-data'), dataset);
        const manifestFile = join(dir, 'manifest.json');
        this.addWatchFile(manifestFile);
        this.addWatchFile(outcomesFile);
        digest = buildDigest(JSON.parse(readFileSync(manifestFile, 'utf8')) as Manifest, JSON.parse(readFileSync(outcomesFile, 'utf8')) as OutcomeRules);
      } catch (err) {
        this.error(`${DIGEST_MODULE}: ${err instanceof Error ? err.message : String(err)}`);
      }
      return `export const digest = ${JSON.stringify(digest)};\n`;
    },
  };
}
