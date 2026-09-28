/**
 * Pages of a server-side list that reports its total ("51–100 of 1,234"). The range is written out
 * and announced when it changes; Previous and Next name the page they go to. At either end the
 * button is disabled and the range beside it says why (it is the first or last page).
 * For "load more as you scroll" lists use DataTable's `onEndReached` instead.
 */
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { formatCount } from '@/domain/format';
import { cn } from '@/lib/cn';

export interface PaginationProps {
  /** 1-based page number. */
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  /** What is being paged, plural ("claims"). */
  noun?: string;
  className?: string;
}

function pageCount(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(Math.max(0, total) / Math.max(1, pageSize)));
}

export function Pagination({ page, pageSize, total, onPageChange, noun = 'items', className }: PaginationProps) {
  const pages = pageCount(total, pageSize);
  const current = Math.min(Math.max(1, page), pages);
  const first = total === 0 ? 0 : (current - 1) * pageSize + 1;
  const last = Math.min(total, current * pageSize);
  const range = total === 0 ? `No ${noun}` : `${formatCount(first)}–${formatCount(last)} of ${formatCount(total)} ${noun}`;
  return (
    <nav aria-label="Pagination" className={cn('flex flex-wrap items-center justify-between gap-3 text-sm', className)}>
      <p aria-live="polite" className="text-fg-muted tabular">
        {range}
        {pages > 1 ? <span className="sr-only">, page {current} of {pages}</span> : null}
      </p>
      <div className="flex items-center gap-1.5">
        <Button
          size="sm"
          disabled={current <= 1}
          aria-label={current <= 1 ? 'Previous page (this is the first page)' : `Previous page, page ${current - 1}`}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft aria-hidden="true" />
          Previous
        </Button>
        <span className="px-1 text-xs text-fg-muted tabular" aria-hidden="true">
          {current} / {pages}
        </span>
        <Button
          size="sm"
          disabled={current >= pages}
          aria-label={current >= pages ? 'Next page (this is the last page)' : `Next page, page ${current + 1}`}
          onClick={() => onPageChange(current + 1)}
        >
          Next
          <ChevronRight aria-hidden="true" />
        </Button>
      </div>
    </nav>
  );
}
