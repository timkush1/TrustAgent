/**
 * "Show exact Jev request" (SPEC §3.3 "Trace detail" 3, the glass box; §2.4 #12; D63):
 * what the fast check was asked for one attempt. Collapsed, it costs nothing; opened, it asks the
 * proxy to recompile the request from the stored text (GET /api/traces/:id/decision-request) and
 * shows:
 *
 * - the summary line ("1 Jev call · 4 questions · 212 ms · $0.0002 · typesafe/jev-1.13-20260917"),
 *   the cost labelled with where it comes from;
 * - how the fast check split its answer across each question's options (ProbabilityBars);
 * - the state and questions it was sent, and the raw request;
 * - a match badge: the server's `matches_recorded`, cross-checked against the request fingerprints
 *   recorded with the decision (`CompileInfo.request_sha256`) whenever the decision carries them.
 *
 * States: a key without the content scope (not fetched), stored text that expired (409), a proxy
 * that cannot recompile (404), redacted text, and a simulated (mock) fast check.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { CheckCheck, ChevronRight, CircleAlert, FlaskConical } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { DisabledReason } from '@/components/common/DisabledReason';
import { ProbabilityBars } from '@/components/charts/ProbabilityBars';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { decisionRequestQuery } from '@/data/queries/evaluators';
import { useSystemStatus } from '@/data/useSystem';
import { copy, humanizeId } from '@/domain/presentation';
import { evalMismatchText, evalRecompileText } from '@/domain/words/evaluators';
import type { DecisionRequestView, GateDecisionView, JevRequest } from '@/types/api';
import { askedCount, documentExcerpts, jevRequestSummary, questionsIn, recompileMatches } from './jevRequest';
import { evaluatorName } from './traceView';

function textOf(value: unknown): string {
  return typeof value === 'string' ? value : JSON.stringify(value, null, 2);
}

function StateValue({ value }: { value: unknown }) {
  const excerpts = documentExcerpts(value);
  const box = 'max-h-40 overflow-auto rounded-sm bg-surface-sunken px-2 py-1 text-xs whitespace-pre-wrap text-fg';
  if (!excerpts) return <dd className={box}>{textOf(value)}</dd>;
  return (
    <dd>
      <ul className="space-y-1.5">
        {excerpts.map((e, i) => (
          <li key={`${e.id ?? 'document'}-${i}`} className={box}>
            <span className="block font-mono text-2xs text-fg-muted">{e.id ?? `Document ${i + 1}`}</span>
            {e.text}
          </li>
        ))}
      </ul>
    </dd>
  );
}

/** A question key ("proof_muree135__v1") in words, through the same names as the answers. */
function questionName(key: string, names?: ReadonlyMap<string, string>): string {
  const m = /^(.*)__v(\d+)$/.exec(key);
  return m ? evaluatorName(`${m[1]}@${m[2]}`, names) : evaluatorName(key, names);
}

function RequestView({ request, index, total, names }: { request: JevRequest; index: number; total: number; names?: ReadonlyMap<string, string> }) {
  const label = total > 1 ? `Request ${index + 1} of ${total}` : 'Request';
  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <h4 className="text-xs font-medium text-fg-muted">{label}: what it read</h4>
        <dl className="space-y-2">
          {Object.entries(request.state).map(([field, value]) => (
            <div key={field} className="space-y-0.5">
              <dt className="text-xs text-fg-muted">{humanizeId(field)}</dt>
              <StateValue value={value} />
            </div>
          ))}
        </dl>
      </div>
      <div className="space-y-1.5">
        <h4 className="text-xs font-medium text-fg-muted">{label}: what it was asked</h4>
        <ul className="space-y-1.5">
          {Object.entries(request.questions).map(([key, q]) => {
            const question = (q ?? {}) as { instructions?: unknown; type?: unknown };
            return (
              <li key={key} className="text-sm">
                <span className="font-medium text-fg">{questionName(key, names)}</span>
                {typeof question.instructions === 'string' ? <span className="text-fg-muted"> — {question.instructions}</span> : null}
              </li>
            );
          })}
        </ul>
      </div>
      <CodeBlock code={JSON.stringify(request, null, 2)} label={`${label} · ${request.model}`} copyLabel="Copy the request" maxHeight="20rem" />
    </div>
  );
}

function MatchBadge({ view, decision }: { view: DecisionRequestView; decision: GateDecisionView }) {
  const matches = recompileMatches(view, decision);
  const why = matches
    ? null
    : view.matches_recorded
      ? "The rebuilt request's fingerprint differs from the one recorded with the decision."
      : evalMismatchText(view.mismatch_reason);
  return (
    <div className="space-y-1">
      <Badge icon={matches ? CheckCheck : CircleAlert} emphasis="quiet">
        {evalRecompileText(matches)}
      </Badge>
      {why ? <p className="text-xs text-fg-muted">{why}</p> : null}
    </div>
  );
}

