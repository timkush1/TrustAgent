/**
 * What happened to a Playground request, in order (SPEC §3.3 Playground): your model's answer,
 * the fast check's decision, what your app received (the block message or a fallback answer in
 * place of the model's, next to it; and every response header the run returned), and the deep
 * audit, which keeps updating from the live stream until it finishes. A streamed run also says
 * when the first token arrived and when the fast check decided (D69: possibly before the answer
 * finished), as measured in this browser.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, Bot, Gavel, Inbox, Search } from 'lucide-react';
import { useMemo, type CSSProperties, type ReactNode } from 'react';
import { useDataSource } from '@/app/services';
import { RiskBar, RiskBarLegend } from '@/components/charts/RiskBar';
import { CodeBlock } from '@/components/common/CodeBlock';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { evaluatorName } from '@/components/trace/traceView';
import { Spinner } from '@/components/ui/spinner';
import { AuditState } from '@/components/outcome/AuditState';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { evaluatorsQuery } from '@/data/queries';
import { useStreamStore } from '@/data/stream/streamStore';
import { stageLabel } from '@/domain/controlPresentation';
import { formatDurationMs, formatRisk } from '@/domain/format';
import {
  checkedAgainst,
  copy,
  levelWord,
  reasonCopy,
  statusReason,
  toneVar,
  traceOutcome,
} from '@/domain/presentation';
import { evalNameMap } from '@/domain/words/evaluators';
import { cn } from '@/lib/cn';
import type { GateAction, GateDecisionView, PlaygroundResult, TraceAuditStatus } from '@/types/api';
import { deliveredAction, receivedCaption } from './playgroundModel';

/** When a streamed run's events arrived, in ms after the request was sent (null: not yet). */
export interface RunTiming {
  /** Time to first token. */
  firstTokenMs: number | null;
  /** When the fast check's decision arrived. */
  decisionMs: number | null;
  /** More of the answer streamed after the decision: the check decided before the answer ended. */
  decidedMidStream: boolean;
}

export interface TimelineState {
  mode: 'chat' | 'audit';
  /** Streamed text so far (chat mode with streaming). */
  tokens: string;
  decision: GateDecisionView | null;
  result: PlaygroundResult | null;
  running: boolean;
  /** The answer came as token events. */
  streamed?: boolean;
  timing?: RunTiming;
  /** A demo stack: its model answers are mocks, so their timings are simulated. */
  demo?: boolean;
  /**
   * Replaces the deep-audit lane (the static live Playground: the deep audit does not run in the
   * browser, so there is no audit to follow and no trace to open).
   */
  deepAudit?: ReactNode;
}

const BROWSER_BASIS = 'Measured in this browser from sending the request, so it includes the network to the gateway.';
const MOCK_BASIS = 'Timed in this browser, but the model or the fast check that answered is a mock: real ones take their own time.';

/**
 * "First token after 340 ms" or "Decided after 820 ms, …", with its value label: Measured, or
 * Simulated when a mock produced what was timed (never mock output presented as measured).
 */
function Timing({ text, subject, simulated }: { text: string; subject: string; simulated: boolean }) {
  return (
    <p className="flex flex-wrap items-center gap-2 text-sm text-fg-muted tabular">
      <span>{text}</span>
      <EvidenceBadge label={simulated ? 'simulated' : 'measured'} basis={simulated ? MOCK_BASIS : BROWSER_BASIS} subject={subject} />
    </p>
  );
}

function Step({
  icon: Icon,
  title,
  status,
  children,
  last = false,
}: {
  icon: typeof Bot;
  title: string;
  /** `off`: this step does not run here (the static live Playground's deep audit). */
  status: 'done' | 'active' | 'waiting' | 'off';
  children?: ReactNode;
  last?: boolean;
}) {
  return (
    <li className="relative grid grid-cols-[1.75rem_minmax(0,1fr)] gap-x-3">
      {last ? null : <span aria-hidden="true" className="absolute top-8 bottom-0 left-3.5 w-px -translate-x-1/2 bg-border" />}
      <span
        aria-hidden="true"
        className={cn(
          'relative z-10 flex size-7 items-center justify-center rounded-full border',
          status === 'done' ? 'border-border-strong bg-surface text-fg' : 'border-dashed border-border-strong bg-surface-sunken text-fg-subtle',
        )}
      >
        {status === 'active' ? <Spinner size="sm" /> : <Icon className="size-3.5" />}
      </span>
      <div className="min-w-0 space-y-2 pb-5">
        <h3 className="pt-1 text-sm font-semibold text-fg">
          {title}{' '}
          <span className="sr-only">{status === 'done' ? '(done)' : status === 'active' ? '(in progress)' : status === 'off' ? '(not run here)' : '(waiting)'}</span>
        </h3>
        {children}
      </div>
    </li>
  );
}

