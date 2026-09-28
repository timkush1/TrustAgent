/**
 * Policies (SPEC §3.3): "What does each route do with a risky answer?"
 *
 * One card per route (match, mode, "Block → currently acting as Flag" with its downgrades and
 * one-click fixes, the block-readiness checklist, the checks it runs), the version the policy is
 * at and where it came from, the diverged-file banner, versions with rollback, presets and the
 * advanced YAML editor. Every change goes into a draft; the draft bar leads to the review (diff,
 * validation, Preview impact) and the save, sent with If-Match.
 *
 * Page contract: delayed skeleton; the server's message with Retry; a degraded state for a proxy
 * without the policies API and for an invalid policies.yaml; the reason next to every write that
 * is unavailable (read-only, not an admin, file-managed, locked).
 */
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, FilePen, Route as RouteIcon, Undo2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { parse as parseYaml } from 'yaml';
import { useDataSource } from '@/app/services';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledReason } from '@/components/common/DisabledReason';
import { notify } from '@/components/common/notify';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { useNow } from '@/components/common/useNow';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { NAV_ITEMS } from '@/components/layout/nav';
import { Button } from '@/components/ui/button';
import { Section } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { evaluatorQuery, evaluatorsQuery, isNotServed, policiesQuery, qkc } from '@/data/queries';
import { useAppMode } from '@/data/useSystem';
import { policySourceLabel } from '@/domain/controlPresentation';
import { formatDateTime, formatRelativeTime } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { diffTexts, summarizeDiff } from '@/domain/lineDiff';
import type { CalibrationProgress, PolicyMode, PolicyState } from '@/types/api';
import { DivergedBanner } from './DivergedBanner';
import {
  allowUncalibratedBlocking,
  applyPreset,
  bufferStreams,
  DEFAULT_ROUTE,
  DraftError,
  draftRouteNames,
  setRouteMode,
  stopTrustingUserContext,
  yamlDiffers,
  type PolicyPreset,
} from './policyDraft';
import { PresetsDialog } from './PresetsDialog';
import { ReviewSaveDialog } from './ReviewSaveDialog';
import { RouteCard, type RouteFix } from './RouteCard';
import { evaluatorId, policyWriteReason } from './routeWords';
import { VersionsSheet } from './VersionsSheet';
import { YamlEditorDialog } from './YamlEditorDialog';

interface Draft {
  yaml: string;
  /** The policy version the draft started from. */
  baseEtag: string;
}

/** Mode per route in a policy text (the default route reads `defaults.mode`). */
function modesIn(yaml: string): Map<string, PolicyMode> {
  const out = new Map<string, PolicyMode>();
  try {
    const doc = parseYaml(yaml) as { defaults?: { mode?: PolicyMode }; routes?: { name?: string; mode?: PolicyMode }[] } | null;
    if (doc?.defaults?.mode) out.set(DEFAULT_ROUTE, doc.defaults.mode);
    for (const r of doc?.routes ?? []) if (r?.name && r.mode) out.set(r.name, r.mode);
  } catch {
    // A draft that does not parse has no modes to show; the review reports the error.
  }
  return out;
}

function PoliciesSkeleton() {
  return (
    <div aria-busy="true" className="grid gap-4 xl:grid-cols-2">
      <span role="status" className="sr-only">
        {copy.states.loading}
      </span>
      <Skeleton className="h-80 w-full" />
      <Skeleton className="h-80 w-full" />
    </div>
  );
}

function SourceLine({ state }: { state: PolicyState }) {
  const now = useNow();
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-muted">
      <span className="inline-flex h-5 items-center rounded-full border border-border bg-surface-sunken px-2 font-medium text-fg">
        Version {state.id} · {policySourceLabel(state.source)}
      </span>
      <span>
        by {state.author} · <time dateTime={state.created_at} title={formatDateTime(state.created_at)}>{formatRelativeTime(state.created_at, now)}</time>
      </span>
      {state.message ? <span className="text-fg">“{state.message}”</span> : null}
    </span>
  );
}

