/**
 * The 1280×640 social card (D115; SPEC §8.2 og-spec), rendered at `/og` only in a
 * `VITE_LAUNCH_ROUTES=1` build and captured by `npm run og` (DX-DOCS). Owned by WEB-REPLAY.
 *
 * Left: the wordmark and the landing's H1. Right: a real crop of the replay's featured answer
 * (featuredTrace(): the flagged refund answer the deep audit found contradicted): its outcome pill
 * and headline, what it was checked against, the question, the answer with its claim marks, and
 * the evidence that contradicts it. 64 px margins; the chrome is achromatic, colour only for the
 * outcome and the verdict. A simulated recording says so on the card (a Simulated chip), so a
 * capture of it can never pass for model output; a recorded one carries its own label, which says
 * whether its answers were scripted or generated.
 *
 * `data-og-ready` appears once the trace and the fonts have loaded: the capture waits for it.
 */
import { useQuery } from '@tanstack/react-query';
import { Fragment, useEffect, useState, type CSSProperties } from 'react';
import { useDataSource } from '@/app/services';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { VerdictBadge } from '@/components/outcome/VerdictBadge';
import { answerFindings } from '@/components/trace/traceView';
import { traceQuery } from '@/data/queries';
import { useReplayStore } from '@/data/replay/replayStore';
import { checkedAgainst, claimVerdictView, copy, traceOutcome } from '@/domain/presentation';
import { claimRuns, segmentClaims } from '@/domain/segmentClaims';
import type { ClaimView, TraceDetail } from '@/types/api';

/** A number and the word after it ("30 days", "$15"), kept by split() as odd parts. */
const QUANTITY = /(\$?\d[\d,.]*(?:\s+[A-Za-z]+)?)/;

const OG_WIDTH = 1280;
const OG_HEIGHT = 640;

/** The answer with its located claims marked in their verdict style (no links: it is a picture). */
function MarkedAnswer({ detail }: { detail: TraceDetail }) {
  const text = detail.body.response_text ?? detail.response_preview ?? '';
  const audit = detail.audit_detail;
  const located = audit?.llm_response == null || audit.llm_response === text;
  const { segments } = segmentClaims(text, audit?.claims ?? [], { located, findings: answerFindings(detail.decisions[0] ?? null) });
  return (
    <p className="text-[1.75rem] leading-snug font-medium text-fg" data-og-answer>
      {claimRuns(segments).map((run) => {
        if (!run.verdict) return <Fragment key={run.start}>{run.pieces.map((p) => p.text).join('')}</Fragment>;
        const view = claimVerdictView(run.claims[0].status);
        const Icon = view.icon;
        return (
          // The icon stays on the line of the words it marks.
          <mark key={run.start} data-v={view.mark ?? undefined} className="whitespace-nowrap" style={{ '--vc': `var(${view.colorVar})` } as CSSProperties}>
            {run.text}
            <Icon aria-hidden="true" className="ms-1 inline size-6 align-[-0.2em] text-(--vc)" />
          </mark>
        );
      })}
    </p>
  );
}

/** The document line that contradicts the answer: "refund-policy.md: “Annual plans … 30 days …”". */
function contradiction(claims: readonly ClaimView[]): { doc: string | null; quote: string } | null {
  const c = claims.find((x) => x.status === 'CONTRADICTED');
  if (!c) return null;
  const span = c.evidence_spans.find((s) => s.text.trim() !== '');
  if (span) return { doc: span.doc_id || null, quote: span.text };
  const quote = c.evidence.find((e) => e.trim() !== '');
  return quote ? { doc: null, quote } : null;
}

