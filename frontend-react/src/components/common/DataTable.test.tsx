import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';
import { ApiError } from '@/data/errors';
import { DataTable, type DataTableColumn } from './DataTable';
import { EmptyState } from './StateViews';

interface Claim {
  id: string;
  text: string;
  count: number;
}

const claims: Claim[] = Array.from({ length: 100 }, (_, i) => ({ id: `c${i}`, text: `Claim ${i}`, count: i * 10 }));

const columns: DataTableColumn<Claim>[] = [
  { id: 'text', header: 'Claim', cell: (c) => c.text, width: 'minmax(0,2fr)' },
  { id: 'count', header: 'Seen', cell: (c) => c.count, numeric: true, width: '5rem' },
];

function renderTable(props: Partial<Parameters<typeof DataTable<Claim>>[0]> = {}) {
  return render(<DataTable<Claim> aria-label="Claims" columns={columns} rows={claims} getRowId={(c) => c.id} {...props} />);
}

// jsdom has no layout: give every element a 400px-tall box so the virtualizer has a viewport
// (10 rows of 40px), and a scrollTo that moves scrollTop and fires the scroll event.
const saved = {
  offsetHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight'),
  offsetWidth: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth'),
  scrollTo: Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTo'),
};
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get: () => 400 });
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get: () => 800 });
  Object.defineProperty(Element.prototype, 'scrollTo', {
    configurable: true,
    value(this: Element, options: ScrollToOptions | number, y?: number) {
      this.scrollTop = typeof options === 'number' ? (y ?? 0) : (options.top ?? this.scrollTop);
      this.dispatchEvent(new Event('scroll'));
    },
  });
});
afterAll(() => {
  for (const [key, descriptor] of Object.entries(saved)) {
    const target = key === 'scrollTo' ? Element.prototype : HTMLElement.prototype;
    if (descriptor) Object.defineProperty(target, key, descriptor);
    else Reflect.deleteProperty(target, key);
  }
});

const rowAt = (rowIndex: number) => document.querySelector(`[role="row"][aria-rowindex="${rowIndex}"]`) as HTMLElement;

