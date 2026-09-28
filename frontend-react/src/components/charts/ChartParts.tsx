/**
 * The pieces every chart shares: the legend, the table view and the hover readout. They are plain
 * HTML in the text tokens; only the small keys beside the words carry a series color.
 */
import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface LegendItem {
  key: string;
  label: string;
  color: string;
  /** Mirror the mark: a square for bars and areas, a short line for lines. */
  mark?: 'square' | 'line';
}

/** Always present for two or more series (never color-matching alone). */
export function ChartLegend({ items, className }: { items: readonly LegendItem[]; className?: string }) {
  return (
    <ul aria-label="Legend" className={cn('flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-muted', className)}>
      {items.map((item) => (
        <li key={item.key} data-series={item.key} className="inline-flex items-center gap-1.5">
          <span
            aria-hidden="true"
            className={item.mark === 'line' ? 'h-0.5 w-3 rounded-full' : 'size-2.5 rounded-[2px]'}
            style={{ backgroundColor: item.color }}
          />
          {item.label}
        </li>
      ))}
    </ul>
  );
}

export interface ChartTableColumn {
  key: string;
  label: string;
  numeric?: boolean;
}

export interface ChartTableProps {
  /** Says what the table holds ("Requests by outcome, per hour"). */
  caption: string;
  columns: readonly ChartTableColumn[];
  /** One object per row keyed by column; the first column is the row header. */
  rows: readonly Record<string, ReactNode>[];
  className?: string;
}

/** The table view of a chart: the same numbers, readable without a pointer or color. */
export function ChartTable({ caption, columns, rows, className }: ChartTableProps) {
  const captionId = useId();
  return (
    <div
      // The table can be taller or wider than its box; keyboard users must be able to scroll it.
      // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
      tabIndex={0}
      role="region"
      aria-labelledby={captionId}
      className={cn('max-h-80 overflow-auto rounded-md border border-border focus-visible:-outline-offset-2', className)}
    >
      <table className="w-full border-collapse text-xs">
        <caption id={captionId} className="sr-only">
          {caption}
        </caption>
        <thead className="sticky top-0 bg-surface">
          <tr className="border-b border-border">
            {columns.map((c) => (
              <th
                key={c.key}
                scope="col"
                className={cn('px-2.5 py-1.5 font-medium whitespace-nowrap text-fg-muted', c.numeric ? 'text-right' : 'text-left')}
              >
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, ri) => (
            <tr key={ri} className="border-b border-border last:border-b-0">
              {columns.map((c, ci) => {
                const Cell = ci === 0 ? 'th' : 'td';
                return (
                  <Cell
                    key={c.key}
                    scope={ci === 0 ? 'row' : undefined}
                    className={cn(
                      'px-2.5 py-1.5 whitespace-nowrap',
                      ci === 0 ? 'text-left font-normal text-fg-muted' : 'text-fg',
                      c.numeric && 'text-right tabular',
                    )}
                  >
                    {row[c.key]}
                  </Cell>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export interface TooltipRow {
  key: string;
  label: string;
  value: string;
  color: string;
  /** Dim a row whose value is zero. */
  muted?: boolean;
}

/** The hover readout: values lead in full ink, series names follow, each keyed by a short line. */
export function ChartTooltipBox({ title, rows, footer }: { title: ReactNode; rows: readonly TooltipRow[]; footer?: ReactNode }) {
  return (
    <div className="min-w-36 rounded-md border border-border bg-surface-raised px-2.5 py-2 text-xs text-fg shadow-popup">
      <p className="mb-1 text-fg-muted">{title}</p>
      <ul className="space-y-0.5">
        {rows.map((r) => (
          <li key={r.key} className={cn('flex items-center gap-2', r.muted && 'opacity-60')}>
            <span aria-hidden="true" className="h-0.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: r.color }} />
            <span className="font-medium tabular">{r.value}</span>
            <span className="text-fg-muted">{r.label}</span>
          </li>
        ))}
      </ul>
      {footer ? <div className="mt-1 border-t border-border pt-1 text-fg-muted">{footer}</div> : null}
    </div>
  );
}
