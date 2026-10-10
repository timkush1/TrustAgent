/**
 * The question and the answer as the model gave it (SPEC §3.3 "Trace detail" 2, left). The
 * instant checks' findings are marked at once: an achromatic line and a scan icon, because a
 * detector is not a verdict. When the deep audit arrives, each located claim is marked too
 * (DESIGN.md §3: verdict hue, underline style and icon, never color alone); where they overlap, the
 * finding nests inside the claim's mark. A claim mark is a link to its row in the Claims section;
 * claims the audit could not place in the text are listed under the answer as "not located in
 * text". When the user received something else (a fallback answer or the block message), it
 * follows the answer as "What the user received instead"; the marks stay on the model's answer,
 * which is what the checks judged.
 */
import { CornerDownRight, ScanSearch, ShieldX } from 'lucide-react';
import { Fragment, type CSSProperties, type MouseEvent } from 'react';
import { Term } from '@/components/common/Term';
import { VerdictBadge } from '@/components/outcome/VerdictBadge';
import { Tooltip } from '@/components/ui/tooltip';
import { claimVerdictView, copy, fillTemplate, reasonCopy } from '@/domain/presentation';
import { claimRuns, segmentClaims, type AnswerSegment, type ClaimRun, type MarkFinding } from '@/domain/segmentClaims';
import type { ClaimView, GateDecisionView } from '@/types/api';
import { answerFindings, auditHasResult, claimRowId, fallbackCheckResult, receivedId, TRACE_ANCHORS, type TraceView } from './traceView';

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

const lowerFirst = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);

/** The instant-check words for the detectors behind some findings, once each ("Links to a URL not in your documents"). */
function findingWords(findings: readonly MarkFinding[]): string[] {
  return [...new Set(findings.map((f) => f.detector))].map((d) => reasonCopy(`det:${d}`));
}

/** ", instant checks: links to a URL not in your documents" for an accessible name; empty without findings. */
function findingName(words: readonly string[]): string {
  return words.length > 0 ? `, ${copy.tiers.instant_checks.toLowerCase()}: ${words.map(lowerFirst).join('; ')}` : '';
}

/**
 * A piece of text an instant check marked. Inside a claim mark it is styling only (the claim's
 * link names and explains it); on its own it repeats its words in a tooltip and, once per finding,
 * for screen readers. The icon follows the finding's last piece.
 */
function DetectorMark({ piece, nested }: { piece: AnswerSegment; nested: boolean }) {
  const ends = piece.findings.some((f) => f.end === piece.end);
  const mark = (
    <mark data-det={[...new Set(piece.findings.map((f) => f.detector))].join(' ')}>
      {piece.text}
      {ends ? <ScanSearch aria-hidden="true" className="ms-0.5 inline size-3.5 align-[-0.15em] text-fg-muted" /> : null}
    </mark>
  );
  if (nested) return mark;
  const words = findingWords(piece.findings);
  return (
    <>
      <Tooltip label={<span className="block max-w-72">{`${copy.tiers.instant_checks}: ${words.join(' · ')}`}</span>}>{mark}</Tooltip>
      {ends ? <span className="sr-only">{` (${findingName(words).slice(2)})`}</span> : null}
    </>
  );
}

function ClaimMark({ run, idPrefix }: { run: ClaimRun; idPrefix: string }) {
  const lead = run.claims[0];
  const view = claimVerdictView(lead.status);
  const Icon = view.icon;
  const target = claimRowId(idPrefix, lead.index);
  const others = run.claims.length - 1;
  const words = findingWords(run.pieces.flatMap((p) => p.findings));
  const label = [...run.claims.map(claimLine), ...words.map((w) => `${copy.tiers.instant_checks}: ${w}`)].join(' · ');
  return (
    <mark
      data-v={view.mark ?? undefined}
      data-fuzzy={run.fuzzy ? '' : undefined}
      style={{ '--vc': `var(${view.colorVar})` } as CSSProperties}
    >
      <Tooltip label={<span className="block max-w-72">{label}</span>}>
        <a
          href={`#${target}`}
          onClick={(e) => jumpToClaim(e, target)}
          // The name starts with the visible words (WCAG 2.5.3) and adds what only the style shows.
          aria-label={`${run.text}, ${view.label.toLowerCase()} claim${others > 0 ? ` and ${others} more` : ''}${run.fuzzy ? ', approximate location' : ''}${findingName(words)}`}
          className="rounded-[2px] text-inherit no-underline"
        >
          {run.pieces.map((p) =>
            p.findings.length > 0 ? <DetectorMark key={p.start} piece={p} nested /> : <Fragment key={p.start}>{p.text}</Fragment>,
          )}
          <Icon aria-hidden="true" className="ms-0.5 inline size-3.5 align-[-0.15em] text-(--vc)" />
        </a>
      </Tooltip>
    </mark>
  );
}

