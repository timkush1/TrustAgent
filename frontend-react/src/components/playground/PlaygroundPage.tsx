/**
 * Playground (SPEC §3.3, full build): "Try it now". Send a chat request through your model, or
 * check a pasted answer, on any route; watch it go through the fast check, see exactly what your
 * app would have received, and follow the deep audit. The form stays filled between runs, and the
 * same request is shown as a curl command that reproduces the chosen mode, with the key left in an
 * environment variable. A streamed run shows when the first token arrived and when the fast check
 * decided, which can be before the answer finished streaming (D69).
 *
 * Page contract: Run needs an admin on a writable instance and says so otherwise; errors show the
 * server's message with what to do (rate limit with its wait, missing connection with the link);
 * the routes list degrades to free text when the policy cannot be read. An older proxy that does
 * not serve runs (404/501) gets a degraded state instead of an error, Run says why it is off, and
 * the curl command still sends the same request through the gateway.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Play, Square } from 'lucide-react';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useDataSource } from '@/app/services';
import { useGatewayOrigin } from '@/components/common/useGatewayOrigin';
import { CodeBlock } from '@/components/common/CodeBlock';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { PageHeader } from '@/components/common/PageHeader';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { NAV_ITEMS } from '@/components/layout/nav';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { isApiError } from '@/data/errors';
import { isNotServed, policiesQuery } from '@/data/queries';
import { useAppMode, useHealth, useSystemStatus } from '@/data/useSystem';
import type { GateDecisionView, PlaygroundResult } from '@/types/api';
import { playgroundCommand } from './playgroundCurl';
import { buildRequest, formProblem } from './playgroundModel';
import { PlaygroundFormView } from './PlaygroundForm';
import { usePlaygroundStore } from './playgroundStore';
import { ResultTimeline, type RunTiming } from './ResultTimeline';

interface RunState {
  running: boolean;
  tokens: string;
  decision: GateDecisionView | null;
  result: PlaygroundResult | null;
  error: unknown;
  mode: 'chat' | 'audit';
  /** The answer streamed as token events. */
  streamed: boolean;
  timing: RunTiming;
}

const NO_TIMING: RunTiming = { firstTokenMs: null, decisionMs: null, decidedMidStream: false };

const IDLE: RunState = { running: false, tokens: '', decision: null, result: null, error: null, mode: 'chat', streamed: false, timing: NO_TIMING };

/** Run is off once this proxy has said it does not serve POST /api/playground/run (an older proxy). */
const NOT_SERVED_REASON: Reason = { text: 'This proxy is older than the dashboard and does not run Playground requests' };

/** 404/501 to a run: a proxy from before the Playground endpoint. What still works, and where. */
function RunNotServed({ version }: { version: string | null }) {
  return (
    <DegradedState
      title="This proxy does not run Playground requests"
      reason={`It does not serve POST /api/playground/run, so it is older than this dashboard${version ? ` (proxy version ${version})` : ''}. Update the proxy to run requests from here.`}
      stillWorks="In chat mode the curl command below sends the same request through the gateway; its checks and deep audit appear in Traffic."
      action={
        <Link to="/app/traffic" className="text-sm font-medium text-fg underline underline-offset-2">
          Open Traffic
        </Link>
      }
    />
  );
}

/** The server's message plus what the viewer can do about it. */
function RunError({ error }: { error: unknown }) {
  const { message, code } = describeError(error);
  let next: ReactNode = null;
  if (isApiError(error)) {
    if (error.code === 'rate_limited' || error.status === 429) {
      const wait = error.retryAfterMs !== null ? Math.ceil(error.retryAfterMs / 1000) : null;
      next = <>The Playground allows 10 runs a minute{wait !== null ? `; try again in ${wait} s` : ''}.</>;
    } else if (['decision_model_unavailable', 'credentials_unavailable', 'no_judge'].includes(error.code)) {
      next = (
        <Link to="/app/settings/$section" params={{ section: 'connections' }} className="font-medium text-fg underline underline-offset-2">
          Open Connections
        </Link>
      );
    } else if (error.code === 'budget_exhausted') {
      next = (
        <Link to="/app/settings/$section" params={{ section: 'budgets' }} className="font-medium text-fg underline underline-offset-2">
          Raise today’s budget
        </Link>
      );
    } else if (error.status === 502 || error.code === 'upstream_error') {
      next = <>Your model returned an error. Check the model name, or use “Check an answer” to try the checks without a model call.</>;
    }
  }
  return (
    <div role="alert" className="space-y-1 rounded-lg border border-border bg-surface px-4 py-3">
      <p className="text-sm font-medium text-fg">The run failed: {message}</p>
      {code ? <p className="font-mono text-2xs text-fg-subtle">{code}</p> : null}
      {next ? <p className="text-sm text-fg-muted">{next}</p> : null}
    </div>
  );
}

