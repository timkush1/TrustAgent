/**
 * Inline claim marks in the answer (DESIGN.md §3): hue, underline style, icon and words, one tab
 * stop per mark, and a "not located in text" list for claims without a span.
 */
import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { TooltipProvider } from '@/components/ui/tooltip';
import { axeViolations, formatViolations } from '@/test/axe';
import { p3Detail } from '@/test/p3Fixtures';
import { s0Audit, s0Trace, scenario } from '@/test/scenarios';
import type { AuditDetail, ClaimView } from '@/types/api';
import { AnswerSection } from './AnswerSection';
import { buildTraceView, type TraceView } from './traceView';

const REFUND = 'refund-contradiction-flag' as const;

function view(audit: AuditDetail, text = scenario(REFUND).response): TraceView {
  return {
    question: 'How long do I have to get a refund?',
    answer: { text, preview: false },
    summary: s0Trace(REFUND, 'audited'),
    audit,
    decision: null,
    attempts: [],
    body: null,
    delivered: 'original',
    deliveredText: null,
  };
}

function withClaims(claims: ClaimView[]): AuditDetail {
  return { ...s0Audit(REFUND), claims };
}

const renderAnswer = (v: TraceView) =>
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

describe('AnswerSection: what the user received', () => {
  it('shows the fallback answer after the marked model answer, with how its check rated it', async () => {
    const v = buildTraceView({ detail: p3Detail('payments-fallback') }) as TraceView;
    renderAnswer(v);
    expect(screen.getByText("Your model's answer (not sent to the user)")).toBeInTheDocument();
    // The claim marks stay on the model's answer.
    const mark = screen.getByRole('link', { name: /^within 2 business days, contradicted claim/ });
    expect(mark.closest('mark')).toHaveAttribute('data-v', 'contradicted');
    const received = screen.getByText('What the user received instead').parentElement as HTMLElement;
    expect(received).toHaveAttribute('data-delivered', 'fallback');
    expect(within(received).getByTestId('received-text')).toHaveTextContent(
      'Approved refunds go back to your original payment method within 5 to 10 business days.',
    );
    expect(received).toHaveTextContent('A fallback answer written from your documents only, then checked again: low risk.');
    expect(received.querySelector('mark')).toBeNull();
    expect(formatViolations(await axeViolations(document.body))).toEqual([]);
  });

  it('shows the block message of a blocked answer', () => {
    renderAnswer(buildTraceView({ detail: p3Detail('help-center-link-block') }) as TraceView);
    const received = screen.getByText('What the user received instead').parentElement as HTMLElement;
    expect(received).toHaveAttribute('data-delivered', 'blocked');
    expect(within(received).getByTestId('received-text')).toHaveTextContent(
      "I can't verify that answer against our help articles, so I've held it back.",
    );
    expect(received).toHaveTextContent("The route's block message, sent in place of your model's answer.");
  });

  it('says when the text sent instead was not stored', () => {
    const v = buildTraceView({ detail: p3Detail('help-center-link-block') }) as TraceView;
    renderAnswer({ ...v, deliveredText: null });
    expect(screen.getByText('The text sent instead is not stored for this request.')).toBeInTheDocument();
  });

  it('shows nothing extra when the user received the model\'s answer', () => {
    renderAnswer(view(s0Audit(REFUND)));
    expect(screen.queryByText('What the user received instead')).toBeNull();
    expect(screen.getByText('Answer', { selector: 'p' })).toBeInTheDocument();
  });
});

describe('AnswerSection: instant-check marks', () => {
  function beforeAudit(id: 'help-center-link-block' | 'payments-fallback'): TraceView {
    const detail = p3Detail(id);
    return buildTraceView({
      detail: { ...detail, audit_detail: null, audit: { ...detail.audit, status: 'queued', verdict: null, verdict_counts: null } },
    }) as TraceView;
  }

  it('marks a finding at once, with its words for pointer and screen-reader users', async () => {
    renderAnswer(beforeAudit('help-center-link-block'));
    const answer = screen.getByTestId('answer-marked');
    const mark = answer.querySelector('mark[data-det~="urls_not_in_evidence"]') as HTMLElement;
    expect(mark).toHaveTextContent(/^https:\/\/acme\.example\/reset-password$/);
    expect(mark.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    expect(answer.querySelector('mark[data-v]')).toBeNull();
    expect(within(answer).getByText('(instant checks: links to a URL not in your documents)')).toHaveClass('sr-only');
    expect(screen.getByRole('button', { name: 'Marked by the instant checks' })).toBeInTheDocument();
    // The answer still reads as the model's text.
    expect(answer.textContent?.replace(' (instant checks: links to a URL not in your documents)', '')).toBe(
      'Open https://acme.example/reset-password and follow the steps. The reset link expires after 1 hour.',
    );
    expect(formatViolations(await axeViolations(document.body))).toEqual([]);
  });

  it('nests the finding inside the claim that covers it, and the claim link names both', async () => {
    renderAnswer(buildTraceView({ detail: p3Detail('help-center-link-block') }) as TraceView);
    const link = screen.getByRole('link', {
      name: 'Open https://acme.example/reset-password and follow the steps, unsupported claim, instant checks: links to a URL not in your documents',
    });
    expect(link.closest('mark')).toHaveAttribute('data-v', 'unsupported');
    expect(link.querySelector('mark[data-det]')).toHaveTextContent('https://acme.example/reset-password');
    // One claim mark (one link, one verdict icon) holds every piece of the claim.
    expect(screen.getAllByRole('link', { name: /unsupported claim/ })).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'The reset link expires after 1 hour, supported claim' })).toBeInTheDocument();
    expect(screen.queryByText(/\(instant checks:/)).toBeNull();
    expect(formatViolations(await axeViolations(document.body))).toEqual([]);
  });

  it('marks the number inside the contradicted claim of the replaced answer', () => {
    renderAnswer(buildTraceView({ detail: p3Detail('payments-fallback') }) as TraceView);
    const link = screen.getByRole('link', {
      name: 'within 2 business days, contradicted claim, instant checks: mentions a number not in your documents',
    });
    expect(link.querySelector('mark[data-det="numbers_not_in_evidence"]')).toHaveTextContent(/^2$/);
    expect(link.closest('mark')).toHaveTextContent('within 2 business days');
  });

  it('marks nothing on a list preview', () => {
    const v = beforeAudit('help-center-link-block');
    renderAnswer({ ...v, answer: { text: v.answer!.text, preview: true } });
    expect(document.querySelector('mark')).toBeNull();
    expect(screen.queryByText('Marked by the instant checks')).toBeNull();
  });
});
