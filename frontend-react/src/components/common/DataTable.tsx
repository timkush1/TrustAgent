/**
 * DataTable: the foundation for long lists (label queue, knowledge claims, keys, benchmark runs).
 * Rows are virtualized with @tanstack/react-virtual inside a scroll viewport, the header stays
 * put while the body scrolls, and the loading, error and empty states of the page contract take
 * the body's place under the header.
 *
 * Keyboard: the table is one tab stop. Arrow Up/Down move between rows, Page Up/Down by a screen,
 * Home/End to the first and last row, Enter or Space opens the row (`onRowActivate`). The focused
 * row is always kept rendered, so focus survives scrolling it out of view. Cells hold text and
 * pills, not their own buttons: a row does one thing, which `onRowActivate` performs.
 *
 * Semantics: an ARIA grid of rows (read-only), with `aria-rowcount` / `aria-rowindex` so screen
 * readers know where they are in the full list, not just the rendered slice.
 */
import { defaultRangeExtractor, useVirtualizer, type Range } from '@tanstack/react-virtual';
import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { EmptyState, ErrorState, LoadingState } from './StateViews';
import { useDelayedLoading } from './useDelayedLoading';

export interface DataTableColumn<Row> {
  id: string;
  header: ReactNode;
  cell: (row: Row, index: number) => ReactNode;
  /** CSS grid track: "7.5rem", "minmax(8rem,2fr)". Default "minmax(0,1fr)". */
  width?: string;
  /** Numbers: right-aligned, tabular figures. */
  numeric?: boolean;
  className?: string;
}

export interface DataTableProps<Row> {
  /** Names the table for screen readers ("Label queue"). */
  'aria-label': string;
  columns: readonly DataTableColumn<Row>[];
  rows: readonly Row[];
  getRowId: (row: Row, index: number) => string;
  /** Enter, Space or a click on a row. */
  onRowActivate?: (row: Row, index: number) => void;
  /** Fixed row height in px (default 40). */
  rowHeight?: number;
  /** Max height of the scroll viewport (CSS; default "min(70vh, 40rem)"). */
  maxHeight?: string;
  /** Rows that exist on the server; -1 when unknown (more pages). Defaults to rows.length. */
  totalRows?: number;
  /** The last rows came into view: load the next page. Called once per row count. */
  onEndReached?: () => void;
  /** First load in flight: shows the loading state after 200 ms (page contract). */
  isLoading?: boolean;
  /** A load failed: shows the server's message with Retry. */
  error?: unknown;
  onRetry?: () => void;
  /** Shown when there are no rows (default: a plain empty state). */
  empty?: ReactNode;
  /** A footer line under the rows ("Loading more…"). */
  footer?: ReactNode;
  /** Extra attributes for a row (`data-*` for tests and styling). */
  rowAttributes?: (row: Row, index: number) => Record<`data-${string}`, string | undefined>;
  overscan?: number;
  className?: string;
}

const HEADER_HEIGHT = 36;
const END_THRESHOLD = 5;

const INTERACTIVE = 'a[href],button,input,select,textarea,[role="button"],[role="link"],[contenteditable="true"]';

