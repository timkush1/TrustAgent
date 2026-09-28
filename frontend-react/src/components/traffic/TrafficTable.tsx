/**
 * The Traffic list (SPEC §3.3): a virtualized table over the window scroll, one row per request
 * with its outcome pill, time, route, model, headline reason, what it was checked against,
 * latency and deep-audit state. The whole row opens the request (a stretched link in the first
 * cell, so keyboard and screen-reader users get a real link).
 *
 * Columns drop out as the table narrows (container queries): outcome, time and reason always
 * show; route and deep audit from 42rem; checked against and latency from 56rem; model from 64rem.
 */
import { Link } from '@tanstack/react-router';
import { useWindowVirtualizer } from '@tanstack/react-virtual';
import { useCallback, useEffect, useState } from 'react';
import { AuditState } from '@/components/outcome/AuditState';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { useNow } from '@/components/common/useNow';
import { copy, traceOutcome } from '@/domain/presentation';
import { formatDateTime, formatDurationMs, formatRelativeTime } from '@/domain/format';
import { cn } from '@/lib/cn';
import type { GateDecisionView, TraceId, TraceSummary } from '@/types/api';
import { rowCheckedAgainst, rowReason } from './trafficRows';

export const ROW_HEIGHT = 48;
/** Start loading the next page this many rows before the end. */
const PREFETCH_ROWS = 10;

const GRID = cn(
  'grid items-center gap-x-3 px-3',
  'grid-cols-[7.5rem_4.5rem_minmax(0,1fr)]',
  '@2xl:grid-cols-[7.5rem_4.5rem_minmax(6rem,1fr)_minmax(10rem,2fr)_9.5rem]',
  '@4xl:grid-cols-[7.5rem_4.5rem_minmax(6rem,1fr)_minmax(10rem,2fr)_minmax(7rem,1.2fr)_4.5rem_9.5rem]',
  '@5xl:grid-cols-[7.5rem_4.5rem_minmax(6rem,1fr)_minmax(6rem,1fr)_minmax(10rem,2fr)_minmax(7rem,1.2fr)_4.5rem_9.5rem]',
);

/** Cell visibility per column, in DOM order (must match GRID at every width). */
const COLUMNS = [
  { id: 'outcome', label: 'Outcome', className: 'flex' },
  { id: 'time', label: 'Time', className: 'flex' },
  { id: 'route', label: 'Route', className: 'hidden @2xl:flex' },
  { id: 'model', label: 'Model', className: 'hidden @5xl:flex' },
  { id: 'reason', label: 'Reason', className: 'flex' },
  { id: 'checked', label: 'Checked against', className: 'hidden @4xl:flex' },
  { id: 'latency', label: 'Latency', className: 'hidden @4xl:flex justify-end' },
  { id: 'audit', label: copy.tiers.deep_audit, className: 'hidden @2xl:flex' },
] as const;

type ColumnId = (typeof COLUMNS)[number]['id'];
const col = (id: ColumnId) => COLUMNS.find((c) => c.id === id)?.className ?? '';

const EM_DASH = '—';

export interface TrafficTableProps {
  rows: readonly TraceSummary[];
  decisions: Readonly<Record<TraceId, GateDecisionView>>;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  onLoadMore: () => void;
}

