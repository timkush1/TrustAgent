import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { DiffView } from './DiffView';
import { EvidenceBadge } from './EvidenceBadge';
import { LabelledRateView } from './LabelledRate';

describe('DiffView', () => {
  it('announces added and removed lines and counts them', () => {
    render(<DiffView label="Changes to policies.yaml" before={'a\nmode: flag\nc\n'} after={'a\nmode: block\nc\n'} />);
    const region = screen.getByRole('region', { name: 'Changes to policies.yaml' });
    expect(region.textContent).toContain('Added: mode: block');
    expect(region.textContent).toContain('Removed: mode: flag');
    expect(screen.getByText('1 added · 1 removed')).toBeInTheDocument();
  });

  it('says so when nothing changed', () => {
    render(<DiffView label="Changes" before="same\n" after="same\n" emptyText="Nothing to save." />);
    expect(screen.getByText('Nothing to save.')).toBeInTheDocument();
    expect(screen.getByText('No changes')).toBeInTheDocument();
  });

  it('unfolds unchanged lines on request', async () => {
    const user = userEvent.setup();
    const before = Array.from({ length: 12 }, (_, i) => `line ${i}`).join('\n');
    render(<DiffView label="Changes" before={before} after={before.replace('line 11', 'last')} context={1} />);
    expect(screen.queryByText('line 2')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show 10 unchanged lines' }));
    expect(screen.getByText('line 2')).toBeInTheDocument();
  });

  it('renders a server unified diff', () => {
    render(<DiffView label="Server diff" unified={'@@ -5,1 +5,1 @@\n-x\n+y\n'} />);
    expect(screen.getByText('4 unchanged lines')).toBeInTheDocument();
    expect(screen.getByText('1 added · 1 removed')).toBeInTheDocument();
  });
});

describe('EvidenceBadge and LabelledRateView', () => {
  it('explains a label, its basis and its source', async () => {
    const user = userEvent.setup();
    render(<EvidenceBadge label="vendor" basis="Jev list price" sourceUrl="https://openrouter.ai/typesafe/jev-1.13" asOf="2026-09-27" subject="Fast-check price" />);
    await user.click(screen.getByRole('button', { name: 'Vendor: how “Fast-check price” was produced' }));
    expect(await screen.findByText('Jev list price')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Source' })).toHaveAttribute('href', 'https://openrouter.ai/typesafe/jev-1.13');
    expect(screen.getByText(/as of 2026-09-27/)).toBeInTheDocument();
  });

  it('shows a meter instead of a number below min_n', async () => {
    const { container } = render(
      <LabelledRateView rate={{ value: 0.5, label: 'measured', basis: 'uniform', n: 12, ci95: null, min_n: 30 }} subject="Hallucination rate" />,
    );
    expect(screen.getByText('Needs 30 unbiased samples (have 12)')).toBeInTheDocument();
    expect(screen.getByRole('meter', { name: 'Hallucination rate: unbiased samples collected' })).toHaveAttribute('aria-valuenow', '12');
    expect(screen.queryByText('50%')).not.toBeInTheDocument();
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
