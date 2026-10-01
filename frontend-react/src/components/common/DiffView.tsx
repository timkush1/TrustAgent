/**
 * A line diff for review steps (save a policy, compare evaluator versions). Color means a verdict
 * or an outcome only (DESIGN.md), so the diff is achromatic: an added line carries a "+" sign, a
 * raised background and full-weight text; a removed line a "−" sign, muted struck-through text.
 * Screen readers hear "Added" / "Removed" before each changed line. Long unchanged runs fold into
 * a "Show N unchanged lines" button.
 */
import { ChevronsUpDown } from 'lucide-react';
import { useId, useMemo, useState } from 'react';
import { diffTexts, foldDiff, parseUnifiedDiff, summarizeDiff, type DiffLine, type DiffRow } from '@/domain/lineDiff';
import { formatCount } from '@/domain/format';
import { cn } from '@/lib/cn';

export type DiffViewProps = {
  /** Names the diff region ("Changes to policies.yaml"). */
  label: string;
  /** Lines of unchanged context around each change. */
  context?: number;
  maxHeight?: string;
  className?: string;
  /** Shown when the two texts are the same. */
  emptyText?: string;
} & ({ before: string; after: string; unified?: never } | { unified: string; before?: never; after?: never });

function Line({ line }: { line: DiffLine }) {
  const sign = line.kind === 'add' ? '+' : line.kind === 'remove' ? '−' : ' ';
  return (
    <div
      data-diff={line.kind}
      className={cn(
        'grid grid-cols-[2.5rem_2.5rem_1.25rem_minmax(0,1fr)] font-mono text-xs leading-5',
        line.kind === 'add' && 'bg-surface-hover font-medium text-fg',
        line.kind === 'remove' && 'text-fg-muted',
        line.kind === 'same' && 'text-fg-muted',
      )}
    >
      <span aria-hidden="true" className="pr-2 text-right text-fg-subtle tabular select-none">
        {line.oldNo ?? ''}
      </span>
      <span aria-hidden="true" className="pr-2 text-right text-fg-subtle tabular select-none">
        {line.newNo ?? ''}
      </span>
      <span aria-hidden="true" className="text-center select-none">
        {sign}
      </span>
      <span className={cn('pr-3 whitespace-pre-wrap break-words', line.kind === 'remove' && 'line-through decoration-fg-subtle')}>
        {line.kind === 'add' ? <span className="sr-only">Added:{' '}</span> : null}
        {line.kind === 'remove' ? <span className="sr-only">Removed:{' '}</span> : null}
        {line.text || ' '}
      </span>
    </div>
  );
}

function Fold({ rows }: { rows: Extract<DiffRow, { kind: 'fold' }> }) {
  const [open, setOpen] = useState(false);
  const noun = rows.hidden === 1 ? 'line' : 'lines';
  if (open) return rows.lines.map((l, i) => <Line key={i} line={l} />);
  if (rows.lines.length === 0) {
    return (
      <div className="border-y border-border bg-surface-sunken px-3 py-0.5 text-2xs text-fg-muted">
        {formatCount(rows.hidden)} unchanged {noun}
      </div>
    );
  }
  return (
    <button
      type="button"
      onClick={() => setOpen(true)}
      className="flex w-full items-center gap-1.5 border-y border-border bg-surface-sunken px-3 py-0.5 text-left text-2xs text-fg-muted hover:bg-surface-hover hover:text-fg"
    >
      <ChevronsUpDown aria-hidden="true" className="size-3" />
      Show {formatCount(rows.hidden)} unchanged {noun}
    </button>
  );
}

export function DiffView(props: DiffViewProps) {
  const { label, context = 3, maxHeight = '24rem', className, emptyText = 'No changes.' } = props;
  const captionId = useId();
  const rows = useMemo(
    () =>
      props.unified !== undefined
        ? parseUnifiedDiff(props.unified)
        : foldDiff(diffTexts(props.before ?? '', props.after ?? ''), context),
    [props.unified, props.before, props.after, context],
  );
  const summary = summarizeDiff(rows);
  const unchanged = summary.added === 0 && summary.removed === 0;

  return (
    <figure className={cn('min-w-0 overflow-hidden rounded-lg border border-border bg-surface', className)}>
      <figcaption className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-1.5 text-xs">
        <span id={captionId} className="font-medium text-fg-muted">
          {label}
        </span>
        <span className="text-fg-muted tabular">
          {unchanged ? 'No changes' : `${formatCount(summary.added)} added · ${formatCount(summary.removed)} removed`}
        </span>
      </figcaption>
      {unchanged ? (
        <p className="px-3 py-3 text-sm text-fg-muted">{emptyText}</p>
      ) : (
        <div
          // A long diff scrolls; keyboard users must reach it (axe scrollable-region-focusable).
          // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
          tabIndex={0}
          role="region"
          aria-labelledby={captionId}
          style={{ maxHeight }}
          className="overflow-auto py-1 focus-visible:-outline-offset-2"
        >
          {rows.map((row, i) => (row.kind === 'fold' ? <Fold key={i} rows={row} /> : <Line key={i} line={row} />))}
        </div>
      )}
    </figure>
  );
}
