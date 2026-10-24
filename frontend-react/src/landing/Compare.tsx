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
import { ArrowUpRight, ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { claim } from './claims';
import { COMMON_READ_DATE, compareColumns, compareRows, sourceHost, type CompareCell } from './compareData';
import { sectionCopy } from './sectionCopy';
import { ClaimValue, Section } from './ui';

const VALUE_WORD: Record<Exclude<CompareCell['value'], 'unverified'>, string> = {
  yes: 'Yes',
  no: 'No',
  partial: 'Partly',
  varies: 'Varies',
  not_documented: 'Not documented',
};

const COUNT_WORDS = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'];
/** "four kinds of tools": the number of rows the contract has, never a fixed word. */
function kindsOfTools(n: number): string {
  return `${COUNT_WORDS[n] ?? String(n)} ${n === 1 ? 'kind' : 'kinds'} of tools`;
}

const ISO_DATE = /(\d{4}-\d{2}-\d{2})/;

/**
 * A cell's text with each date (2026-10-10) kept on one line: browsers break after a hyphen, and a
 * narrow table column otherwise shows "2026-" and "10-10" on two lines.
 */
function KeepDates({ text }: { text: string }) {
  return (
    <>
      {text.split(ISO_DATE).map((part, i) =>
        i % 2 === 1 ? (
          <span key={i} className="whitespace-nowrap">
            {part}
          </span>
        ) : (
          part
        ),
      )}
    </>
  );
}

/**
 * A vendor cell: the scannable answer (Yes, No, Partly, Varies, Not documented) and the product
 * whose docs it cites ("Yes · Langfuse"), so a row of several products never credits one vendor's
 * docs to another. One control, Source, opens what those docs say and the link. The date the docs
 * were read shows here only when the cells were read on different days; otherwise the note under
 * the table says it once.
 */
function VendorCell({ cell }: { cell: CompareCell }) {
  if (cell.value === 'unverified' || !cell.source_url || !cell.as_of || !cell.vendor) {
    return (
      <span data-compare-cell="unverified" className="text-fg-muted">
        {sectionCopy.compare.not_verified}
      </span>
    );
  }
  const host = sourceHost(cell.source_url);
  const ownDate = cell.as_of !== COMMON_READ_DATE;
  return (
    <div data-compare-cell={cell.value}>
      <p data-compare-answer>
        <span className="font-medium text-fg">{VALUE_WORD[cell.value]}</span>
        {/* A product name stays on one line ("Guardrails AI", not "Guardrails" and "AI"), with its
            separator: a narrow column breaks before "· NeMo Guardrails", never after "Varies ·". */}
        <span className="text-fg-muted">
          {' '}
          <span className="whitespace-nowrap" data-compare-vendor>
            {'·\u00a0'}
            {cell.vendor}
          </span>
        </span>
      </p>
      <details className="group mt-0.5">
        <summary className="inline-flex min-h-6 cursor-pointer list-none items-center gap-1 text-xs text-fg-muted underline-offset-2 marker:hidden hover:text-fg hover:underline">
          <ChevronRight aria-hidden="true" className="size-3 transition-transform group-open:rotate-90" />
          Source<span className="sr-only">: what the {cell.vendor} docs say</span>
        </summary>
        <p className="mt-1 text-fg-muted">
          <KeepDates text={cell.text} />
        </p>
        <p className="mt-1 text-xs text-fg-subtle">
          <a
            href={cell.source_url}
            rel="noreferrer"
            aria-label={`${cell.vendor} docs on ${host}, read ${cell.as_of}`}
            className="inline-flex min-h-6 items-center gap-0.5 text-fg-muted underline underline-offset-2 hover:text-fg"
          >
            {host}
            <ArrowUpRight aria-hidden="true" className="size-3" />
          </a>
          {ownDate ? ' · ' : null}
          <time dateTime={cell.as_of} className={ownDate ? 'whitespace-nowrap' : 'sr-only whitespace-nowrap'}>
            {cell.as_of}
          </time>
        </p>
      </details>
    </div>
  );
}

/** TrustAgent's own cells: the same answer first, then what stands behind it. */
function OurCell({ answer, detail }: { answer: ReactNode; detail?: ReactNode }) {
  return (
    <div data-compare-ours>
      <p className="font-medium text-fg">{answer}</p>
      {detail ? <p className="mt-0.5 text-sm text-fg-muted">{detail}</p> : null}
    </div>
  );
}

function useOurs(): Record<string, ReactNode> {
  const fastCheck = claim('fast_check_cost_per_1k');
  return {
    inline: <OurCell answer="Yes" detail="A proxy your SDK already speaks to" />,
    self_hosted: <OurCell answer="Yes" detail="MIT license" />,
    cost_class: <OurCell answer={<ClaimValue claim={fastCheck} />} detail="The fast check: every question in one call" />,
    explanations: <OurCell answer="Yes" detail="Claim by claim, from the deep audit" />,
    calibration: <OurCell answer="Yes" detail="Thresholds fitted to your own labels, per model version" />,
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
          {/* This site's own product: marked with a bar, never a verdict colour (DESIGN.md). */}
          <tr className="border-b border-border bg-surface" data-compare-row="trustagent">
            <th scope="row" className="border-l-4 border-l-fg px-4 py-3 align-top font-semibold text-fg">
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

/**
 * Below 768 px: TrustAgent's card, then the other kinds of tools behind one disclosure (a phone
 * would otherwise scroll past four long cards), each a card with the five columns as a list.
 */
function CompareCards({ ours }: { ours: Record<string, ReactNode> }) {
  const fields = (render: (id: string) => ReactNode) => (
    <dl className="mt-3 space-y-3 text-sm">
      {compareColumns.map((c) => (
        <div key={c.id}>
          <dt className="text-xs font-medium text-fg-subtle">{c.label}</dt>
          <dd className="mt-0.5 text-fg">{render(c.id)}</dd>
        </div>
      ))}
    </dl>
  );
  return (
    <div className="md:hidden">
      <div className="rounded-lg border border-l-4 border-fg bg-surface p-4" data-compare-row="trustagent">
        <p className="font-semibold text-fg">TrustAgent</p>
        {fields((id) => ours[id])}
      </div>
      <details className="group mt-4" data-compare-others>
        <summary className="flex min-h-11 cursor-pointer list-none items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface px-4 py-2 text-sm font-medium text-fg marker:hidden">
          <span>
            Compare with {kindsOfTools(compareRows.length)}
            <span className="block text-xs font-normal text-fg-muted">{compareRows.map((r) => r.examples).join(', ')}</span>
          </span>
          <ChevronRight aria-hidden="true" className="size-4 shrink-0 text-fg-muted transition-transform group-open:rotate-90" />
        </summary>
        <ul className="mt-4 space-y-4" aria-label="Comparison">
          {compareRows.map((r) => (
            <li key={r.id} className="rounded-lg border border-border p-4">
              <p className="font-semibold text-fg">{r.name}</p>
              <p className="text-xs text-fg-muted">{r.examples}</p>
              {fields((id) => <VendorCell cell={r.cells[id]} />)}
            </li>
          ))}
        </ul>
      </details>
    </div>
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
      <p className="mt-3 text-xs text-fg-muted" data-compare-note>
        Each cell names the product whose own public documentation it cites, as read
        {COMMON_READ_DATE ? (
          <>
            {' '}
            on <time dateTime={COMMON_READ_DATE} className="whitespace-nowrap">{COMMON_READ_DATE}</time>
          </>
        ) : (
          ' on the date shown'
        )}
        ; Source opens what the docs say and the link. It is not a ranking. Where the docs are silent, the cell says Not documented.
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