function AnswerText({ text, segments, idPrefix }: { text: string; segments: AnswerSegment[]; idPrefix: string }) {
  if (!segments.some((s) => s.verdict || s.findings.length > 0)) return <p className="text-md whitespace-pre-wrap text-fg">{text}</p>;
  return (
    <p className="text-md whitespace-pre-wrap text-fg" data-testid="answer-marked">
      {claimRuns(segments).map((run) =>
        run.verdict ? (
          <ClaimMark key={run.start} run={run} idPrefix={idPrefix} />
        ) : (
          <Fragment key={run.start}>
            {run.pieces.map((p) =>
              p.findings.length > 0 ? <DetectorMark key={p.start} piece={p} nested={false} /> : <span key={p.start}>{p.text}</span>,
            )}
          </Fragment>
        ),
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

function ReceivedInstead({
  delivered,
  text,
  check,
  documents,
  id,
}: {
  delivered: 'fallback' | 'blocked';
  text: string | null;
  check: GateDecisionView | null;
  /** The request carried documents; without them the fallback prompt has none to write from. */
  documents: boolean;
  id: string;
}) {
  const Icon = delivered === 'blocked' ? ShieldX : CornerDownRight;
  const note =
    delivered === 'blocked'
      ? copy.trace.received_blocked
      : fillTemplate(documents ? copy.trace.received_fallback : copy.trace.received_fallback_no_documents, {
          result: fallbackCheckResult(check),
        });
  return (
    <div id={id} tabIndex={-1} data-delivered={delivered} className="space-y-1.5 border-t border-border pt-3">
      <p className="flex items-center gap-1.5 text-xs font-medium text-fg">
        <Icon aria-hidden="true" className="size-3.5 shrink-0 text-fg-muted" />
        {copy.trace.received_instead}
      </p>
      {text ? (
        <p data-testid="received-text" className="rounded-md border border-border-strong bg-surface px-3 py-2 text-md whitespace-pre-wrap text-fg">
          {text}
        </p>
      ) : (
        <p className="text-sm text-fg-muted">{copy.trace.received_not_stored}</p>
      )}
      <p className="text-xs text-fg-muted">{note}</p>
    </div>
  );
}

export function AnswerSection({
  view,
  claimIdPrefix = '',
}: {
  view: Pick<TraceView, 'question' | 'answer' | 'summary' | 'audit' | 'decision' | 'delivered' | 'deliveredText' | 'attempts'>;
  /** Prefix of the Claims section's row ids (claimRowId), unique per page. */
  claimIdPrefix?: string;
}) {
  const { question, answer, summary, audit, decision, delivered, deliveredText, attempts } = view;
  const claims = audit && auditHasResult(audit.status) ? audit.claims : [];
  // Spans refer to the text the engine audited; mark only when that is the text shown here.
  const located = Boolean(answer && !answer.preview && (audit?.llm_response == null || audit.llm_response === answer.text));
  // Findings refer to the gated text, the stored answer (never a list preview).
  const findings = answer && !answer.preview ? answerFindings(decision) : [];
  const { segments, unlocated } = answer ? segmentClaims(answer.text, claims, { located, findings }) : { segments: [], unlocated: [] };
  const marked = segments.some((s) => s.findings.length > 0);
  const replaced = delivered === 'fallback' || delivered === 'blocked';
  const answerLabel = replaced ? copy.trace.answer_not_sent : question ? 'Answer' : null;
  return (
    <section aria-labelledby={TRACE_ANCHORS.answer} className="scroll-mt-4 space-y-3">
      <h2 id={TRACE_ANCHORS.answer} className="scroll-mt-4 text-base font-semibold text-fg">
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
            {answerLabel ? <p className="text-xs font-medium text-fg-muted">{answerLabel}</p> : null}
            {answer ? (
              <AnswerText text={answer.text} segments={segments} idPrefix={claimIdPrefix} />
            ) : (
              <p className="text-sm text-fg-muted">The answer text is not stored for this request.</p>
            )}
            {answer?.preview ? (
              <p className="text-xs text-fg-muted">Preview: the full text is not stored for this request.</p>
            ) : null}
            {marked ? (
              <p className="flex items-center gap-1 pt-1 text-xs text-fg-muted">
                <span aria-hidden="true" className="inline-flex items-center gap-0.5">
                  <span className="h-0.5 w-3 rounded-full bg-fg-muted" />
                  <ScanSearch className="size-3.5" />
                </span>
                <Term id="instant_checks">{copy.trace.instant_marks}</Term>
              </p>
            ) : null}
          </div>
          {unlocated.length > 0 ? <NotLocated claims={unlocated} idPrefix={claimIdPrefix} /> : null}
          {replaced ? (
            <ReceivedInstead
              delivered={delivered}
              text={deliveredText}
              check={attempts.find((d) => d.attempt === 1) ?? null}
              documents={Boolean((summary.checked_against ?? decision?.evidence_summary)?.used.length)}
              id={receivedId(claimIdPrefix)}
            />
          ) : null}
        </div>
      )}
    </section>
  );
}
