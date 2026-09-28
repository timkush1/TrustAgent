/**
 * Inline claim marks in the answer (DESIGN.md §3): hue, underline style, icon and words, one tab
 * stop per mark, and a "not located in text" list for claims without a span.
 */
import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { TooltipProvider } from '@/components/ui/tooltip';
import { axeViolations, formatViolations } from '@/test/axe';
import { s0Audit, s0Trace, scenario } from '@/test/scenarios';
import type { AuditDetail, ClaimView } from '@/types/api';
import { AnswerSection } from './AnswerSection';

const REFUND = 'refund-contradiction-flag' as const;

function view(audit: AuditDetail, text = scenario(REFUND).response) {
  return {
    question: 'How long do I have to get a refund?',
    answer: { text, preview: false },
    summary: s0Trace(REFUND, 'audited'),
    audit,
  };
}

function withClaims(claims: ClaimView[]): AuditDetail {
  return { ...s0Audit(REFUND), claims };
}

const renderAnswer = (v: ReturnType<typeof view>) =>
  render(
    <TooltipProvider>
      <AnswerSection view={v} claimIdPrefix="t-" />
    </TooltipProvider>,
  );

describe('AnswerSection claim marks', () => {
  it('marks the refund claim at "60 days" with its verdict style, and passes axe', async () => {
    renderAnswer(view(s0Audit(REFUND)));
    const link = screen.getByRole('link', { name: '60 days, contradicted claim' });
    expect(link).toHaveAttribute('href', '#t-claim-0');
    const mark = link.closest('mark');
    expect(mark).toHaveAttribute('data-v', 'contradicted');
    expect(mark).not.toHaveAttribute('data-fuzzy');
    expect(mark?.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    expect(formatViolations(await axeViolations(document.body))).toEqual([]);
  });

  it('lists a paraphrased claim as not located in text instead of marking it', async () => {
    const [refund] = s0Audit(REFUND).claims;
    renderAnswer(view(withClaims([{ ...refund, span: null }])));
    expect(screen.queryByRole('link', { name: /contradicted claim/ })).toBeNull();
    const list = screen.getByRole('list', { name: 'Claims not located in text' });
    const item = within(list).getByRole('listitem');
    expect(item).toHaveTextContent('Contradicted');
    expect(within(item).getByRole('link', { name: refund.claim ?? '' })).toHaveAttribute('href', '#t-claim-0');
    expect(formatViolations(await axeViolations(document.body))).toEqual([]);
  });

  it('does not place spans in a text the engine did not audit', () => {
    renderAnswer(view(s0Audit(REFUND), 'A different answer than the audited one.'));
    expect(screen.queryByRole('link', { name: /contradicted claim/ })).toBeNull();
    expect(screen.getByRole('list', { name: 'Claims not located in text' })).toBeInTheDocument();
  });
});