function DecisionView({ d, delivered }: { d: GateDecisionView; delivered: GateAction | null }) {
  // The checks are named from the evaluator library, as in Traffic: a renamed check or a Studio
  // copy is never shown by its id. While the library is not at hand, the humanized id stands in.
  const ds = useDataSource();
  const evaluators = useQuery({ ...evaluatorsQuery(ds), retry: false });
  const names = useMemo(() => evalNameMap(evaluators.data?.items ?? []), [evaluators.data]);
  // One outcome per request, as in Traffic: what the app received once the run says so.
  const outcome = traceOutcome({
    gate_status: d.status,
    action: delivered ?? d.action,
    would_action: d.would_action,
    skip_reason: d.skip_reason,
    checked_against: d.evidence_summary,
    degraded: d.degraded,
  });
  const rows = Object.values(d.evaluators).filter((e) => !e.internal);
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <OutcomePill outcome={outcome.id} />
        <span className="text-sm text-fg">{outcome.headlineText}</span>
      </div>
      <p className="text-xs text-fg-muted">
        {checkedAgainst(d.evidence_summary).text}
        {d.gate_ms !== null ? ` · ${formatDurationMs(d.gate_ms)}` : ''}
      </p>
      {d.reasons.length > 0 ? (
        <ul className="list-disc space-y-0.5 pl-5 text-sm text-fg">
          {d.reasons.map((r) => (
            <li key={r}>{reasonCopy(r)}</li>
          ))}
        </ul>
      ) : null}
      {rows.length > 0 ? (
        <div className="space-y-2">
          <ul className="space-y-2">
            {rows.map((e) => {
              const level = e.level ? levelWord(e.level) : null;
              return (
                <li key={e.ref} className="space-y-1">
                  <p className="flex items-baseline justify-between gap-2 text-sm">
                    <span className="font-medium text-fg">
                      {evaluatorName(e.ref, names)}
                      {e.stage === 'shadow' ? <span className="ml-1.5 text-xs font-normal text-fg-muted">{stageLabel('shadow')}</span> : null}
                    </span>
                    {level ? (
                      <span
                        style={{ '--tone': `var(${toneVar(level.tone)})` } as CSSProperties}
                        className={cn('text-xs', level.level === 'ok' ? 'text-fg-muted' : 'font-medium text-(--tone)')}
                      >
                        {level.label} <span className="ml-1 text-fg-muted tabular">{formatRisk(e.risk)}</span>
                      </span>
                    ) : (
                      <span className="text-xs text-fg-muted">{statusReason(e.reason) ?? copy.trace.not_checked_no_documents}</span>
                    )}
                  </p>
                  {e.status === 'assessed' && e.level && e.risk !== null ? (
                    <RiskBar
                      risk={e.risk}
                      level={e.level}
                      warnAt={e.thresholds.warn_at}
                      criticalAt={e.thresholds.critical_at}
                      label={`${evaluatorName(e.ref, names)} risk`}
                    />
                  ) : null}
                </li>
              );
            })}
          </ul>
          <RiskBarLegend />
        </div>
      ) : null}
    </div>
  );
}

