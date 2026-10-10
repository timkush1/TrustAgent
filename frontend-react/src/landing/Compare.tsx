/**
 * Section 6 (SPEC §8.1): how it compares, and honest limits. Every other product's cell comes from
 * contracts/claims/compare.v1.json (D113): what that vendor's own public documentation says, with
 * the link and the date it was read, or "Not yet verified". Nothing is guessed or ranked, and the
 * owner reviews every cell before launch. TrustAgent's own row reads the value claims. The five
 * limits are the contract's words (copy.limits).
 *
 * Wide screens get a table; a phone gets one card per kind of tool (DESIGN.md: no table wider than
 * the page).
 */
import { ArrowUpRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { claim } from './claims';
import { compareColumns, compareRows, sourceHost, type CompareCell } from './compareData';
import { sectionCopy } from './sectionCopy';
import { ClaimValue, Section } from './ui';

const VALUE_WORD: Partial<Record<CompareCell['value'], string>> = { yes: 'Yes', no: 'No', partial: 'Partly' };

const COUNT_WORDS = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'];
/** "four kinds of tools": the number of rows the contract has, never a fixed word. */
function kindsOfTools(n: number): string {
  return `${COUNT_WORDS[n] ?? String(n)} ${n === 1 ? 'kind' : 'kinds'} of tools`;
}

function VendorCell({ cell }: { cell: CompareCell }) {
  if (cell.value === 'unverified' || !cell.source_url || !cell.as_of) {
    return (
      <span data-compare-cell="unverified" className="text-fg-muted">
        {sectionCopy.compare.not_verified}
      </span>
    );
  }
  const word = VALUE_WORD[cell.value];
  const host = sourceHost(cell.source_url);
  return (
    <div data-compare-cell={cell.value}>
      <p className="text-fg-muted">
        {word ? <span className="font-medium text-fg">{word}. </span> : null}
        {cell.text}
      </p>
      <p className="mt-1.5 text-xs text-fg-subtle">
        <a
          href={cell.source_url}
          rel="noreferrer"
          aria-label={`Source: ${host}, read ${cell.as_of}`}
          className="inline-flex items-center gap-0.5 text-fg-muted underline underline-offset-2 hover:text-fg"
        >
          {host}
          <ArrowUpRight aria-hidden="true" className="size-3" />
        </a>{' '}
        · <time dateTime={cell.as_of}>{cell.as_of}</time>
      </p>
    </div>
  );
}

function useOurs(): Record<string, ReactNode> {
  const fastCheck = claim('fast_check_cost_per_1k');
  return {
    inline: 'Yes: a proxy your SDK already speaks to',
    self_hosted: 'Yes, MIT',
    cost_class: (
      <span className="inline-flex flex-wrap items-baseline gap-x-1" data-chip-scope>
        fast check <ClaimValue claim={fastCheck} strong={false} />
      </span>
    ),
    explanations: 'Claim by claim, from the deep audit',
    calibration: 'Thresholds fitted to your own labels, per model version',
  };
}

function CompareTable({ ours }: { ours: Record<string, ReactNode> }) {
  return (
    // Wide screens only; a phone gets the cards. Still a named, focusable scroll region, so a
    // keyboard can scroll it sideways between 768 and 1024 px.
    // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
    <div className="overflow-x-auto rounded-lg border border-border max-md:hidden" tabIndex={0} role="region" aria-label="Comparison table (scrolls sideways)">
      <table className="w-full min-w-[60rem] border-collapse text-left text-sm">
        <caption className="sr-only">
          TrustAgent compared with {kindsOfTools(compareRows.length)}. Each other cell says what that vendor’s documentation says, with its link and the
          date it was read.
        </caption>
        <thead>
          <tr className="border-b border-border bg-surface-sunken">
            <th scope="col" className="w-56 min-w-44 px-4 py-3 font-medium text-fg-muted">
              <span className="sr-only">Tool</span>
            </th>
            {compareColumns.map((c) => (
              <th key={c.id} scope="col" className="px-4 py-3 align-bottom font-medium text-fg">
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          <tr className="border-b border-border bg-surface">
            <th scope="row" className="px-4 py-3 align-top font-semibold text-fg">
              TrustAgent
            </th>
            {compareColumns.map((c) => (
              <td key={c.id} className="px-4 py-3 align-top text-fg">
                {ours[c.id]}
              </td>
            ))}
          </tr>
          {compareRows.map((r) => (
            <tr key={r.id} className="border-b border-border last:border-b-0">
              <th scope="row" className="px-4 py-3 align-top font-normal">
                <span className="block font-medium text-fg">{r.name}</span>
                <span className="text-xs text-fg-muted">{r.examples}</span>
              </th>
              {compareColumns.map((c) => (
                <td key={c.id} className="px-4 py-3 align-top">
                  <VendorCell cell={r.cells[c.id]} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Below 768 px: one card per row, the five columns as a list. */
function CompareCards({ ours }: { ours: Record<string, ReactNode> }) {
  const cards = [{ id: 'trustagent', name: 'TrustAgent', examples: null as string | null, render: (id: string) => ours[id] }].concat(
    compareRows.map((r) => ({ id: r.id, name: r.name, examples: r.examples, render: (id: string) => <VendorCell cell={r.cells[id]} /> })),
  );
  return (
    <ul className="space-y-4 md:hidden" aria-label="Comparison">
      {cards.map((card) => (
        <li key={card.id} className={`rounded-lg border p-4 ${card.examples === null ? 'border-fg bg-surface' : 'border-border'}`}>
          <p className="font-semibold text-fg">{card.name}</p>
          {card.examples ? <p className="text-xs text-fg-muted">{card.examples}</p> : null}
          <dl className="mt-3 space-y-3 text-sm">
            {compareColumns.map((c) => (
              <div key={c.id}>
                <dt className="text-xs font-medium text-fg-subtle">{c.label}</dt>
                <dd className="mt-0.5 text-fg">{card.render(c.id)}</dd>
              </div>
            ))}
          </dl>
        </li>
      ))}
    </ul>
  );
}

export function Compare() {
  const ours = useOurs();
  return (
    <Section
      id="compare"
      eyebrow="How it compares, and honest limits"
      title="Where it fits, and where it doesn’t."
      lead="A per-answer gate you run yourself, for teams that already send documents with their prompts."
    >
      <CompareTable ours={ours} />
      <CompareCards ours={ours} />
      <p className="mt-3 text-xs text-fg-muted">
        Each cell says what that vendor’s own public documentation said on the date shown, with a link; it is not a ranking. Where the docs are silent the
        cell says so.
      </p>

      <div className="mt-16 grid grid-cols-1 gap-10 lg:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)]">
        <div>
          <h3 className="text-2xl font-semibold tracking-tight text-fg">Honest limits</h3>
          <p className="mt-2 text-fg-muted">What the fast check is not, today.</p>
        </div>
        <ol className="divide-y divide-border border-y border-border">
          {sectionCopy.limits.map((limit, i) => (
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
