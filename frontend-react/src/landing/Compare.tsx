/**
 * Section 6 (SPEC §8.1): how it compares, and honest limits. The comparison table is a skeleton:
 * every other product's cell is a dated TODO until it is verified against that vendor's own docs,
 * with a link (P6). Nothing is guessed. The five limits are the contract's words (copy.limits).
 */
import { claim } from './claims';
import { copy } from './content';
import { ClaimValue, Section } from './ui';

/** The month the table's cells are dated to; P6 re-verifies every cell. */
export const COMPARE_AS_OF = '2026-09';

const columns = ['Runs inline in your gateway', 'Self-hosted and open source', 'Per-answer cost class', 'Explanations', 'Calibration workflow'] as const;

const rows: { name: string; examples: string }[] = [
  { name: 'LLM-as-judge in observability tools', examples: 'Langfuse, Arize, Datadog' },
  { name: 'Guardrail frameworks', examples: 'Guardrails AI, NeMo Guardrails' },
  { name: 'Cloud groundedness APIs', examples: 'Bedrock contextual grounding, Azure groundedness detection' },
  { name: 'Small-model evaluators', examples: 'Galileo Luna' },
];

function Todo() {
  return (
    <span className="inline-flex items-center rounded-sm border border-dashed border-border-strong px-1.5 py-0.5 text-2xs font-medium whitespace-nowrap text-fg-muted" data-compare-todo>
      TODO: verify · {COMPARE_AS_OF}
    </span>
  );
}

export function Compare() {
  const fastCheck = claim('fast_check_cost_per_1k');
  const ours: Record<(typeof columns)[number], React.ReactNode> = {
    'Runs inline in your gateway': 'Yes: a proxy your SDK already speaks to',
    'Self-hosted and open source': 'Yes, MIT',
    'Per-answer cost class': (
      <span className="inline-flex flex-wrap items-baseline gap-x-1">
        fast check <ClaimValue claim={fastCheck} strong={false} />
      </span>
    ),
    Explanations: 'Claim by claim, from the deep audit',
    'Calibration workflow': 'Thresholds fitted to your own labels, per model version',
  };
  return (
    <Section
      id="compare"
      eyebrow="How it compares, and honest limits"
      title="Where it fits, and where it doesn’t."
      lead="A per-answer gate you run yourself, for teams that already send documents with their prompts."
    >
      {/* A focusable, named scroll region: keyboard users can scroll the table sideways on a phone. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
      <div className="overflow-x-auto rounded-lg border border-border" tabIndex={0} role="region" aria-label="Comparison table (scrolls sideways)">
        <table className="w-full min-w-[56rem] border-collapse text-left text-sm">
          <caption className="sr-only">TrustAgent compared with four kinds of tools. Cells marked TODO are not yet verified.</caption>
          <thead>
            <tr className="border-b border-border bg-surface-sunken">
              <th scope="col" className="w-64 px-4 py-3 font-medium text-fg-muted">
                <span className="sr-only">Tool</span>
              </th>
              {columns.map((c) => (
                <th key={c} scope="col" className="px-4 py-3 font-medium text-fg">
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            <tr className="border-b border-border bg-surface">
              <th scope="row" className="px-4 py-3 align-top font-semibold text-fg">
                TrustAgent
              </th>
              {columns.map((c) => (
                <td key={c} className="px-4 py-3 align-top text-fg">
                  {ours[c]}
                </td>
              ))}
            </tr>
            {rows.map((r) => (
              <tr key={r.name} className="border-b border-border last:border-b-0">
                <th scope="row" className="px-4 py-3 align-top font-normal">
                  <span className="block font-medium text-fg">{r.name}</span>
                  <span className="text-xs text-fg-muted">{r.examples}</span>
                </th>
                {columns.map((c) => (
                  <td key={c} className="px-4 py-3 align-top">
                    <Todo />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-fg-muted">
        Other products’ cells stay blank until each is checked against that vendor’s own documentation, dated and linked. We would
        rather show a gap than a guess.
      </p>

      <div className="mt-16 grid grid-cols-1 gap-10 lg:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)]">
        <div>
          <h3 className="text-2xl font-semibold tracking-tight text-fg">Honest limits</h3>
          <p className="mt-2 text-fg-muted">What the fast check is not, today.</p>
        </div>
        <ol className="divide-y divide-border border-y border-border">
          {copy.limits.map((limit, i) => (
            <li key={limit} className="flex gap-4 py-4">
              <span className="font-mono text-sm text-fg-subtle">0{i + 1}</span>
              <span className="text-fg">{limit}</span>
            </li>
          ))}
        </ol>
      </div>
    </Section>
  );
}