export function TrafficTable({ rows, decisions, hasNextPage, isFetchingNextPage, onLoadMore }: TrafficTableProps) {
  const now = useNow();
  // Distance from the top of the document, measured once; overscan absorbs a banner that
  // appears later.
  const [scrollMargin, setScrollMargin] = useState(0);
  const bodyRef = useCallback((el: HTMLDivElement | null) => {
    if (el) setScrollMargin(el.getBoundingClientRect().top + window.scrollY);
  }, []);

  const virtualizer = useWindowVirtualizer({
    count: rows.length,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
    scrollMargin,
    getItemKey: (i) => rows[i].trace_id,
  });
  const items = virtualizer.getVirtualItems();
  const lastIndex = items.length > 0 ? items[items.length - 1].index : -1;

  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && lastIndex >= rows.length - PREFETCH_ROWS) onLoadMore();
  }, [hasNextPage, isFetchingNextPage, lastIndex, rows.length, onLoadMore]);

  return (
    <div className="@container">
      <div
        role="table"
        aria-label="Requests"
        aria-rowcount={hasNextPage ? -1 : rows.length + 1}
        className="rounded-lg border border-border bg-surface"
      >
        <div role="rowgroup">
          <div role="row" aria-rowindex={1} className={cn(GRID, 'h-9 border-b border-border text-xs font-medium text-fg-muted')}>
            {COLUMNS.map((c) => (
              <div key={c.id} role="columnheader" className={cn(c.className, 'min-w-0 items-center')}>
                {c.label}
              </div>
            ))}
          </div>
        </div>
        <div role="rowgroup" ref={bodyRef} className="relative" style={{ height: virtualizer.getTotalSize() }}>
          {items.map((item) => {
            const t = rows[item.index];
            return (
              <TrafficRow
                key={item.key}
                trace={t}
                decision={decisions[t.trace_id]}
                index={item.index}
                top={item.start - scrollMargin}
                now={now}
              />
            );
          })}
        </div>
        {isFetchingNextPage ? (
          <p role="status" className="border-t border-border px-3 py-2 text-xs text-fg-muted">
            {copy.states.loading}
          </p>
        ) : null}
      </div>
    </div>
  );
}

function TrafficRow({
  trace: t,
  decision,
  index,
  top,
  now,
}: {
  trace: TraceSummary;
  decision: GateDecisionView | undefined;
  index: number;
  top: number;
  now: number;
}) {
  const outcome = traceOutcome({ ...t, degraded: decision?.degraded ?? null });
  // `now` ticks every 15 s: a request newer than the last tick (or a server clock ahead of this
  // one) reads "just now", never "in 8 sec.".
  const created = Date.parse(t.created_at);
  const since = Number.isNaN(created) ? now : Math.max(now, created);
  const reason = rowReason(t, decision);
  const checked = rowCheckedAgainst(t, decision);
  return (
    <div
      role="row"
      aria-rowindex={index + 2}
      data-trace-id={t.trace_id}
      data-outcome={outcome.id}
      style={{ height: ROW_HEIGHT, transform: `translateY(${top}px)` }}
      className={cn(
        GRID,
        'absolute inset-x-0 top-0 border-b border-border text-sm last:border-b-0 hover:bg-surface-hover',
        'has-[a:focus-visible]:outline-2 has-[a:focus-visible]:-outline-offset-2 has-[a:focus-visible]:outline-fg',
      )}
    >
      <div role="cell" className={cn(col('outcome'), 'min-w-0 items-center')}>
        <Link
          to="/app/traffic/$traceId"
          params={{ traceId: t.trace_id }}
          className="min-w-0 rounded-full outline-none after:absolute after:inset-0"
        >
          <OutcomePill outcome={outcome} />
          <span className="sr-only">, open request {t.trace_id}</span>
        </Link>
      </div>
      <div role="cell" className={cn(col('time'), 'min-w-0 text-xs text-fg-muted tabular')}>
        <time dateTime={t.created_at} title={formatDateTime(t.created_at)} className="truncate">
          {formatRelativeTime(t.created_at, since)}
        </time>
      </div>
      <div role="cell" className={cn(col('route'), 'min-w-0')}>
        <span className="truncate">{t.route}</span>
      </div>
      <div role="cell" className={cn(col('model'), 'min-w-0 text-fg-muted')}>
        <span className="truncate">{t.model ?? EM_DASH}</span>
      </div>
      <div role="cell" className={cn(col('reason'), 'min-w-0', reason ? 'text-fg' : 'text-fg-subtle')}>
        <span className="truncate">{reason ?? EM_DASH}</span>
      </div>
      <div role="cell" className={cn(col('checked'), 'min-w-0', checked && !checked.none ? 'text-fg' : 'text-fg-muted')}>
        <span className="truncate">{checked?.text ?? EM_DASH}</span>
      </div>
      <div role="cell" className={cn(col('latency'), 'min-w-0 text-fg-muted tabular')}>
        {formatDurationMs(t.timings.total_ms)}
      </div>
      <div role="cell" className={cn(col('audit'), 'min-w-0')}>
        <AuditState audit={t.audit} />
      </div>
    </div>
  );
}