function Received({ result }: { result: PlaygroundResult }) {
  const caption = receivedCaption(result);
  const headers = Object.entries(result.headers);
  return (
    <div className="space-y-3">
      {caption ? (
        <div className="grid gap-2 md:grid-cols-2">
          <figure className="space-y-1">
            <figcaption className="text-xs text-fg-muted">Your model wrote</figcaption>
            <p className="rounded-md border border-border bg-surface-sunken px-3 py-2 text-sm whitespace-pre-wrap text-fg-muted line-through decoration-fg-subtle">
              {result.response_text}
            </p>
          </figure>
          <figure className="space-y-1">
            <figcaption className="text-xs font-medium text-fg">{caption}</figcaption>
            <p className="rounded-md border border-border-strong bg-surface px-3 py-2 text-sm whitespace-pre-wrap text-fg">{result.delivered_text}</p>
          </figure>
        </div>
      ) : (
        <p className="rounded-md border border-border bg-surface px-3 py-2 text-md whitespace-pre-wrap text-fg">{result.delivered_text || '—'}</p>
      )}
      {headers.length > 0 ? (
        <details className="text-sm">
          <summary className="cursor-pointer text-xs font-medium text-fg-muted hover:text-fg">Response headers your app saw</summary>
          <CodeBlock
            className="mt-2"
            code={headers.map(([k, v]) => `${k}: ${v}`).join('\n')}
            label={`${headers.length} header${headers.length === 1 ? '' : 's'}`}
            copyLabel="Copy the response headers"
            maxHeight="16rem"
          />
        </details>
      ) : null}
    </div>
  );
}

function DeepAudit({ result }: { result: PlaygroundResult }) {
  // The live stream moves the audit along (queued → running → completed) after the run returns.
  const live = useStreamStore((s) => s.traces[result.trace_id]?.audit);
  const audit: { status: TraceAuditStatus; verdict: null | NonNullable<typeof live>['verdict'] } = live ?? {
    status: result.audit_status,
    verdict: null,
  };
  return (
    <div className="flex flex-wrap items-center gap-3">
      <AuditState audit={audit} />
      <Link
        to="/app/traffic/$traceId"
        params={{ traceId: result.trace_id }}
        className="inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2"
      >
        Open the full trace
        <ArrowRight aria-hidden="true" className="size-3.5" />
      </Link>
    </div>
  );
}

export function ResultTimeline({ state }: { state: TimelineState }) {
  const { mode, tokens, decision, result, running, streamed = false, timing, demo = false, deepAudit } = state;
  const d = result?.decision ?? decision;
  // A mock fast check (simulated checks, or the demo) decides on a script, not at Jev's speed.
  const mockDecision = demo || d?.decision_model?.backend === 'mock';
  const firstToken = streamed && timing?.firstTokenMs != null ? timing.firstTokenMs : null;
  const decidedAt = streamed && timing?.decisionMs != null ? timing.decisionMs : null;
  return (
    <ol aria-label="What happened to the request" className="min-w-0">
      <Step
        icon={Bot}
        title={mode === 'audit' ? 'The answer to check' : 'Your model answered'}
        status={result || tokens || mode === 'audit' ? 'done' : running ? 'active' : 'waiting'}
      >
        {mode === 'audit' ? (
          <p className="text-sm text-fg-muted">Checked as written: no model was called to produce it.</p>
        ) : result?.upstream ? (
          <p className="text-sm text-fg-muted tabular">
            HTTP {String(result.upstream.status)} · {formatDurationMs(result.upstream.ms)}
          </p>
        ) : null}
        {firstToken !== null ? <Timing text={`First token after ${formatDurationMs(firstToken)}`} subject="Time to first token" simulated={demo} /> : null}
        {tokens && !result ? <p className="rounded-md border border-border bg-surface px-3 py-2 text-md whitespace-pre-wrap text-fg">{tokens}</p> : null}
      </Step>
      <Step icon={Search} title={copy.tiers.fast_check} status={d ? 'done' : running ? 'active' : 'waiting'}>
        {decidedAt !== null ? (
          <Timing
            text={`Decided after ${formatDurationMs(decidedAt)}${timing?.decidedMidStream ? ', while the answer was still streaming' : ''}`}
            subject="When the fast check decided"
            simulated={mockDecision}
          />
        ) : null}
        {d ? <DecisionView d={d} delivered={deliveredAction(result)} /> : running ? <p className="text-sm text-fg-muted">Checking the answer…</p> : null}
      </Step>
      <Step icon={Inbox} title="What your app received" status={result ? 'done' : running ? 'active' : 'waiting'}>
        {result ? <Received result={result} /> : null}
      </Step>
      <Step icon={Gavel} title={copy.tiers.deep_audit} status={deepAudit !== undefined ? 'off' : result ? 'done' : 'waiting'} last>
        {deepAudit !== undefined ? deepAudit : result ? <DeepAudit result={result} /> : null}
      </Step>
    </ol>
  );
}
