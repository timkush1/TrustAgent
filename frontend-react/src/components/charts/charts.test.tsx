import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { BarChart } from './BarChart';
import { OutcomeTimeSeries, type OutcomeBucket } from './OutcomeTimeSeries';
import { ProbabilityBars } from './ProbabilityBars';
import { Sparkline } from './Sparkline';

const buckets: OutcomeBucket[] = [
  { t: '2026-09-27T10:00:00Z', counts: { passed: 40, flagged: 3, blocked: 1 } },
  { t: '2026-09-27T11:00:00Z', counts: { passed: 52, replaced: 2 } },
  { t: '2026-09-27T12:00:00Z', counts: { passed: 31, flagged: 1 } },
];

describe('OutcomeTimeSeries', () => {
  it('stacks only the outcomes that occur, in the fixed order, with a legend in plain words', () => {
    const { container } = render(<OutcomeTimeSeries title="Requests by outcome" description="Per hour" buckets={buckets} width={600} formatBucket={(iso) => iso.slice(11, 16)} />);
    expect(screen.getByRole('heading', { level: 3, name: 'Requests by outcome' })).toBeInTheDocument();
    const legend = screen.getByRole('list', { name: 'Legend' });
    expect(within(legend).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Passed', 'Flagged', 'Replaced', 'Blocked']);
    const segments = [...container.querySelectorAll('path[data-series]')];
    expect(new Set(segments.map((p) => p.getAttribute('data-series')))).toEqual(new Set(['passed', 'flagged', 'replaced', 'blocked']));
    expect(container.querySelector('path[data-series="blocked"]')).toHaveAttribute('fill', 'var(--tone-danger)');
    expect(container.querySelector('path[data-series="replaced"]')?.getAttribute('fill')).toContain('color-mix');
    const svg = container.querySelector('svg.recharts-surface') as SVGSVGElement;
    expect(svg).toHaveAccessibleName('Requests by outcome');
    expect(svg).toHaveAccessibleDescription('130 requests in 3 intervals; the busiest, 11:00, had 54.');
  });

  it('is focusable, and the arrow keys show the readout for a bucket with every outcome', async () => {
    const { container } = render(
      <OutcomeTimeSeries title="Requests by outcome" buckets={buckets} width={600} formatBucket={(iso) => iso.slice(11, 16)} />,
    );
    const svg = container.querySelector('svg.recharts-surface') as SVGSVGElement;
    expect(svg).toHaveAttribute('tabindex', '0');
    svg.focus();
    await userEvent.keyboard('{ArrowRight}');
    // Focus shows the first bucket; the arrow moves to the next one.
    const readout = await screen.findByText('54 total');
    const box = readout.parentElement as HTMLElement;
    expect(box).toHaveTextContent('11:00');
    expect(within(box).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['0Blocked', '2Replaced', '0Flagged', '52Passed']);
  });

  it('has a table view with every number, including totals', async () => {
    render(<OutcomeTimeSeries title="Requests by outcome" buckets={buckets} width={600} formatBucket={(iso) => iso.slice(11, 16)} />);
    await userEvent.click(screen.getByRole('radio', { name: 'Table' }));
    const table = screen.getByRole('table', { name: 'Requests by outcome' });
    const headers = within(table).getAllByRole('columnheader').map((h) => h.textContent);
    expect(headers).toEqual(['Time', 'Passed', 'Flagged', 'Replaced', 'Blocked', 'Total']);
    const firstRow = within(table).getAllByRole('row')[1];
    expect(within(firstRow).getByRole('rowheader')).toHaveTextContent('10:00');
    expect(within(firstRow).getAllByRole('cell').map((c) => c.textContent)).toEqual(['40', '3', '0', '1', '44']);
    expect(screen.queryByRole('list', { name: 'Legend' })).toBeNull();
  });

  it('keeps a color with its outcome whatever order the caller asks for', () => {
    const { container } = render(
      <OutcomeTimeSeries title="Blocked and passed" buckets={buckets} outcomes={['blocked', 'passed']} width={600} />,
    );
    const legend = screen.getByRole('list', { name: 'Legend' });
    expect(within(legend).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Passed', 'Blocked']);
    expect(container.querySelector('path[data-series="passed"]')).toHaveAttribute('fill', 'var(--tone-success)');
  });

  it('says when there is nothing to show', () => {
    const { container } = render(<OutcomeTimeSeries title="Requests by outcome" buckets={[]} width={600} />);
    expect(container.querySelector('svg.recharts-surface')).toHaveAccessibleDescription('No requests in this window.');
  });
});

describe('BarChart', () => {
  const reasons = [
    { key: 'contradiction', label: 'Contradicts your documents', value: 12 },
    { key: 'numbers', label: 'Number not in your documents', value: 5 },
  ];

  it('labels each bar with its value and needs no legend for one series', () => {
    const { container } = render(<BarChart title="What was caught" data={reasons} valueLabel="Requests" width={480} />);
    expect(screen.queryByRole('list', { name: 'Legend' })).toBeNull();
    const labels = [...container.querySelectorAll('.recharts-label-list text')].map((t) => t.textContent);
    expect(labels).toEqual(['12', '5']);
    expect(container.querySelector('.recharts-bar-rectangle path')).toHaveAttribute('fill', 'var(--fg-muted)');
    expect(container.querySelector('svg.recharts-surface')).toHaveAccessibleDescription(
      'Contradicts your documents: 12; Number not in your documents: 5',
    );
  });

  it('has a table view', async () => {
    render(<BarChart title="What was caught" data={reasons} valueLabel="Requests" width={480} />);
    await userEvent.click(screen.getByRole('radio', { name: 'Table' }));
    const table = screen.getByRole('table', { name: 'What was caught' });
    expect(within(table).getAllByRole('row')[1]).toHaveTextContent('Contradicts your documents12');
  });
});

describe('ProbabilityBars', () => {
  const options = [
    { key: 'answers_request', label: 'Answers the request', p: 0.08 },
    { key: 'declines_or_deflects', label: 'Declines or deflects', p: 0.86, tone: 'warning' as const },
    { key: 'nothing_to_answer', label: 'Nothing to answer', p: 0.06 },
  ];

  it('lists every option with its probability and marks the most likely one', () => {
    const { container } = render(<ProbabilityBars label="Refusal" options={options} />);
    const list = screen.getByRole('list', { name: 'Refusal' });
    const items = within(list).getAllByRole('listitem');
    expect(items.map((li) => li.textContent)).toEqual([
      'Answers the request: 0.08',
      'Declines or deflects (most likely): 0.86',
      'Nothing to answer: 0.06',
    ]);
    const fill = container.querySelector('[data-option="declines_or_deflects"] [aria-hidden] span') as HTMLElement;
    expect(fill.style.width).toBe('86%');
    expect(fill.style.backgroundColor).toBe('var(--tone-warning)');
    const neutral = container.querySelector('[data-option="answers_request"] [aria-hidden] span') as HTMLElement;
    expect(neutral.style.backgroundColor).toBe('var(--fg-muted)');
  });

  it('can sort most likely first and follow the reported top option', () => {
    render(<ProbabilityBars label="Refusal" options={options} sort="desc" top="nothing_to_answer" />);
    const items = within(screen.getByRole('list', { name: 'Refusal' })).getAllByRole('listitem');
    expect(items[0]).toHaveTextContent('Declines or deflects');
    expect(items[2]).toHaveTextContent('Nothing to answer (most likely)');
  });
});

describe('Sparkline', () => {
  it('is an image named with what it shows and a summary, with a gap for missing points', () => {
    const { container } = render(<Sparkline label="Fast-check p95" values={[210, 230, null, 190, 250]} format={(v) => `${v} ms`} />);
    expect(screen.getByRole('img', { name: 'Fast-check p95: from 210 ms to 250 ms, lowest 190 ms, highest 250 ms' })).toBeInTheDocument();
    expect(container.querySelectorAll('path')).toHaveLength(2);
    const dot = container.querySelector('[data-end-dot]') as SVGCircleElement;
    expect(dot).toHaveAttribute('r', '4');
    expect(dot).toHaveAttribute('stroke', 'var(--surface)');
  });

  it('shows the value under the pointer', () => {
    const { container } = render(
      <Sparkline label="Blocked per hour" values={[1, 4, 2]} pointLabels={['10:00', '11:00', '12:00']} width={100} />,
    );
    const svg = container.querySelector('svg') as SVGSVGElement;
    svg.getBoundingClientRect = () => ({ left: 0, top: 0, width: 100, height: 32, right: 100, bottom: 32, x: 0, y: 0, toJSON: () => ({}) });
    fireEvent.pointerMove(svg, { clientX: 50, clientY: 10 });
    expect(screen.getByText('4')).toBeInTheDocument();
    expect(screen.getByText('11:00')).toBeInTheDocument();
    fireEvent.pointerLeave(svg);
    expect(screen.queryByText('11:00')).toBeNull();
  });

  it('says so when there is no data', () => {
    render(<Sparkline label="Cost" values={[null, null]} />);
    expect(screen.getByRole('img', { name: 'Cost: no data' })).toBeInTheDocument();
  });
});
