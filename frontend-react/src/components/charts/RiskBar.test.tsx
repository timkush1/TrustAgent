import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { RiskBar } from './RiskBar';

describe('RiskBar', () => {
  it('is a meter that states the risk and both thresholds, with a tick for each', () => {
    const { container } = render(<RiskBar risk={0.964} level="critical" warnAt={0.8} criticalAt={0.95} label="Groundedness risk" />);
    const meter = screen.getByRole('meter', { name: 'Groundedness risk' });
    expect(meter).toHaveAttribute('aria-valuenow', '0.96');
    expect(meter).toHaveAttribute('aria-valuetext', '0.96 (flags at 0.80, blocks at 0.95)');
    expect(meter.style.getPropertyValue('--bar')).toBe('var(--tone-danger)');
    expect((container.querySelector('[data-tick="flag"]') as HTMLElement).style.left).toBe('80%');
    expect((container.querySelector('[data-tick="block"]') as HTMLElement).style.left).toBe('95%');
  });

  it('has no block tick for an evaluator that never blocks, and clamps the fill', () => {
    const { container } = render(<RiskBar risk={1.2} level="warn" warnAt={0.7} criticalAt={null} label="Refusal risk" />);
    const meter = screen.getByRole('meter', { name: 'Refusal risk' });
    expect(meter).toHaveAttribute('aria-valuenow', '1');
    expect(meter).toHaveAttribute('aria-valuetext', '1.00 (flags at 0.70, never blocks)');
    expect(meter.style.getPropertyValue('--bar')).toBe('var(--tone-warning)');
    expect(container.querySelector('[data-tick="block"]')).toBeNull();
    expect((meter.firstElementChild as HTMLElement).style.width).toBe('100%');
  });
});
