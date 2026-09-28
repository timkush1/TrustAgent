/**
 * One evaluator (SPEC §3.3 Evaluators detail): what it asks and how its answer becomes a risk,
 * where it runs, its calibration progress, the reliability chart ("agreement with labels, not
 * ground truth"), and its versions with a diff between any two. Writing a new version, adopting a
 * calibration and labelling arrive in P4; their buttons say so.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowLeft, Copy, Eye, Scale } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { DiffView } from '@/components/common/DiffView';
import { DisabledAction } from '@/components/common/DisabledReason';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { KeyValueList } from '@/components/common/KeyValue';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { DegradedState } from '@/components/common/DegradedState';
import { Term } from '@/components/common/Term';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { useNow } from '@/components/common/useNow';
import { Chip } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, Section } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { evaluatorQuery, evaluatorVersionQuery, isNotServed, reliabilityQuery } from '@/data/queries';
import { useAppMode } from '@/data/useSystem';
import { evaluatorCategoryLabel, evaluatorOriginLabel, stageLabel } from '@/domain/controlPresentation';
import { formatCount, formatDateTime, formatRelativeTime, formatRisk } from '@/domain/format';
import { copy } from '@/domain/presentation';
import type { EvaluatorDetail, TimeWindow } from '@/types/api';
import { CalibrationMeter } from './CalibrationMeter';
import { evidenceNeedLabel, P4_REASONS, questionTypeLabel } from './evaluatorWords';
import { ReliabilityChart } from './ReliabilityChart';

type LabelSource = 'human' | 'uniform_audit' | 'all';

const LABEL_SOURCES: Record<LabelSource, string> = {
  human: 'Your labels',
  uniform_audit: 'Deep-audit sample',
  all: 'Both',
};

function specText(spec: unknown): string {
  return JSON.stringify(spec, null, 2);
}

function Reliability({ detail }: { detail: EvaluatorDetail }) {
  const ds = useDataSource();
  const { demo } = useAppMode();
  const [timeWindow, setTimeWindow] = useState<TimeWindow>('7d');
  const [labels, setLabels] = useState<LabelSource>('human');
  const ref = `${detail.id}@${detail.latest.version}`;
  const q = useQuery({ ...reliabilityQuery(ds, { evaluator: ref, window: timeWindow, bins: 10, labels }), retry: false });

  let body;
  if (q.isPending) body = <Skeleton className="h-60 w-full" />;
  else if (q.isError) {
    body = isNotServed(q.error) ? (
      <p className="text-sm text-fg-muted">This proxy does not report reliability yet.</p>
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  } else if (q.data.n === 0) {
    body = (
      <EmptyState icon={Scale} title="No labelled answers in this window" className="py-8">
        <p>Reliability compares the risk the fast check gave with how answers were labelled. Label answers in a trace (“Was this right?”) to see it.</p>
      </EmptyState>
    );
  } else {
    const n = q.data.n_by_source;
    body = (
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,30rem)_minmax(0,1fr)]">
        <ReliabilityChart data={q.data} />
        <div className="space-y-3">
          <KeyValueList
            items={[
              { label: 'Labelled answers', value: formatCount(q.data.n), numeric: true },
              { label: 'From your labels', value: formatCount(n.human ?? 0), numeric: true },
              { label: 'From the deep-audit sample', value: formatCount(n.uniform_audit ?? 0), numeric: true, hint: 'judge labels, a weaker signal' },
              { label: 'Calibration error', value: q.data.ece !== null ? formatRisk(q.data.ece) : null, numeric: true, hint: 'ECE, lower is better' },
              { label: 'Fast-check version', value: q.data.snapshot, mono: true },
            ]}
          />
          <EvidenceBadge
            label={demo ? 'simulated' : 'measured'}
            basis={`Labels on your own traffic for fast-check version ${q.data.snapshot ?? 'unknown'}: agreement with labels, not ground truth.`}
            subject="Reliability"
          />
        </div>
      </div>
    );
  }

  return (
    <Card>
      <CardHeader
        title="How well its risk matches your labels"
        description={
          <>
            Bars on the dashed line mean a risk of 0.8 is a problem about 8 times in 10. <Term id="calibration">Calibration</Term> moves them
            there.
          </>
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <SegmentedControl<TimeWindow>
              aria-label="Window"
              size="sm"
              options={[
                { value: '7d', label: '7d' },
                { value: '30d', label: '30d' },
              ]}
              value={timeWindow}
              onValueChange={setTimeWindow}
            />
            <Select<LabelSource>
              aria-label="Labels from"
              value={labels}
              onValueChange={(v) => v && setLabels(v)}
              options={(Object.keys(LABEL_SOURCES) as LabelSource[]).map((k) => ({ value: k, label: LABEL_SOURCES[k] }))}
              className="w-44"
            />
          </div>
        }
      />
      <CardContent>{body}</CardContent>
    </Card>
  );
}

function Versions({ detail }: { detail: EvaluatorDetail }) {
  const ds = useDataSource();
  const now = useNow();
  const sorted = [...detail.versions].sort((a, b) => b.version - a.version);
  const latest = detail.latest.version;
  const [from, setFrom] = useState(String(sorted[1]?.version ?? latest));
  const [to, setTo] = useState(String(latest));
  const a = useQuery({ ...evaluatorVersionQuery(ds, detail.id, Number(from)), enabled: sorted.length > 1 });
  const b = useQuery({ ...evaluatorVersionQuery(ds, detail.id, Number(to)), enabled: sorted.length > 1 });
  const options = sorted.map((v) => ({ value: String(v.version), label: `Version ${v.version}` }));

  return (
    <Section title="Versions" description="Versions never change once saved: a new question or threshold basis is a new version with its own calibration.">
      <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
        {sorted.map((v) => (
          <li key={v.version} className="flex flex-wrap items-start justify-between gap-2 px-4 py-2.5">
            <div className="min-w-0 space-y-0.5">
              <p className="text-sm font-medium text-fg">
                Version {v.version}
                {v.version === latest ? <span className="ml-2 text-xs font-normal text-fg-muted">latest</span> : null}
              </p>
              <p className="text-sm text-fg-muted">{v.changelog || 'No changelog'}</p>
            </div>
            <p className="text-xs text-fg-muted">
              {v.created_by} · <time dateTime={v.created_at} title={formatDateTime(v.created_at)}>{formatRelativeTime(v.created_at, now)}</time>
            </p>
          </li>
        ))}
      </ul>
      {sorted.length > 1 ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <Field label="Compare">
              <Select value={from} onValueChange={(v) => v && setFrom(v)} options={options} className="w-40" />
            </Field>
            <Field label="with">
              <Select value={to} onValueChange={(v) => v && setTo(v)} options={options} className="w-40" />
            </Field>
          </div>
          {a.isPending || b.isPending ? (
            <Skeleton className="h-40 w-full" />
          ) : a.isError || b.isError ? (
            <ErrorState
              error={a.error ?? b.error}
              onRetry={() => {
                void a.refetch();
                void b.refetch();
              }}
            />
          ) : (
            <DiffView label={`Version ${from} → version ${to}`} before={specText(a.data.spec)} after={specText(b.data.spec)} emptyText="The two versions are the same." />
          )}
        </div>
      ) : (
        <p className="text-sm text-fg-muted">This is the first version, so there is nothing to compare yet.</p>
      )}
    </Section>
  );
}

function Detail({ detail }: { detail: EvaluatorDetail }) {
  const spec = detail.latest.spec;
  const outcomes = spec.outcomes as { warn_at?: number; critical_at?: number | null; block_eligible?: boolean };
  return (
    <div className="space-y-6">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Card>
          <CardHeader title="What it checks" />
          <CardContent>
            <KeyValueList
              items={[
                { label: 'Category', value: evaluatorCategoryLabel(detail.category) },
                { label: 'Made by', value: evaluatorOriginLabel(detail.origin) },
                { label: 'Question', value: questionTypeLabel(spec.question.type) },
                { label: 'Documents', value: evidenceNeedLabel((spec.evidence as { requires?: string }).requires) },
                {
                  label: 'Flags from',
                  value: outcomes.warn_at !== undefined ? formatRisk(outcomes.warn_at) : null,
                  numeric: true,
                  hint: 'risk',
                },
                {
                  label: 'Can block from',
                  value: outcomes.block_eligible && outcomes.critical_at != null ? formatRisk(outcomes.critical_at) : 'Never blocks',
                  numeric: true,
                },
                { label: 'Calibration key', value: detail.latest.calibration_key, mono: true, copy: true },
              ]}
            />
          </CardContent>
        </Card>
        <Card>
          <CardHeader title="Runs on" />
          <CardContent className="space-y-4">
            {detail.attached_routes.length === 0 ? (
              <p className="text-sm text-fg-muted">No route runs this check yet.</p>
            ) : (
              <ul className="flex flex-wrap gap-1.5">
                {detail.attached_routes.map((r) => (
                  <li key={`${r.route}-${r.stage}`}>
                    <Chip icon={r.stage === 'shadow' ? Eye : undefined}>
                      <span className="font-mono">{r.route}</span> · {stageLabel(r.stage)}
                    </Chip>
                  </li>
                ))}
              </ul>
            )}
            <Link to="/app/policies" className="inline-flex text-sm font-medium text-fg underline underline-offset-2">
              Change routes in Policies
            </Link>
            <div className="border-t border-border pt-3">
              <h4 className="mb-2 text-xs font-medium text-fg-muted">Calibration</h4>
              <CalibrationMeter progress={detail.calibration_progress} />
            </div>
          </CardContent>
        </Card>
      </div>
      <Reliability detail={detail} />
      <Section collapsible title="The question it asks" description="The evaluator/v1 spec sent to the fast check, as stored.">
        <CodeBlock label={`${detail.id}@${detail.latest.version}`} code={specText(spec)} maxHeight="28rem" />
      </Section>
      <Versions detail={detail} />
    </div>
  );
}

export function EvaluatorDetailPage({ id }: { id: string }) {
  const ds = useDataSource();
  const q = useQuery(evaluatorQuery(ds, id));
  const showLoading = useDelayedLoading(q.isPending);
  const detail = q.data;

  let body;
  if (q.isPending) {
    body = showLoading ? (
      <div aria-busy="true" className="space-y-4">
        <span role="status" className="sr-only">
          {copy.states.loading}
        </span>
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    ) : null;
  } else if (q.isError) {
    body =
      isApiError(q.error) && q.error.status === 404 && q.error.code === 'not_found' ? (
        <EmptyState title={`No evaluator called ${id}`}>
          <Link to="/app/evaluators" className="font-medium text-fg underline underline-offset-2">
            Back to the library
          </Link>
        </EmptyState>
      ) : isNotServed(q.error) ? (
        <DegradedState title="Evaluator details are not available on this proxy" reason={q.error.message} />
      ) : (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      );
  } else if (detail) {
    body = <Detail detail={detail} />;
  }

  return (
    <div>
      <Link to="/app/evaluators" className="mb-3 inline-flex items-center gap-1 text-sm text-fg-muted hover:text-fg">
        <ArrowLeft aria-hidden="true" className="size-4" />
        Evaluators
      </Link>
      <PageHeader
        title={detail?.name ?? id}
        description={detail?.latest.spec.description}
        actions={
          detail ? (
            <>
              <DisabledAction reason={P4_REASONS.studio} layout="stacked">
                {(p) => (
                  <Button {...p}>
                    <Copy aria-hidden="true" />
                    Duplicate and edit
                  </Button>
                )}
              </DisabledAction>
              <DisabledAction reason={P4_REASONS.adopt} layout="stacked">
                {(p) => (
                  <Button {...p}>
                    {copy.evaluators.adopt_calibration}
                  </Button>
                )}
              </DisabledAction>
            </>
          ) : null
        }
      />
      {body}
    </div>
  );
}
