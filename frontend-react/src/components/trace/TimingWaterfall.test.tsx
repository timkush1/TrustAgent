/**
 * The timing waterfall: where each step falls on the request's clock (a check before the answer
 * on enforcing routes, after it on observe routes, the first token of a stream as a point), and
 * the chart's keyboard readout and table view.
 */
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { niceTicks } from '@/components/charts/scale';
import { axeViolations, formatViolations } from '@/test/axe';
import { TimingWaterfall } from './TimingWaterfall';
import { rowReadout, waterfallRows } from './waterfall';

const TIMINGS = { upstream_ms: 842, ttft_ms: 210, gate_ms: 12, overhead_ms: 15, total_ms: 857 };

describe('waterfallRows', () => {
  it('puts the check after the model answer and inside what the proxy added', () => {
    const rows = waterfallRows({ timings: TIMINGS, deferred: false });
    expect(rows.map((r) => [r.key, r.start, r.end, r.note])).toEqual([
      ['upstream', 0, 842, null],
      ['ttft', 210, 210, null],
      ['gate', 842, 854, null],
      ['overhead', 842, 857, 'includes the fast check'],
      ['total', 0, 857, null],
    ]);
    expect(rows.find((r) => r.key === 'ttft')?.point).toBe(true);
  });

  it('runs an observe route\'s check after the answer was sent', () => {
    const rows = waterfallRows({ timings: { ...TIMINGS, ttft_ms: null, overhead_ms: 2, total_ms: 844 }, deferred: true });
    const gate = rows.find((r) => r.key === 'gate');
    expect([gate?.start, gate?.end, gate?.note]).toEqual([844, 856, 'after the answer was sent']);
    expect(rows.find((r) => r.key === 'overhead')?.note).toBeNull();
  });

  it('uses the decision\'s gate time when the trace has none, and leaves out what was not measured', () => {
    const rows = waterfallRows({
      timings: { upstream_ms: null, ttft_ms: null, gate_ms: null, overhead_ms: null, total_ms: 20 },
      gateMs: 6,
      deferred: false,
    });
    expect(rows.map((r) => [r.key, r.start, r.end])).toEqual([
      ['gate', 0, 6],
      ['total', 0, 20],
    ]);
  });

  it('reads each row in words', () => {
    const [upstream, ttft] = waterfallRows({ timings: TIMINGS, deferred: false });
    expect(rowReadout(upstream)).toBe('Model answer: 842 ms, from 0 ms to 842 ms');
    expect(rowReadout(ttft)).toBe('First token at 210 ms');
  });

  it('picks round axis ticks that cover the longest step', () => {
    expect(niceTicks(857)).toEqual([0, 200, 400, 600, 800, 1000]);
    expect(niceTicks(12)).toEqual([0, 5, 10, 15]);
    expect(niceTicks(0)).toEqual([0, 1]);
  });
});

describe('TimingWaterfall', () => {
  it('moves the readout with the arrow keys, has a table view, and passes axe', async () => {
    const user = userEvent.setup();
    const { container } = render(<TimingWaterfall rows={waterfallRows({ timings: TIMINGS, deferred: false })} />);
    const chart = screen.getByRole('application', { name: /Where the time went/ });
    expect(container.querySelectorAll('[data-row]')).toHaveLength(5);
    chart.focus();
    await user.keyboard('{ArrowDown}');
    expect(within(chart).getByText('Model answer: 842 ms, from 0 ms to 842 ms')).toHaveClass('sr-only');
    await user.keyboard('{ArrowDown}{ArrowDown}');
    expect(within(chart).getByText('Fast check (Jev): 12 ms, from 842 ms to 854 ms')).toBeInTheDocument();
    expect(formatViolations(await axeViolations(container))).toEqual([]);

    await user.click(screen.getByRole('radio', { name: 'Table' }));
    const table = screen.getByRole('table');
    expect(within(table).getByRole('rowheader', { name: 'Proxy overhead (includes the fast check)' })).toBeInTheDocument();
    expect(within(table).getAllByRole('row')).toHaveLength(6);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
