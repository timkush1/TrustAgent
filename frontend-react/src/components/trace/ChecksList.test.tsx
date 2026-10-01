import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { decision, trace } from '@/test/fixtures';
import { s0Decision, s0Trace } from '@/test/scenarios';
import { ChecksList } from './ChecksList';

describe('ChecksList', () => {
  it('labels shadow evaluators as watching and keys the ticks once', () => {
    const d = s0Decision('grounded-pass');
    const shadow = { ...d, evaluators: { ...d.evaluators, 'off_brand_tone@1': { ...d.evaluators['groundedness@1'], ref: 'off_brand_tone@1', stage: 'shadow' as const } } };
    render(<ChecksList summary={s0Trace('grounded-pass')} decision={shadow} />);
    const items = within(screen.getByRole('list', { name: 'Checks' })).getAllByRole('listitem');
    expect(items).toHaveLength(3);
    expect(items[2]).toHaveTextContent('Off brand tone Watching (not affecting responses)');
    expect(screen.getAllByRole('meter')).toHaveLength(3);
    expect(screen.getByText('Flags from here')).toBeInTheDocument();
    expect(screen.getByText('Can block from here')).toBeInTheDocument();
  });

  it('says why no checks ran for a skipped request', () => {
    const skipped = trace({ gate_status: 'skipped', skip_reason: 'budget', action: 'pass' });
    render(<ChecksList summary={skipped} decision={decision({ status: 'skipped', skip_reason: 'budget', evaluators: {} })} />);
    expect(screen.getByText("No checks ran: today's fast-check budget is used up.")).toBeInTheDocument();
    expect(screen.queryByRole('list')).toBeNull();
  });

  it('says when the check details are not available', () => {
    render(<ChecksList summary={s0Trace('grounded-pass')} decision={null} />);
    expect(screen.getByText('The check details are not available for this request.')).toBeInTheDocument();
  });

  it('names the checks from the evaluator library', () => {
    const names = new Map([['groundedness', 'Grounded in your documents']]);
    render(<ChecksList summary={s0Trace('grounded-pass')} decision={s0Decision('grounded-pass')} names={names} />);
    const items = within(screen.getByRole('list', { name: 'Checks' })).getAllByRole('listitem');
    expect(items[0]).toHaveTextContent('Grounded in your documents');
    // Not in the library (or no library): the humanized id.
    expect(items[1]).toHaveTextContent('Contradiction');
  });

  it('says a late decision was recorded, not enforced', () => {
    render(<ChecksList summary={s0Trace('refund-contradiction-flag')} decision={{ ...s0Decision('refund-contradiction-flag'), late: true }} />);
    expect(screen.getByTestId('late-decision')).toHaveTextContent(
      'Arrived late, not enforced. The fast check answered after the deadline, so its result did not change what was sent.',
    );
    expect(screen.getAllByRole('meter')).toHaveLength(2);
  });

  it('says nothing about lateness for an on-time decision', () => {
    render(<ChecksList summary={s0Trace('refund-contradiction-flag')} decision={s0Decision('refund-contradiction-flag')} />);
    expect(screen.queryByTestId('late-decision')).toBeNull();
  });
});