function Probabilities({ decision, names }: { decision: GateDecisionView; names?: ReadonlyMap<string, string> }) {
  const rows = Object.values(decision.evaluators).filter((r) => r.probabilities && Object.keys(r.probabilities).length > 0);
  if (rows.length === 0) return null;
  return (
    <div className="space-y-2">
      <h4 className="text-xs font-medium text-fg-muted">How it answered</h4>
      <div className="grid gap-4 @3xl:grid-cols-2">
        {rows.map((r) => (
          <div key={r.ref} className="space-y-1">
            <p className="text-sm font-medium text-fg">{evaluatorName(r.ref, names)}</p>
            <ProbabilityBars
              label={evaluatorName(r.ref, names)}
              options={Object.entries(r.probabilities ?? {}).map(([key, p]) => ({ key, label: humanizeId(key), p }))}
              top={r.top}
            />
          </div>
        ))}
      </div>
    </div>
  );
}

function Body({ traceId, decision, redacted, names }: { traceId: string; decision: GateDecisionView; redacted: boolean; names?: ReadonlyMap<string, string> }) {
  const ds = useDataSource();
  const { status } = useSystemStatus();
  const scopes = status?.auth.caller_scopes;
  const noContent = Boolean(scopes && !scopes.includes('content'));
  const q = useQuery({ ...decisionRequestQuery(ds, traceId, { attempt: decision.attempt }), enabled: !noContent });

  let content;
  const contentReason = {
    text: 'Needs a key with the content scope: the request holds the prompt and your documents.',
    fix: (
      <Link to="/app/setup" className="font-medium text-fg underline underline-offset-2">
        Sign in with another key
      </Link>
    ),
  };
  if (noContent) {
    content = <DisabledReason reason={contentReason} />;
  } else if (q.isPending) {
    content = (
      <div role="status" className="space-y-2">
        <span className="sr-only">Rebuilding the request…</span>
        <Skeleton className="h-5 w-1/2" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  } else if (q.isError) {
    const err = q.error;
    const code = isApiError(err) ? err.code : null;
    const status = isApiError(err) ? err.status : 0;
    content =
      code === 'body_expired' ? (
        <p className="text-sm text-fg-muted">This request&apos;s stored text has expired, so the exact request can&apos;t be rebuilt.</p>
      ) : status === 401 || status === 403 ? (
        <DisabledReason reason={contentReason} />
      ) : code === 'not_implemented' ? (
        // This build has no gateway to ask (the static demo says how to run one).
        <p className="text-sm text-fg-muted">{err.message}</p>
      ) : status === 404 || status === 501 ? (
        <p className="text-sm text-fg-muted">This proxy can&apos;t rebuild the request: it is an older version, or it kept no decision for this attempt.</p>
      ) : (
        <p className="text-sm text-fg">
          The request could not be rebuilt: {err.message}
          {code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{code}</span> : null}
        </p>
      );
  }

  return <JevRequestBody decision={decision} view={q.isSuccess && !noContent ? q.data : undefined} redacted={redacted} names={names} pending={content} />;
}

/**
 * The glass box for a request already in hand: the summary line, the probabilities, the match
 * badge and each request sent. Fetches nothing, so the static live Playground shows the request it
 * just sent through it (LV-6); `pending` is shown instead of the requests while `view` is absent.
 */
export function JevRequestBody({
  decision,
  view,
  redacted = false,
  names,
  pending,
}: {
  decision: GateDecisionView;
  view?: DecisionRequestView;
  redacted?: boolean;
  names?: ReadonlyMap<string, string>;
  pending?: ReactNode;
}) {
  const simulated = decision.decision_model?.backend === 'mock';
  const questions = view ? questionsIn(view) : askedCount(decision);
  return (
    <div className="mt-2 space-y-4 rounded-md border border-border px-3 py-3" data-testid="jev-request">
      <p className="text-sm text-fg tabular">{jevRequestSummary(decision, questions)}</p>
      {simulated ? (
        <p className="flex items-start gap-1.5 text-xs text-fg-muted">
          <FlaskConical aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
          {copy.value_labels.simulated}: a mock fast check answered this request, so no call reached Jev and the probabilities are made up.
        </p>
      ) : null}
      {redacted ? (
        <p className="text-xs text-fg-muted">
          {copy.trace.flags.text_redacted}: the rebuilt request reads the redacted text, so it is not byte-for-byte what was sent.
        </p>
      ) : null}
      <Probabilities decision={decision} names={names} />
      {view ? (
        <div className="space-y-4">
          <MatchBadge view={view} decision={decision} />
          {view.requests.map((r, i) => (
            <RequestView key={i} request={r} index={i} total={view.requests.length} names={names} />
          ))}
        </div>
      ) : (
        pending
      )}
    </div>
  );
}

export function JevRequestPanel({
  traceId,
  decision,
  redacted,
  names,
}: {
  traceId: string;
  decision: GateDecisionView;
  redacted: boolean;
  names?: ReadonlyMap<string, string>;
}) {
  // Nothing is fetched until the viewer opens it.
  const [open, setOpen] = useState(false);
  return (
    <details className="group/jev text-sm" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-sm text-xs font-medium text-fg-muted hover:text-fg [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden="true" className="size-3.5 transition-transform group-open/jev:rotate-90" />
        {copy.trace.show_jev_request}
      </summary>
      {open ? <Body traceId={traceId} decision={decision} redacted={redacted} names={names} /> : null}
    </details>
  );
}