export function PlaygroundPage() {
  const ds = useDataSource();
  const origin = useGatewayOrigin();
  const form = usePlaygroundStore((s) => s.form);
  const runUnavailable = usePlaygroundStore((s) => s.runUnavailable);
  const setRunUnavailable = usePlaygroundStore((s) => s.setRunUnavailable);
  const { status } = useSystemStatus();
  const health = useHealth();
  const { demo } = useAppMode();
  const writeReason = useWriteReason('admin');
  const policies = useQuery(policiesQuery(ds));
  const [run, setRun] = useState<RunState>(IDLE);
  const abort = useRef<AbortController | null>(null);
  // Leaving the page stops a run still in flight (a streamed answer would otherwise keep reading).
  useEffect(() => () => abort.current?.abort(), []);

  const routes = policies.data ? policies.data.effective_routes.map((r) => r.name).filter((n) => n !== 'default') : policies.isError ? null : [];
  const maxChars = status?.limits.max_text_chars ?? null;
  const problem = formProblem(form, maxChars);
  // Mock checks on a demo or test-mode stack answer an unedited example with its scripted answers.
  const scripted = demo || status?.test_mode === true;
  const request = buildRequest(form, { scenario: scripted });
  const command = playgroundCommand(request, origin);
  const runReason: Reason | null = runUnavailable ? NOT_SERVED_REASON : (writeReason ?? (problem ? { text: problem } : null));

  const start = async () => {
    abort.current?.abort();
    const controller = new AbortController();
    abort.current = controller;
    const sentAt = performance.now();
    const elapsed = () => Math.round(performance.now() - sentAt);
    setRun({ ...IDLE, running: true, mode: request.mode, streamed: request.stream === true });
    try {
      const result = request.stream
        ? await ds.streamPlayground(
            request,
            {
              onToken: (t) => {
                const at = elapsed();
                setRun((r) => ({
                  ...r,
                  tokens: r.tokens + t,
                  // A token after the decision: the fast check decided while the answer was still streaming.
                  timing: { ...r.timing, firstTokenMs: r.timing.firstTokenMs ?? at, decidedMidStream: r.timing.decidedMidStream || r.decision !== null },
                }));
              },
              onDecision: (d) => {
                const at = elapsed();
                setRun((r) => ({ ...r, decision: d, timing: { ...r.timing, decisionMs: r.timing.decisionMs ?? at } }));
              },
            },
            controller.signal,
          )
        : await ds.runPlayground(request, controller.signal);
      setRun((r) => ({ ...r, running: false, result, decision: result.decision }));
    } catch (error) {
      if (controller.signal.aborted) {
        setRun((r) => ({ ...r, running: false }));
      } else if (isNotServed(error)) {
        setRunUnavailable(true);
        setRun(IDLE);
      } else {
        setRun((r) => ({ ...r, running: false, error }));
      }
    } finally {
      if (abort.current === controller) abort.current = null;
    }
  };

  const started = run.running || run.result !== null || run.decision !== null || run.error !== null;

  return (
    <div>
      <PageHeader description={`${NAV_ITEMS[4].question}: send one request and follow every step the gateway takes.`} />
      <div className="grid gap-6 xl:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
        <Card>
          <CardHeader level={2} title="Request" description="Kept here between runs." />
          <CardContent className="space-y-5">
            <PlaygroundFormView
              routes={routes}
              maxChars={maxChars}
              busy={run.running}
              scripted={scripted}
              knowledge={status?.knowledge.enabled === true}
            />
            {/* Stays in view at the bottom of a long form. */}
            <div className="sticky bottom-0 z-10 -mx-4 -mb-4 flex flex-wrap items-center gap-2 rounded-b-lg border-t border-border bg-surface px-4 py-3">
              {run.running ? (
                <Button onClick={() => abort.current?.abort()}>
                  <Square aria-hidden="true" />
                  Stop
                </Button>
              ) : (
                <DisabledAction reason={runReason}>
                  {(p) => (
                    <Button variant="primary" onClick={() => void start()} {...p}>
                      <Play aria-hidden="true" />
                      {form.mode === 'audit' ? 'Check the answer' : 'Send the request'}
                    </Button>
                  )}
                </DisabledAction>
              )}
            </div>
          </CardContent>
        </Card>

        <div className="min-w-0 space-y-4">
          <Card>
            <CardHeader level={2} title="What happened" description={started ? undefined : 'Run the request to see each step here.'} />
            <CardContent aria-live="polite" aria-busy={run.running}>
              {runUnavailable ? <RunNotServed version={health.data?.version ?? null} /> : null}
              {run.error ? <RunError error={run.error} /> : null}
              {runUnavailable ? null : started ? (
                <div className={run.error ? 'mt-4' : undefined}>
                  <ResultTimeline
                    state={{
                      mode: run.mode,
                      tokens: run.tokens,
                      decision: run.decision,
                      result: run.result,
                      running: run.running,
                      streamed: run.streamed,
                      timing: run.timing,
                    }}
                  />
                </div>
              ) : (
                <ol className="space-y-2 text-sm text-fg-muted">
                  <li>1. Your model answers (or you paste an answer).</li>
                  <li>2. The fast check reads it against your documents.</li>
                  <li>3. You see exactly what your app would receive.</li>
                  <li>4. The deep audit checks it claim by claim.</li>
                </ol>
              )}
            </CardContent>
          </Card>
          <CodeBlock label="The same request with curl" code={command.code} maxHeight="20rem" />
          <p className="text-xs text-fg-muted">
            Calls <code className="font-mono text-fg">{command.endpoint}</code>. {command.keyNote}
          </p>
        </div>
      </div>
    </div>
  );
}
