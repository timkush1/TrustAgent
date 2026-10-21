/**
 * Overview (SPEC §3.3): "Is my AI answering safely, and what does checking cost?"
 *
 * Summary tab, top to bottom: the setup checklist; "Connect your app" while no request has arrived
 * (the page's empty state); the at-a-glance strip; "Requests by outcome"; "What was caught" next to
 * evidence coverage with its "Why not?" panel; the savings strip; the hallucination rate among
 * audited answers; Performance (collapsed); and "Connect your app" again for a second app.
 *
 * Page contract: the delayed skeleton, the server's message with Retry, the degraded state when
 * this proxy does not serve the overview, the demo copy and simulated labels, and no writes (so
 * nothing to disable in read-only mode).
 */
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { ArrowRight, Info } from 'lucide-react';
import { useDataSource, useRuntimeConfig } from '@/app/services';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { recordingDate, useReplayInfo } from '@/components/replay/useReplay';
import { useGatewayOrigin } from '@/components/common/useGatewayOrigin';
import { OutcomeTimeSeries } from '@/components/charts/OutcomeTimeSeries';
import { DegradedState } from '@/components/common/DegradedState';
import { LabelledRateView } from '@/components/common/LabelledRate';
import { PageHeader } from '@/components/common/PageHeader';
import { NAV_ITEMS } from '@/components/layout/nav';
import { ErrorState } from '@/components/common/StateViews';
import { SavingsPanel } from '@/components/savings/SavingsPanel';
import { Term } from '@/components/common/Term';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { Card, CardContent } from '@/components/ui/card';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Skeleton } from '@/components/ui/skeleton';
import { Tab, TabPanel, Tabs, TabsList } from '@/components/ui/tabs';
import { isNotServed, overviewQuery } from '@/data/queries';
import { useStreamStore } from '@/data/stream/streamStore';
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import { copy } from '@/domain/presentation';
import type { OverviewStats, TimeWindow } from '@/types/api';
import { AtAGlance } from './AtAGlance';
import { ConnectYourApp } from './ConnectYourApp';
import { EvidenceCoverageCard } from './EvidenceCoverageCard';
import { bucketFormatter, DEFAULT_WINDOW, outcomeBuckets, setupChecklist, TIME_WINDOWS } from './overviewModel';
import { heroDescription } from './heroChart';
import { PerformanceSection } from './PerformanceSection';
import { SavingsStrip } from './SavingsStrip';
import { SetupChecklist } from './SetupChecklist';
import { WhatWasCaught } from './WhatWasCaught';

export interface OverviewSearchParams {
  tab?: 'summary' | 'savings';
  window?: TimeWindow;
}


function OverviewSkeleton() {
  return (
    <div aria-busy="true" className="space-y-4">
      <span role="status" className="sr-only">
        {copy.states.loading}
      </span>
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-72 w-full" />
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-56 w-full" />
        <Skeleton className="h-56 w-full" />
      </div>
    </div>
  );
}

function Summary({ stats, timeWindow, demo, replay }: { stats: OverviewStats; timeWindow: TimeWindow; demo: boolean; replay: boolean }) {
  const origin = useGatewayOrigin();
  const liveTraces = useStreamStore((s) => s.order.length);
  const { status } = useSystemStatus();
  const received = liveTraces > 0 || stats.totals.requests > 0;
  const noTraffic = stats.totals.requests === 0;
  const label = demo || stats.include_mock ? 'simulated' : 'measured';

  const checklist = setupChecklist({
    status,
    requests: stats.totals.requests,
    firstRequestSeen: liveTraces > 0,
    evidenceRequests: noTraffic ? null : stats.evidence_coverage.with_evidence,
  });

  return (
    <div className="space-y-5">
      {/* A replay has no gateway to set up or connect to. */}
      {replay ? null : <SetupChecklist items={checklist} />}
      {noTraffic && !replay ? (
        <>
          <ConnectYourApp origin={origin} demo={demo} received={received} />
          <p className="text-sm text-fg-muted">
            No requests in {TIME_WINDOWS.find((w) => w.value === timeWindow)?.long ?? 'this window'}. {copy.states.empty_traffic}
          </p>
        </>
      ) : null}
      {noTraffic ? null : (
        <>
          {/* A replay plays a scripted mix chosen to show every outcome (D117): its counts keep
              their dataset label, and its rates are not a traffic rate. */}
          {replay ? (
            <p data-showcase-caveat className="flex items-start gap-1.5 text-xs text-fg-muted">
              <Info aria-hidden="true" className="mt-px size-3.5 shrink-0" />
              {copy.recording.showcase_caveat}
            </p>
          ) : null}
          <AtAGlance stats={stats} label={label} replay={replay} />
          <Card>
            <CardContent>
              <OutcomeTimeSeries
                title={copy.overview.hero_chart}
                description={heroDescription(stats, replay)}
                buckets={outcomeBuckets(stats.outcomes_series)}
                formatBucket={bucketFormatter(stats.bucket)}
                headingLevel={2}
              />
            </CardContent>
          </Card>
          <div className="grid items-start gap-4 lg:grid-cols-2">
            <WhatWasCaught stats={stats} span={replay ? 'over the replayed traffic' : undefined} />
            <EvidenceCoverageCard stats={stats} timeWindow={timeWindow} />
          </div>
          <SavingsStrip timeWindow={timeWindow} />
          <section aria-labelledby="audited-share-title" className="rounded-lg border border-border bg-surface px-4 py-3">
            <h2 id="audited-share-title" className="mb-1 text-sm font-semibold text-fg">
              Hallucinations among audited answers
            </h2>
            <p className="mb-2 text-xs text-fg-muted">
              The <Term id="deep_audit">deep audit</Term> mostly sees risky answers, so this share is not your traffic’s{' '}
              <Term id="hallucination_rate">hallucination rate</Term>: {copy.overview.audited_share_basis}.
            </p>
            <LabelledRateView
              rate={stats.audited_hallucination_share}
              subject="Hallucinations among audited answers"
              needsTemplate="Needs {min_n} audited answers (have {n})"
            />
          </section>
          <PerformanceSection stats={stats} />
          {replay ? null : <ConnectAnotherApp origin={origin} demo={demo} received={received} />}
        </>
      )}
    </div>
  );
}