function Crop({ detail }: { detail: TraceDetail }) {
  const outcome = traceOutcome({ ...detail, degraded: detail.decisions[0]?.degraded ?? null });
  const evidence = checkedAgainst(detail.checked_against ?? detail.decisions[0]?.evidence_summary);
  const line = contradiction(detail.audit_detail?.claims ?? []);
  return (
    <div className="flex h-full min-h-0 flex-col gap-5 rounded-2xl border border-border-strong bg-surface p-8 shadow-sm" data-og-crop>
      <div className="flex items-center gap-4">
        <OutcomePill outcome={outcome} className="px-3 py-1 text-base [&_svg]:size-5" />
        <p className="text-xl font-semibold text-fg">{outcome.headlineText}</p>
      </div>
      <p className="text-lg text-fg-muted" data-og-checked>
        {evidence.text}
      </p>
      {detail.body.user_request ? (
        <div className="space-y-1.5">
          <p className="text-sm font-medium tracking-wide text-fg-muted uppercase">Question</p>
          <p className="text-lg text-fg">{detail.body.user_request}</p>
        </div>
      ) : null}
      <div className="space-y-1.5">
        <p className="text-sm font-medium tracking-wide text-fg-muted uppercase">Answer</p>
        <MarkedAnswer detail={detail} />
      </div>
      {line ? (
        <div className="mt-auto space-y-2 border-t border-border pt-5" data-og-evidence>
          <VerdictBadge verdict="CONTRADICTED" />
          <p className="text-lg text-fg">
            {line.doc ? <span className="font-medium">{line.doc}: </span> : null}“
            {/* Quantities stand out ("30 days"), so the difference with the answer reads at a glance. */}
            {line.quote.split(QUANTITY).map((part, i) => (i % 2 === 1 ? <strong key={i}>{part}</strong> : part))}”
          </p>
        </div>
      ) : null}
    </div>
  );
}

export default function OgCard() {
  const ds = useDataSource();
  const featured = useReplayStore((s) => s.featuredTraceId);
  const kind = useReplayStore((s) => s.kind);
  const label = useReplayStore((s) => s.label);
  const status = useReplayStore((s) => s.status);
  const error = useReplayStore((s) => s.error);
  const q = useQuery({ ...traceQuery(ds, featured ?? ''), enabled: featured !== null });
  const [fonts, setFonts] = useState(false);
  useEffect(() => {
    let live = true;
    void (document.fonts?.ready ?? Promise.resolve()).then(() => {
      if (live) setFonts(true);
    });
    return () => {
      live = false;
    };
  }, []);
  const ready = fonts && q.isSuccess;

  return (
    <main
      data-og-card
      data-og-ready={ready ? '' : undefined}
      data-recording-kind={kind ?? undefined}
      style={{ width: OG_WIDTH, height: OG_HEIGHT }}
      className="grid shrink-0 grid-cols-[30rem_minmax(0,1fr)] gap-16 overflow-hidden bg-bg p-16 text-fg"
    >
      <div className="flex flex-col justify-between">
        <p className="flex items-center gap-3 text-2xl font-semibold tracking-tight text-fg">
          <img src={`${import.meta.env.BASE_URL}favicon.svg`} alt="" className="size-9" />
          {copy.product.name}
        </p>
        <div className="space-y-6">
          <h1 className="text-[3.5rem] leading-[1.05] font-semibold tracking-[-0.04em] text-fg">{copy.product.tagline}</h1>
          <p className="text-xl text-fg-muted">Open-source hallucination gateway for LLM apps</p>
        </div>
        {kind === 'mock' ? (
          // Honest numbers: a capture of the simulated tape says what it is.
          <p data-og-simulated className="inline-flex w-fit items-center gap-2 rounded-full border border-border-strong px-3 py-1 text-base text-fg-muted">
            {copy.recording.mock}
          </p>
        ) : label ? (
          // A recorded tape's own label (Go recording.recordedLabel): when, with which models, and
          // whether the answers were scripted or generated. The checks are real; the answer on a
          // demo tape is usually the scenario's scripted one, so the card never calls it a real answer.
          <p data-og-recording-label className="text-base text-fg-muted">
            {label}
          </p>
        ) : null}
      </div>
      {q.isSuccess ? (
        <Crop detail={q.data} />
      ) : status === 'error' || q.isError ? (
        <p role="alert" className="self-center text-lg text-fg">
          The demo recording could not be read: {q.error?.message ?? error}
        </p>
      ) : featured === null && status === 'ready' ? (
        <p role="alert" className="self-center text-lg text-fg">
          The recording has no caught answer to show.
        </p>
      ) : (
        <div aria-busy="true" className="rounded-2xl border border-border bg-surface" />
      )}
    </main>
  );
}