describe('DataTable', () => {
  it('is a named grid with a header row, full-list row count and one tab stop', () => {
    renderTable();
    const grid = screen.getByRole('grid', { name: 'Claims' });
    expect(grid).toHaveAttribute('aria-rowcount', '101');
    expect(grid).toHaveAttribute('aria-colcount', '2');
    expect(within(grid).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Claim', 'Seen']);
    const rows = within(grid).getAllByRole('row').slice(1);
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(claims.length);
    expect(rows.filter((r) => r.tabIndex === 0)).toHaveLength(1);
    expect(rowAt(2)).toHaveAttribute('tabindex', '0');
  });

  it('moves row focus with the arrow keys, Home and End, and opens a row with Enter', async () => {
    const onRowActivate = vi.fn();
    renderTable({ onRowActivate });
    await userEvent.tab();
    expect(rowAt(2)).toHaveFocus();
    await userEvent.keyboard('{ArrowDown}');
    await waitFor(() => expect(rowAt(3)).toHaveFocus());
    await userEvent.keyboard('{End}');
    await waitFor(() => expect(rowAt(101)).toHaveFocus());
    expect(rowAt(101)).toHaveTextContent('Claim 99');
    expect(rowAt(101)).toHaveAttribute('tabindex', '0');
    await userEvent.keyboard('{Home}');
    await waitFor(() => expect(rowAt(2)).toHaveFocus());
    await userEvent.keyboard('{Enter}');
    expect(onRowActivate).toHaveBeenLastCalledWith(claims[0], 0);
    await userEvent.keyboard('{ArrowUp}');
    expect(rowAt(2)).toHaveFocus();
  });

  it('opens a row on click, but lets a link inside a cell do its own thing', async () => {
    const onRowActivate = vi.fn();
    render(
      <DataTable<Claim>
        aria-label="Claims"
        rows={claims.slice(0, 3)}
        getRowId={(c) => c.id}
        onRowActivate={onRowActivate}
        columns={[
          { id: 'text', header: 'Claim', cell: (c) => c.text },
          { id: 'link', header: 'Source', cell: (c) => <a href={`#${c.id}`}>source</a> },
        ]}
      />,
    );
    await userEvent.click(screen.getByText('Claim 1'));
    expect(onRowActivate).toHaveBeenCalledWith(claims[1], 1);
    const link = within(rowAt(2)).getByRole('link', { name: 'source' });
    await userEvent.click(link);
    expect(onRowActivate).toHaveBeenCalledTimes(1);
    link.focus();
    await userEvent.keyboard('{Enter}');
    expect(onRowActivate).toHaveBeenCalledTimes(1);
  });

  it('shows the loading state after the page-contract delay', async () => {
    renderTable({ rows: [], isLoading: true });
    expect(screen.queryByText('Loading…')).toBeNull();
    expect(await screen.findByText('Loading…')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Claim' })).toBeInTheDocument();
  });

  it("shows the server's message with Retry when the first load fails", async () => {
    const onRetry = vi.fn();
    renderTable({
      rows: [],
      error: new ApiError({ kind: 'http', status: 503, code: 'unavailable', message: 'Knowledge base is starting' }),
      onRetry,
    });
    expect(screen.getByRole('alert')).toHaveTextContent('Knowledge base is starting');
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it('shows the empty slot when there are no rows', () => {
    renderTable({ rows: [], empty: <EmptyState title="No claims yet" /> });
    expect(screen.getByText('No claims yet')).toBeInTheDocument();
    expect(screen.getByRole('grid', { name: 'Claims' })).toHaveAttribute('aria-rowcount', '1');
  });

  it('asks once for the next page when the last rows are rendered, again after the list grows', () => {
    const onEndReached = vi.fn();
    const { rerender } = renderTable({ rows: claims.slice(0, 5), totalRows: -1, onEndReached });
    expect(onEndReached).toHaveBeenCalledOnce();
    expect(screen.getByRole('grid')).toHaveAttribute('aria-rowcount', '-1');
    rerender(<DataTable<Claim> aria-label="Claims" columns={columns} rows={claims.slice(0, 5)} getRowId={(c) => c.id} totalRows={-1} onEndReached={onEndReached} />);
    expect(onEndReached).toHaveBeenCalledOnce();
    rerender(<DataTable<Claim> aria-label="Claims" columns={columns} rows={claims.slice(0, 8)} getRowId={(c) => c.id} totalRows={-1} onEndReached={onEndReached} />);
    expect(onEndReached).toHaveBeenCalledTimes(2);
  });

  it('asks again for a replaced list of the same length, as after a new filter', () => {
    const onEndReached = vi.fn();
    const props = { 'aria-label': 'Claims', columns, getRowId: (c: Claim) => c.id, totalRows: -1, onEndReached };
    const { rerender } = render(<DataTable<Claim> {...props} rows={claims.slice(0, 5)} />);
    expect(onEndReached).toHaveBeenCalledOnce();
    rerender(<DataTable<Claim> {...props} rows={claims.slice(50, 55)} />);
    expect(onEndReached).toHaveBeenCalledTimes(2);
  });

  it('does not take the focused row, kept rendered far below the view, for reaching the end', async () => {
    const onEndReached = vi.fn();
    const props = { 'aria-label': 'Claims', columns, getRowId: (c: Claim) => c.id, totalRows: -1, onEndReached };
    const { rerender } = render(<DataTable<Claim> {...props} rows={claims} />);
    const viewport = screen.getByRole('grid').parentElement as HTMLElement;
    Object.defineProperty(viewport, 'scrollHeight', { configurable: true, get: () => 36 + claims.length * 40 });
    Object.defineProperty(viewport, 'clientHeight', { configurable: true, get: () => 400 });
    await userEvent.tab();
    await userEvent.keyboard('{End}');
    await waitFor(() => expect(rowAt(101)).toHaveFocus());
    expect(onEndReached).toHaveBeenCalledOnce();
    // Back at the top, a new list of 60 rows: the focus position, clamped to its last row, stays
    // rendered far below the view, but the viewer has not scrolled anywhere near it.
    act(() => viewport.scrollTo({ top: 0 }));
    rerender(<DataTable<Claim> {...props} rows={claims.slice(0, 60)} />);
    expect(rowAt(61)).toHaveAttribute('tabindex', '0');
    expect(rowAt(30)).toBeNull();
    expect(onEndReached).toHaveBeenCalledOnce();
  });

  it("shows a later page's failure under the rows, with Retry", async () => {
    const onRetry = vi.fn();
    renderTable({
      rows: claims.slice(0, 5),
      totalRows: -1,
      error: new ApiError({ kind: 'http', status: 503, code: 'unavailable', message: 'Knowledge base is starting' }),
      onRetry,
    });
    expect(rowAt(2)).toHaveTextContent('Claim 0');
    expect(screen.getByRole('alert')).toHaveTextContent('Knowledge base is starting');
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it('scrolls a row above the view to just under the sticky header, never behind it', async () => {
    renderTable();
    const viewport = screen.getByRole('grid').parentElement as HTMLElement;
    // A 36px header plus 100 rows of 40px, seen through the 400px viewport.
    Object.defineProperty(viewport, 'scrollHeight', { configurable: true, get: () => 36 + claims.length * 40 });
    Object.defineProperty(viewport, 'clientHeight', { configurable: true, get: () => 400 });
    await userEvent.tab();
    await userEvent.keyboard('{End}');
    await waitFor(() => expect(rowAt(101)).toHaveFocus());
    expect(viewport.scrollTop).toBe(36 + claims.length * 40 - 400);
    await userEvent.keyboard('{Home}');
    await waitFor(() => expect(rowAt(2)).toHaveFocus());
    // The first row sits right under the 36px header, so the list is scrolled to its very top.
    expect(viewport.scrollTop).toBe(0);
  });

  it('pages by a screen of rows with Page Down and Page Up', async () => {
    renderTable();
    await userEvent.tab();
    await userEvent.keyboard('{PageDown}');
    // A 400px viewport of 40px rows is 10 rows; a page keeps one row of context.
    await waitFor(() => expect(rowAt(11)).toHaveFocus());
    await userEvent.keyboard('{PageUp}');
    await waitFor(() => expect(rowAt(2)).toHaveFocus());
  });

  it('puts numbers right-aligned in tabular figures and adds row attributes', () => {
    renderTable({ rowAttributes: (c) => ({ 'data-claim': c.id }) });
    const cell = within(rowAt(3)).getAllByRole('gridcell')[1];
    expect(cell).toHaveTextContent('10');
    expect(cell.className).toContain('tabular');
    expect(rowAt(3)).toHaveAttribute('data-claim', 'c1');
  });
});