/** "Connect your app" once traffic flows: folded under its own heading, for a second app. */
function ConnectAnotherApp(props: { origin: string; demo: boolean; received: boolean }) {
  return (
    <details className="group rounded-lg border border-border bg-surface">
      <summary className="cursor-pointer list-none px-4 py-3 text-sm font-semibold text-fg marker:hidden">
        <span className="inline-flex items-center gap-1.5">
          <ArrowRight aria-hidden="true" className="size-4 text-fg-muted transition-transform group-open:rotate-90" />
          {copy.connect_app.title}
        </span>
      </summary>
      <div className="px-4 pb-4">
        <ConnectYourApp {...props} className="border-0" />
      </div>
    </details>
  );
}

/** The static build's stand-in for the time window: the recording is one fixed stretch of time. */
function RecordingChip() {
  const info = useReplayInfo();
  if (info.status !== 'ready') return null;
  return (
    <span className="inline-flex flex-wrap items-center gap-2 text-xs text-fg-muted">
      <span>
        {info.traces} requests on {recordingDate(info.recordedAt)}
      </span>
      <EvidenceBadge label={info.kind === 'mock' ? 'simulated' : 'measured'} basis={info.label ?? undefined} subject="The replayed numbers" />
    </span>
  );
}

export function OverviewPage({ search }: { search: OverviewSearchParams }) {
  const replay = useRuntimeConfig().appMode === 'static';
  const ds = useDataSource();
  const navigate = useNavigate({ from: '/app/overview' });
  const { demo } = useAppMode();
  const timeWindow = search.window ?? DEFAULT_WINDOW;
  const tab = search.tab ?? 'summary';
  const q = useQuery(overviewQuery(ds, { window: timeWindow }));
  const showLoading = useDelayedLoading(q.isPending);

  let summary;
  if (q.isPending) {
    summary = showLoading ? <OverviewSkeleton /> : null;
  } else if (q.isError && q.data === undefined) {
    summary = isNotServed(q.error) ? (
      <DegradedState
        title="Overview numbers are not available on this proxy"
        reason="It does not serve GET /api/overview yet (an older version)."
        stillWorks="Every checked request is still listed in Traffic."
        action={
          <Link to="/app/traffic" className="inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2">
            Open Traffic
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        }
      />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  } else {
    summary = <Summary stats={q.data as OverviewStats} timeWindow={timeWindow} demo={demo} replay={replay} />;
  }

  return (
    <div>
      <PageHeader
        description={NAV_ITEMS[0].question}
        actions={
          replay ? (
            <RecordingChip />
          ) : (
          <SegmentedControl<TimeWindow>
            aria-label="Time window"
            size="sm"
            options={TIME_WINDOWS.map((w) => ({ value: w.value, label: w.label }))}
            value={timeWindow}
            onValueChange={(next) => void navigate({ search: (prev) => ({ ...prev, window: next === DEFAULT_WINDOW ? undefined : next }) })}
          />
          )
        }
      />
      <Tabs
        value={tab}
        onValueChange={(next) => void navigate({ search: (prev) => ({ ...prev, tab: next === 'summary' ? undefined : (next as 'savings') }) })}
      >
        <TabsList aria-label="Overview views" className="mb-1">
          <Tab value="summary">Summary</Tab>
          <Tab value="savings">Savings</Tab>
        </TabsList>
        <TabPanel value="summary">{summary}</TabPanel>
        <TabPanel value="savings">
          <SavingsPanel timeWindow={timeWindow} />
        </TabPanel>
      </Tabs>
    </div>
  );
}