export function DataTable<Row>({
  'aria-label': ariaLabel,
  columns,
  rows,
  getRowId,
  onRowActivate,
  rowHeight = 40,
  maxHeight = 'min(70vh, 40rem)',
  totalRows,
  onEndReached,
  isLoading = false,
  error,
  onRetry,
  empty,
  footer,
  rowAttributes,
  overscan = 8,
  className,
}: DataTableProps<Row>) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const rowRefs = useRef(new Map<number, HTMLDivElement>());
  const [active, setActive] = useState(0);
  const [focusRequest, setFocusRequest] = useState<number | null>(null);
  const showLoading = useDelayedLoading(isLoading && rows.length === 0);

  const count = rows.length;
  const activeIndex = count === 0 ? -1 : Math.min(active, count - 1);

  const rangeExtractor = useCallback(
    (range: Range) => {
      const indexes = defaultRangeExtractor(range);
      if (activeIndex >= 0 && !indexes.includes(activeIndex)) {
        indexes.push(activeIndex);
        indexes.sort((a, b) => a - b);
      }
      return indexes;
    },
    [activeIndex],
  );

  // The virtualizer's methods change without its identity changing, so the React Compiler skips
  // memoizing this component; nothing here is passed to memoized children.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan,
    scrollMargin: HEADER_HEIGHT,
    getItemKey: (i) => getRowId(rows[i], i),
    rangeExtractor,
  });
  const items = virtualizer.getVirtualItems();
  const lastRendered = items.length > 0 ? items[items.length - 1].index : -1;

  // Ask once per row count: the next call waits until the list has grown (or shrunk).
  const endRequestedAt = useRef(-1);
  useEffect(() => {
    if (!onEndReached || count === 0 || lastRendered < count - END_THRESHOLD) return;
    if (endRequestedAt.current === count) return;
    endRequestedAt.current = count;
    onEndReached();
  }, [onEndReached, lastRendered, count]);

  // Move focus once the requested row is rendered (it always is: see rangeExtractor).
  useEffect(() => {
    if (focusRequest === null) return;
    rowRefs.current.get(focusRequest)?.focus({ preventScroll: true });
    setFocusRequest(null);
  }, [focusRequest, items]);

  const moveTo = (index: number) => {
    if (count === 0) return;
    const next = Math.max(0, Math.min(count - 1, index));
    setActive(next);
    virtualizer.scrollToIndex(next, { align: 'auto' });
    setFocusRequest(next);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>, index: number) => {
    // Keys pressed inside a cell's own control (a link) belong to that control.
    if (e.target !== e.currentTarget) return;
    // A screen of rows, less one so the viewer keeps a row of context.
    const pageSize = Math.max(1, Math.floor((scrollRef.current?.clientHeight || rowHeight * 10) / rowHeight) - 1);
    switch (e.key) {
      case 'ArrowDown':
        moveTo(index + 1);
        break;
      case 'ArrowUp':
        moveTo(index - 1);
        break;
      case 'PageDown':
        moveTo(index + pageSize);
        break;
      case 'PageUp':
        moveTo(index - pageSize);
        break;
      case 'Home':
        moveTo(0);
        break;
      case 'End':
        moveTo(count - 1);
        break;
      case 'Enter':
      case ' ':
        if (!onRowActivate) return;
        onRowActivate(rows[index], index);
        break;
      default:
        return;
    }
    e.preventDefault();
  };

  const onClick = (e: MouseEvent<HTMLDivElement>, index: number) => {
    setActive(index);
    // A link or button inside a cell does its own thing.
    const target = e.target as HTMLElement;
    if (target.closest(INTERACTIVE) && target.closest(INTERACTIVE) !== e.currentTarget) return;
    onRowActivate?.(rows[index], index);
  };

  const gridTemplateColumns = columns.map((c) => c.width ?? 'minmax(0,1fr)').join(' ');
  const rowCount = (totalRows ?? count) < 0 ? -1 : (totalRows ?? count) + 1;

  let state: ReactNode = null;
  if (count === 0) {
    if (error !== undefined && error !== null) state = <ErrorState error={error} onRetry={onRetry} className="m-3" />;
    else if (isLoading) state = showLoading ? <LoadingState rows={4} className="p-3" /> : null;
    else state = empty ?? <EmptyState title="Nothing to show yet" className="m-3" />;
  }

  return (
    <div className={cn('overflow-hidden rounded-lg border border-border bg-surface', className)}>
      <div ref={scrollRef} className="relative overflow-auto" style={{ maxHeight }}>
        <div role="grid" aria-label={ariaLabel} aria-readonly="true" aria-rowcount={rowCount} aria-colcount={columns.length} className="min-w-full">
          <div role="rowgroup" className="sticky top-0 z-10 bg-surface">
            <div
              role="row"
              aria-rowindex={1}
              className="grid items-center gap-x-3 border-b border-border px-3 text-xs font-medium text-fg-muted"
              style={{ gridTemplateColumns, height: HEADER_HEIGHT }}
            >
              {columns.map((c, ci) => (
                <div
                  key={c.id}
                  role="columnheader"
                  aria-colindex={ci + 1}
                  className={cn('flex min-w-0 items-center', c.numeric && 'justify-end text-right', c.className)}
                >
                  <span className="truncate">{c.header}</span>
                </div>
              ))}
            </div>
          </div>
          {count > 0 ? (
            <div role="rowgroup" className="relative" style={{ height: virtualizer.getTotalSize() }}>
              {items.map((item) => {
                const row = rows[item.index];
                const focused = item.index === activeIndex;
                return (
                  <div
                    key={item.key}
                    ref={(el) => {
                      if (el) rowRefs.current.set(item.index, el);
                      else rowRefs.current.delete(item.index);
                    }}
                    role="row"
                    aria-rowindex={item.index + 2}
                    tabIndex={focused ? 0 : -1}
                    data-row-index={item.index}
                    {...rowAttributes?.(row, item.index)}
                    onKeyDown={(e) => onKeyDown(e, item.index)}
                    onClick={(e) => onClick(e, item.index)}
                    onFocus={() => setActive(item.index)}
                    className={cn(
                      'absolute inset-x-0 top-0 grid items-center gap-x-3 border-b border-border px-3 text-sm text-fg',
                      'focus-visible:-outline-offset-2',
                      onRowActivate && 'cursor-pointer hover:bg-surface-hover',
                    )}
                    style={{ gridTemplateColumns, height: rowHeight, transform: `translateY(${item.start - HEADER_HEIGHT}px)` }}
                  >
                    {columns.map((c, ci) => (
                      <div
                        key={c.id}
                        role="gridcell"
                        aria-colindex={ci + 1}
                        className={cn('flex min-w-0 items-center', c.numeric && 'justify-end text-right tabular', c.className)}
                      >
                        {c.cell(row, item.index)}
                      </div>
                    ))}
                  </div>
                );
              })}
            </div>
          ) : null}
        </div>
        {state}
      </div>
      {footer ? <div className="border-t border-border px-3 py-2 text-xs text-fg-muted">{footer}</div> : null}
    </div>
  );
}
