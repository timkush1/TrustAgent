/**
 * Evaluator Studio (SPEC §7.3): write a check in plain English and take it from a draft
 * to a watched, then active, evaluator. Seven steps on one page: describe (AI draft or template),
 * review (Simple / Advanced / JSON with live lints), test on samples (with the estimate first and a
 * free threshold what-if), quick calibration, save a version, watch it on a route (plus the paid
 * test on past traffic), and calibrate for blocking.
 *
 * `/app/evaluators/studio` starts a new check; `/app/evaluators/studio/$id/$version` starts from a
 * saved version (a built-in or file check is copied as a new check of the viewer's own). Nothing is
 * saved or attached without an explicit action, and every paid action shows its cost first.
 */
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useMemo, useState } from 'react';
import { useDataSource } from '@/app/services';
import { type Reason } from '@/components/common/DisabledReason';
import { PageHeader } from '@/components/common/PageHeader';
import { ErrorState, LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { draftRouteNames } from '@/components/policies/policyDraft';
import { evaluatorQuery, evaluatorsQuery, evaluatorVersionQuery, policiesQuery } from '@/data/queries';
import { useSystemStatus } from '@/data/useSystem';
import { evaluatorOriginLabel } from '@/domain/controlPresentation';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { checkSpecDocument, isSavable, type CheckOptions } from '@/lib/evaluator/checkSpec';
import { evaluatorIdFrom } from '@/lib/studio/draftToSpec';
import { sameSpec } from '@/lib/studio/specFields';
import { runKeys } from '@/lib/studio/testBench';
import type { EvaluatorDetail, EvaluatorSpec, EvaluatorVersion, StudioDraftResponse, StudioSample } from '@/types/api';
import { CalibrateStep } from './CalibrateStep';
import { DescribeStep, type StudioStart } from './DescribeStep';
import { QuickCalibrationStep } from './QuickCalibrationStep';
import { ReviewStep } from './ReviewStep';
import { SaveStep, type StudioBase } from './SaveStep';
import { StudioStep } from './StudioStep';
import { StudioSteps, type StepStatus } from './StudioSteps';
import { TestBench, type TestRun } from './TestBench';
import { WatchStep } from './WatchStep';

interface Session {
  spec: EvaluatorSpec | null;
  samples: StudioSample[];
  draft: StudioDraftResponse | null;
  origin: string | null;
  route: string | null;
  base: StudioBase | null;
}

const EMPTY: Session = { spec: null, samples: [], draft: null, origin: null, route: null, base: null };

/** A session that starts from a saved version: a dashboard check gets its next version; a built-in or file check is copied. */
function sessionFrom(version: EvaluatorVersion, detail: EvaluatorDetail, taken: readonly string[]): Session {
  const base: StudioBase = { id: detail.id, latest: detail.latest_version, spec: detail.latest.spec, origin: detail.origin };
  if (detail.origin === 'dashboard') {
    return { ...EMPTY, spec: structuredClone(version.spec), base, origin: `Editing version ${version.version} of ${detail.name}` };
  }
  const copy = structuredClone(version.spec);
  delete copy.builtin;
  delete copy.changelog;
  return {
    ...EMPTY,
    spec: {
      ...copy,
      id: evaluatorIdFrom(`${detail.id} custom`, [...taken, detail.id]),
      version: 1,
      name: `${version.spec.name} (custom)`.slice(0, 80),
      parent: `${detail.id}@${version.version}`,
    },
    base: null,
    origin: `A copy of ${detail.name} version ${version.version} (${evaluatorOriginLabel(detail.origin).toLowerCase()}), saved as a new check`,
  };
}

function StudioEditor({ initial }: { initial: Session }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const { status } = useSystemStatus();
  const writeReason = useWriteReason('admin');
  const policies = useQuery(policiesQuery(ds));
  const evaluators = useQuery(evaluatorsQuery(ds, true));
  const [session, setSession] = useState<Session>(initial);
  const [run, setRun] = useState<TestRun | null>(null);
  const { spec, samples, base } = session;

  const taken = useMemo(() => {
    const ids = new Set(Object.values(BUILTIN_TEMPLATES).map((t) => t.id));
    for (const e of evaluators.data?.items ?? []) ids.add(e.id);
    return [...ids];
  }, [evaluators.data]);
  const checkOptions = useMemo<CheckOptions>(() => (evaluators.data ? { registry: taken } : {}), [evaluators.data, taken]);
  const routes = useMemo(() => (policies.data ? draftRouteNames(policies.data.yaml) : []), [policies.data]);

  const check = useMemo(() => (spec ? checkSpecDocument(spec, checkOptions) : null), [spec, checkOptions]);
  const savable = check ? isSavable(check) : false;
  const specReason: Reason | null = spec && !savable ? { text: 'Fix the errors in step 2 first' } : null;
  const decisionModel = status?.decision_model ?? null;
  const modelReason: Reason | null =
    decisionModel && (decisionModel.backend === 'off' || decisionModel.state === 'off')
      ? {
          text: 'No fast-check model is connected',
          fix: (
            <Link to="/app/settings/$section" params={{ section: 'connections' }} className="font-medium text-fg underline underline-offset-2">
              Connect one
            </Link>
          ),
        }
      : null;
  const savedRef = spec && base && base.id === spec.id && sameSpec(base.spec, spec) ? `${base.id}@${base.latest}` : null;
  const keys = spec ? runKeys(spec, samples) : null;
  const stale = run !== null && keys !== null && (run.specKey !== keys.specKey || run.samplesKey !== keys.samplesKey);

  const setSpec = (next: EvaluatorSpec) => setSession((s) => ({ ...s, spec: next }));
  const start = (st: StudioStart) => {
    setRun(null);
    setSession({ spec: st.spec, samples: st.samples, draft: st.draft, origin: st.origin, route: st.route, base: null });
  };
  /** The saved version becomes the base; an edit made while the save was on its way stays. */
  const onSaved = (version: EvaluatorVersion, sent: EvaluatorSpec) =>
    setSession((s) => ({
      ...s,
      spec: s.spec && !sameSpec(s.spec, sent) ? s.spec : version.spec,
      base: { id: version.evaluator_id, latest: version.version, spec: version.spec, origin: 'dashboard' },
    }));
  /** After a 412: the latest saved version becomes the base (the diff and base_version follow); the edits stay. */
  const reloadLatest = async () => {
    const id = session.base?.id ?? spec?.id;
    if (!id) return;
    const detail = await queryClient.fetchQuery({ ...evaluatorQuery(ds, id), staleTime: 0 });
    setSession((s) => ({ ...s, base: { id: detail.id, latest: detail.latest_version, spec: detail.latest.spec, origin: detail.origin } }));
  };

  const statuses: StepStatus[] = [
    spec ? 'done' : 'todo',
    !spec ? 'waiting' : savable ? 'done' : 'todo',
    !spec ? 'waiting' : run && !stale ? 'done' : 'todo',
    !spec ? 'waiting' : 'optional',
    !spec ? 'waiting' : savedRef ? 'done' : 'todo',
    !savedRef ? 'waiting' : 'todo',
    !savedRef ? 'waiting' : 'optional',
  ];

  // The next step to do carries the page's one primary action (DESIGN.md: one primary per view).
  const next = statuses.indexOf('todo');

  return (
    <div className="space-y-5">
      <StudioSteps statuses={statuses} />
      {initial.spec ? (
        <StudioStep n={1} id="step-describe" title="Start from a saved version" description={initial.origin ?? undefined} />
      ) : (
        <DescribeStep judge={status?.judge ?? null} routes={routes} taken={taken} writeReason={writeReason} hasCheck={spec !== null} onStart={start} />
      )}
      {spec && check ? (
        <>
          <ReviewStep
            spec={spec}
            onChange={setSpec}
            check={check}
            checkOptions={checkOptions}
            draft={session.draft}
            origin={session.origin}
            idLocked={base !== null && base.id === spec.id}
            disabled={false}
          />
          <TestBench
            spec={spec}
            samples={samples}
            onSamplesChange={(next) => setSession((s) => ({ ...s, samples: next }))}
            run={run}
            onRun={setRun}
            onSpecChange={setSpec}
            decisionModel={decisionModel}
            routes={routes}
            defaultRoute={session.route}
            writeReason={writeReason}
            specReason={specReason}
            primary={next === 2}
          />
          <QuickCalibrationStep spec={spec} results={run?.response.results ?? null} stale={stale} samples={samples} onSpecChange={setSpec} />
          <SaveStep spec={spec} onSpecChange={setSpec} base={base} specReason={specReason} writeReason={writeReason} taken={taken} onSaved={onSaved} onReloadLatest={reloadLatest} primary={next === 4} />
          <WatchStep
            savedRef={savedRef}
            spec={spec}
            defaultRoute={session.route}
            specReason={specReason}
            simulated={decisionModel?.backend === 'mock'}
            modelReason={modelReason}
            primary={next === 5}
          />
          <CalibrateStep evaluatorId={base?.id ?? null} />
        </>
      ) : null}
    </div>
  );
}

const DESCRIPTION = 'Describe a check in plain English, test it on real answers, then let it watch a route before it changes anything. Jev then asks it on every request, in the same call as the route’s other checks.';

/** Studio for a new check. */
export function StudioPage() {
  return (
    <div>
      <PageHeader description={DESCRIPTION} />
      <StudioEditor initial={EMPTY} />
    </div>
  );
}

/** Studio starting from a saved version (`/app/evaluators/studio/$id/$version`). */
export function StudioVersionPage({ id, version: param }: { id: string; version: string }) {
  const ds = useDataSource();
  const version = /^[1-9][0-9]{0,8}$/.test(param) ? Number(param) : 0;
  const versionQ = useQuery({ ...evaluatorVersionQuery(ds, id, version), enabled: version > 0 });
  const detailQ = useQuery(evaluatorQuery(ds, id));
  const evaluators = useQuery(evaluatorsQuery(ds, true));
  // The library's ids too: a copy of a built-in or file check takes an id none of them has.
  const loading = useDelayedLoading(versionQ.isPending || detailQ.isPending || evaluators.isPending);

  if (version === 0) {
    return <ErrorState error={new Error(`“${param}” is not a version number. Open Studio from a version on the check's page.`)} />;
  }
  // Once the editor is up it stays: a later refetch that fails (a reload after a 412, a refocus)
  // keeps the cached version and the viewer's edits instead of swapping the page for an error.
  if (!versionQ.data || !detailQ.data || evaluators.isPending) {
    const error = versionQ.error ?? detailQ.error;
    if (error) {
      return (
        <ErrorState
          error={error}
          onRetry={() => {
            void versionQ.refetch();
            void detailQ.refetch();
          }}
        />
      );
    }
    return loading ? <LoadingState rows={4} /> : null;
  }

  const taken = [...Object.values(BUILTIN_TEMPLATES).map((t) => t.id), ...(evaluators.data?.items ?? []).map((e) => e.id)];
  const initial = sessionFrom(versionQ.data, detailQ.data, taken);
  return (
    <div>
      <PageHeader
        description={
          <>
            {DESCRIPTION}{' '}
            <Link to="/app/evaluators/$id" params={{ id }} className="font-medium text-fg underline underline-offset-2">
              Back to {detailQ.data.name}
            </Link>
          </>
        }
      />
      <StudioEditor key={`${id}@${version}`} initial={initial} />
    </div>
  );
}