function DraftBar({
  draft,
  state,
  onDiscard,
  onReview,
}: {
  draft: Draft;
  state: PolicyState;
  onDiscard: () => void;
  onReview: () => void;
}) {
  const summary = summarizeDiff(diffTexts(state.yaml, draft.yaml));
  const moved = draft.baseEtag !== state.etag;
  return (
    <div
      role="region"
      aria-label="Unsaved changes"
      className="sticky bottom-4 z-30 mt-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface-raised px-4 py-3 shadow-popup"
    >
      <div className="min-w-0 space-y-0.5">
        <p className="flex items-center gap-1.5 text-sm font-medium text-fg">
          <FilePen aria-hidden="true" className="size-4" />
          Unsaved changes · {summary.added + summary.removed} lines
        </p>
        {moved ? (
          <p className="text-xs text-fg-muted">The policy changed on the server since you started; saving will ask you to reload it.</p>
        ) : (
          <p className="text-xs text-fg-muted">Nothing changes for your users until you save.</p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" onClick={onDiscard}>
          <Undo2 aria-hidden="true" />
          Discard
        </Button>
        <Button onClick={onReview}>{copy.policies.preview_impact}</Button>
        <Button variant="primary" onClick={onReview}>
          Review and save
        </Button>
      </div>
    </div>
  );
}

export function PoliciesPage() {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const { demo } = useAppMode();
  const instanceReason = useWriteReason('admin');
  const postReason = useWriteReason('post');
  const q = useQuery(policiesQuery(ds));
  const evaluators = useQuery(evaluatorsQuery(ds));
  const showLoading = useDelayedLoading(q.isPending);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [reviewOpen, setReviewOpen] = useState(false);

  const state = q.data;
  const writeReason = policyWriteReason(state, instanceReason);
  const draftYaml = draft?.yaml ?? state?.yaml ?? '';

  const names = useMemo(() => new Map((evaluators.data?.items ?? []).map((e) => [e.id, e.name])), [evaluators.data]);

  // Calibration progress of the evaluators that keep a route from blocking ("Label N more").
  const uncalibratedIds = useMemo(() => {
    const ids = new Set<string>();
    for (const r of state?.effective_routes ?? []) {
      for (const b of r.block_readiness) if (b.missing.includes('calibration')) ids.add(evaluatorId(b.evaluator));
    }
    return [...ids];
  }, [state]);
  const progress = useQueries({
    queries: uncalibratedIds.map((id) => ({ ...evaluatorQuery(ds, id), retry: false })),
    combine: (results) =>
      new Map<string, CalibrationProgress>(results.flatMap((r) => (r.data ? [[r.data.id, r.data.calibration_progress] as const] : []))),
  });

  const draftModes = useMemo(() => (draft ? modesIn(draft.yaml) : new Map<string, PolicyMode>()), [draft]);

  const applyEdit = (change: (yaml: string) => string, done?: string) => {
    if (!state) return;
    try {
      const next = change(draftYaml);
      if (!yamlDiffers(next, state.yaml)) {
        setDraft(null);
      } else {
        setDraft({ yaml: next, baseEtag: draft?.baseEtag ?? state.etag });
      }
      if (done) notify.success(done, { description: 'Review and save to apply it.' });
    } catch (err) {
      notify.error("Couldn't change the draft", err instanceof DraftError ? err : new Error(String(err)));
    }
  };

  const onFix = (route: string) => (fix: RouteFix) => {
    switch (fix) {
      case 'buffer_streams':
        applyEdit((y) => bufferStreams(y, route), `Streams on ${route} will be buffered`);
        break;
      case 'allow_uncalibrated':
        applyEdit((y) => allowUncalibratedBlocking(y, route), `${route} may block without calibration`);
        break;
      case 'stop_trusting_user_context':
        applyEdit((y) => stopTrustingUserContext(y, route), `${route} no longer treats user messages as documents`);
        break;
      default: {
        const unhandled: never = fix;
        return unhandled;
      }
    }
  };

  const onSaved = () => setDraft(null);
  const onChanged = (next: PolicyState) => {
    queryClient.setQueryData(qkc.policies.state, next);
    void queryClient.invalidateQueries({ queryKey: qkc.policies.versions });
  };

  const header = (
    <PageHeader
      description={
        <>
          {NAV_ITEMS[3].question}
          {state ? (
            <span className="mt-1.5 block">
              <SourceLine state={state} />
            </span>
          ) : null}
        </>
      }
      actions={
        state ? (
          <>
            <VersionsSheet state={state} writeReason={writeReason} />
            <PresetsDialog
              routes={draftRouteNames(draftYaml)}
              writeReason={writeReason}
              onApply={(route, preset: PolicyPreset) => applyEdit((y) => applyPreset(y, route, preset), `${preset.name} applied to ${route}`)}
            />
            <YamlEditorDialog yaml={draftYaml} writeReason={writeReason} onApply={(yaml) => applyEdit(() => yaml)} />
          </>
        ) : null
      }
    />
  );

  let body;
  if (q.isPending) {
    body = showLoading ? <PoliciesSkeleton /> : null;
  } else if (q.isError && !state) {
    body = isNotServed(q.error) ? (
      <DegradedState
        title="Policies cannot be read from this proxy"
        reason="It does not serve GET /api/policies yet (an older version)."
        stillWorks="Routes still follow config/policies.yaml, read when the proxy starts."
      />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  } else if (state) {
    const routes = state.effective_routes;
    body = (
      <div className="space-y-4">
        {writeReason && typeof writeReason !== 'string' ? (
          <p className="rounded-lg border border-border bg-surface px-4 py-2.5">
            <DisabledReason reason={writeReason} />
          </p>
        ) : null}
        {state.file.status === 'diverged' ? <DivergedBanner state={state} writeReason={writeReason} onChanged={onChanged} /> : null}
        {state.file.status === 'invalid' ? (
          <DegradedState
            title="policies.yaml has errors, so the last good version stays in effect"
            reason={
              <span className="block space-y-0.5">
                {state.file.errors.map((e, i) => (
                  <span key={i} className="block">
                    {e.line ? `Line ${e.line}: ` : ''}
                    {e.message}
                  </span>
                ))}
              </span>
            }
            stillWorks="Every route keeps working with the version shown above."
          />
        ) : null}
        {state.file.status === 'missing' ? (
          <p className="text-sm text-fg-muted">
            No policies.yaml at <code className="font-mono text-xs">{state.file.path}</code>; the policy lives in the database only.
          </p>
        ) : null}
        {state.persisted ? null : <p className="text-sm text-fg-muted">{copy.states.applies_until_restart}</p>}
        {state.warnings.length > 0 ? (
          <ul className="space-y-1 text-sm text-fg-muted">
            {state.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        ) : null}
        <Section title="Routes" description="A request follows the first route whose matchers all fit; the default route takes the rest.">
        {routes.length === 0 ? (
          <EmptyState icon={RouteIcon} title="No routes yet: every request uses the defaults">
            <p>Start from a preset, or add a route in the YAML editor.</p>
          </EmptyState>
        ) : (
          <div className="grid gap-4 xl:grid-cols-2">
            {routes.map((route) => (
              <RouteCard
                key={route.name}
                route={route}
                draftMode={draftModes.get(route.name) ?? null}
                names={names}
                progress={progress}
                writeReason={writeReason}
                onModeChange={(mode) => applyEdit((y) => setRouteMode(y, route.name, mode))}
                onFix={onFix(route.name)}
              />
            ))}
          </div>
        )}
        </Section>
        <p className="text-xs text-fg-muted">
          Checks and their thresholds live in{' '}
          <Link to="/app/evaluators" className="inline-flex items-center gap-0.5 font-medium text-fg underline underline-offset-2">
            Evaluators
            <ArrowRight aria-hidden="true" className="size-3" />
          </Link>
        </p>
        {draft ? (
          <>
            <DraftBar draft={draft} state={state} onDiscard={() => setDraft(null)} onReview={() => setReviewOpen(true)} />
            <ReviewSaveDialog
              open={reviewOpen}
              onOpenChange={setReviewOpen}
              state={state}
              draftYaml={draft.yaml}
              baseEtag={draft.baseEtag}
              names={names}
              writeReason={writeReason}
              postReason={postReason}
              simulated={demo}
              onSaved={onSaved}
              onReloadLatest={() => {
                setDraft(null);
                void q.refetch();
              }}
            />
          </>
        ) : null}
      </div>
    );
  }

  return (
    <div>
      {header}
      {body}
    </div>
  );
}
