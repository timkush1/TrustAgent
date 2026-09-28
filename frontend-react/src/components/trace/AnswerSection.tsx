/**
 * The question and the answer as the model gave it (SPEC §3.3 "Trace detail" 2, left). When the
 * deep audit arrives, each located claim is marked in the answer (DESIGN.md §3: verdict hue,
 * underline style and icon, never color alone). A mark is a link to its row in the Claims section;
 * claims the audit could not place in the text are listed under the answer as "not located in
 * text". Detector marks follow in P3.
 */
import type { CSSProperties, MouseEvent } from 'react';
import { VerdictBadge } from '@/components/outcome/VerdictBadge';
import { Tooltip } from '@/components/ui/tooltip';
import { claimVerdictView, copy } from '@/domain/presentation';
import { segmentClaims, type AnswerSegment } from '@/domain/segmentClaims';
import type { ClaimView } from '@/types/api';
import { auditHasResult, claimRowId, type TraceView } from './traceView';

/** Scrolls to a claim's row and moves focus there (the row is focusable with tabIndex -1). */
function jumpToClaim(event: MouseEvent<HTMLAnchorElement>, id: string) {
  const row = document.getElementById(id);
  if (!row) return;
  event.preventDefault();
  row.scrollIntoView?.({ block: 'nearest' }); // absent in jsdom
  row.focus({ preventScroll: true });
}

function claimLine(claim: ClaimView): string {
  const verdict = claimVerdictView(claim.status).label;
  return claim.claim ? `${verdict}: ${claim.claim}` : verdict;
}

function ClaimMark({ segment, idPrefix }: { segment: AnswerSegment; idPrefix: string }) {
  const lead = segment.claims[0];
  const view = claimVerdictView(lead.status);
  const Icon = view.icon;
  const target = claimRowId(idPrefix, lead.index);
  const others = segment.claims.length - 1;
  const label = segment.claims.map(claimLine).join(' · ');
  return (
    <mark
      data-v={view.mark ?? undefined}
      data-fuzzy={segment.fuzzy ? '' : undefined}
      style={{ '--vc': `var(${view.colorVar})` } as CSSProperties}
    >
      <Tooltip label={<span className="block max-w-72">{label}</span>}>
        <a
          href={`#${target}`}
          onClick={(e) => jumpToClaim(e, target)}
          className="rounded-[2px] text-inherit no-underline"
        >
          {segment.text}
          <Icon aria-hidden="true" className="ms-0.5 inline size-3.5 align-[-0.15em] text-(--vc)" />
          <span className="sr-only">
            {`, ${view.label.toLowerCase()} claim${others > 0 ? ` and ${others} more` : ''}${segment.fuzzy ? ', approximate location' : ''}`}
          </span>
        </a>
      </Tooltip>
    </mark>
  );
}

function AnswerText({ text, segments, idPrefix }: { text: string; segments: AnswerSegment[]; idPrefix: string }) {
  if (!segments.some((s) => s.verdict)) return <p className="text-md whitespace-pre-wrap text-fg">{text}</p>;
  return (
    <p className="text-md whitespace-pre-wrap text-fg" data-testid="answer-marked">
      {segments.map((s) =>
        s.verdict ? <ClaimMark key={s.start} segment={s} idPrefix={idPrefix} /> : <span key={s.start}>{s.text}</span>,
      )}
    </p>
  );
}

function NotLocated({ claims, idPrefix }: { claims: ClaimView[]; idPrefix: string }) {
  return (
    <div className="space-y-1 border-t border-border pt-3">
      <p className="text-xs font-medium text-fg-muted">Claims {copy.trace.claim_not_located}</p>
      <ul aria-label={`Claims ${copy.trace.claim_not_located}`} className="space-y-1">
        {claims.map((c) => {
          const target = claimRowId(idPrefix, c.index);
          return (
            <li key={c.index} className="flex flex-wrap items-baseline gap-x-2 text-sm">
              <VerdictBadge verdict={c.status} />
              <a
                href={`#${target}`}
                onClick={(e) => jumpToClaim(e, target)}
                className="text-fg underline decoration-border-strong underline-offset-2 hover:decoration-fg"
              >
                {c.claim ?? 'Hidden: this key cannot see claim text.'}
              </a>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

export function AnswerSection({
  view,
  claimIdPrefix = '',
}: {
  view: Pick<TraceView, 'question' | 'answer' | 'summary' | 'audit'>;
  /** Prefix of the Claims section's row ids (claimRowId), unique per page. */
  claimIdPrefix?: string;
}) {
  const { question, answer, summary, audit } = view;
  const claims = audit && auditHasResult(audit.status) ? audit.claims : [];
  // Spans refer to the text the engine audited; mark only when that is the text shown here.
  const located = Boolean(answer && !answer.preview && (audit?.llm_response == null || audit.llm_response === answer.text));
  const { segments, unlocated } = answer ? segmentClaims(answer.text, claims, { located }) : { segments: [], unlocated: [] };
  return (
    <section aria-labelledby="answer-title" className="space-y-3">
      <h2 id="answer-title" className="text-base font-semibold text-fg">
        Answer
      </h2>
      {summary.redacted ? (
        <p className="text-sm text-fg-muted">Hidden: this key can't see prompts and answers.</p>
      ) : (
        <div className="space-y-3 rounded-lg border border-border bg-surface p-4">
          {question ? (
            <div className="space-y-0.5">
              <p className="text-xs font-medium text-fg-muted">Question</p>
              <p className="text-sm whitespace-pre-wrap text-fg">{question}</p>
            </div>
          ) : null}
          <div className="space-y-0.5">
            {question ? <p className="text-xs font-medium text-fg-muted">Answer</p> : null}
            {answer ? (
              <AnswerText text={answer.text} segments={segments} idPrefix={claimIdPrefix} />
            ) : (
              <p className="text-sm text-fg-muted">The answer text is not stored for this request.</p>
            )}
            {answer?.preview ? (
              <p className="text-xs text-fg-muted">Preview: the full text is not stored for this request.</p>
            ) : null}
          </div>
          {unlocated.length > 0 ? <NotLocated claims={unlocated} idPrefix={claimIdPrefix} /> : null}
        </div>
      )}
    </section>
  );
}
