import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { OUTCOME_IDS } from '@/domain/contracts';
import { outcomeView } from '@/domain/presentation';
import { OutcomePill } from './OutcomePill';

describe('OutcomePill', () => {
  it.each(OUTCOME_IDS)('renders %s with its contract label and an icon', (id) => {
    const { container } = render(<OutcomePill outcome={id} />);
    expect(screen.getByText(outcomeView(id).label)).toBeInTheDocument();
    const pill = container.querySelector(`[data-outcome="${id}"]`) as HTMLElement;
    expect(pill.style.getPropertyValue('--badge')).toMatch(/^var\(--tone-/);
    expect(pill.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
  });
});
