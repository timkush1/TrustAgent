/**
 * Studio step 6, watch it on a route (SPEC §7.3): "Save and watch on route X" adds the saved
 * version to the route as a shadow evaluator (asked on every request, never enforced); "Make
 * active" then lets it act. Both are policy edits (lib/studio/policyEdits.ts) reviewed and saved
 * through the Policies review dialog: the diff, server validation, "Preview impact (free)", a
 * message, and If-Match. Every policy version can be rolled back in one click from Policies.
 * "Test on past traffic (paid)" runs the check on the route's stored answers first.
 */
import { Link } from '@tanstack/react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Eye } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { notify } from '@/components/common/notify';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { DraftError } from '@/components/policies/policyDraft';
import { ReviewSaveDialog } from '@/components/policies/ReviewSaveDialog';
import { policyWriteReason } from '@/components/policies/routeWords';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { evaluatorsQuery, policiesQuery, qkc } from '@/data/queries';
import { useAppMode } from '@/data/useSystem';
import { copy } from '@/domain/presentation';
import { stageLabel } from '@/domain/controlPresentation';
import { attachEvaluator, findEvaluator, refId, replaceEvaluatorRef, setEvaluatorStage, type RouteEvaluatorEntry } from '@/lib/studio/policyEdits';
import type { EvaluatorSpec } from '@/types/api';
import { studioSaveAndWatchLabel } from './studioWords';
import { DryRunPanel } from './DryRunPanel';
import { StudioStep } from './StudioStep';
import { evalNameMap } from '@/domain/words/evaluators';

export interface WatchStepProps {
  /** The saved version to attach ("id@version"), or null while the check is unsaved or changed. */
  savedRef: string | null;
  spec: EvaluatorSpec;
  defaultRoute: string | null;
  /** Why the check cannot run in a test (its errors), or null. */
  specReason: Reason | null;
  /** The fast check is a mock: costs are simulated. */
  simulated: boolean;
  /** Why the fast check cannot run at all (not connected), or null. */
  modelReason: Reason | null;
  /** This is the step to do next: its action is the page's one primary button. */
  primary: boolean;
}

function safeFind(yaml: string, route: string, id: string): RouteEvaluatorEntry | undefined {
  try {
    return findEvaluator(yaml, route, id);
  } catch {
    return undefined;
  }
}

export function WatchStep({ savedRef, spec, defaultRoute, specReason, simulated, modelReason, primary }: WatchStepProps) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const { demo } = useAppMode();
  const instanceReason = useWriteReason('admin');
  const postReason = useWriteReason('post');
  const policies = useQuery(policiesQuery(ds));
  const evaluators = useQuery(evaluatorsQuery(ds, true));
  const state = policies.data;
  const routes = useMemo(() => (state ? state.effective_routes.map((r) => r.name) : []), [state]);
  const [chosen, setChosen] = useState<string | null>(null);
  const route = chosen ?? (defaultRoute && routes.includes(defaultRoute) ? defaultRoute : (routes.find((r) => r !== 'default') ?? routes[0] ?? null));
  const [draft, setDraft] = useState<{ yaml: string; etag: string } | null>(null);
  const names = useMemo(() => {
    return evalNameMap([...(evaluators.data?.items ?? []), { id: spec.id, name: spec.name }]);
  }, [evaluators.data, spec.id, spec.name]);

  /** A ref in words: "Competitor mentions, version 2" rather than "proof_murg2b8p@2". */
  const refWords = (ref: string) => {
    const at = ref.lastIndexOf('@');
    const refName = names.get(at < 0 ? ref : ref.slice(0, at)) ?? (at < 0 ? ref : ref.slice(0, at));
    return at < 0 ? refName : `${refName}, version ${ref.slice(at + 1)}`;
  };

  const id = savedRef ? refId(savedRef) : spec.id;
  const entry = state && route ? safeFind(state.yaml, route, id) : undefined;
  const stage = entry ? (entry.stage ?? 'active') : null;
  const writeReason = policyWriteReason(state, instanceReason);

  const open = (edit: (yaml: string) => string) => {
    if (!state) return;
    try {
      setDraft({ yaml: edit(state.yaml), etag: state.etag });
    } catch (err) {
      notify.error("Couldn't change the policy draft", err instanceof DraftError ? err : new Error(String(err)));
    }
  };

  let action: { label: string; detail: string; run: () => void } | null = null;
  if (route && savedRef) {
    if (!entry) {
      action = {
        label: studioSaveAndWatchLabel(route),
        detail: `Adds ${refWords(savedRef)} to ${route} as Watching: it is asked on every request and shown on traces, but never changes an answer.`,
        run: () => open((y) => attachEvaluator(y, route, savedRef, 'shadow')),
      };
    } else if (entry.ref !== savedRef) {
      action = {
        label: `Use version ${savedRef.slice(savedRef.lastIndexOf('@') + 1)} on ${route}`,
        detail: `${route} runs ${refWords(entry.ref)} (${stageLabel(stage ?? 'active')}). This points it at ${refWords(savedRef)} and keeps it ${stageLabel(stage ?? 'active').toLowerCase()}.`,
        run: () => open((y) => replaceEvaluatorRef(y, route, savedRef)),
      };
    } else if (stage === 'shadow') {
      action = {
        label: copy.studio.make_active,
        detail: `${route} is watching ${refWords(savedRef)}. Make it active once its answers look right: it then flags (and, when calibrated, blocks) as the route's mode says.`,
        run: () => open((y) => setEvaluatorStage(y, route, id, 'active')),
      };
    }
  }

  let attachReason: Reason | null = writeReason;
  if (!attachReason && !savedRef) attachReason = { text: 'Save a version first (step 5)' };
  if (!attachReason && !route) attachReason = { text: 'The policy has no routes yet' };

  const dryReason = instanceReason ?? modelReason ?? (savedRef ? null : specReason) ?? (route ? null : { text: 'Choose a route first' });

  return (
    <StudioStep
      n={6}
      id="step-watch"
      title="Watch it on a route"
      description="Watching runs the check on real traffic without changing any answer. Make it active when its answers look right; roll back from Policies → Versions in one click."
    >
      {policies.isPending ? (
        <Skeleton className="h-8 w-64" />
      ) : !state && policies.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-fg">
          <span>The policy could not be loaded: {policies.error.message}</span>
          <Button size="sm" onClick={() => void policies.refetch()}>
            Retry
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Route" className="min-w-48">
            <Select value={route} onValueChange={(v) => setChosen(v)} options={routes.map((r) => ({ value: r, label: r }))} />
          </Field>
          {stage ? (
            <Badge tone="neutral" emphasis="quiet" icon={Eye}>
              {entry?.ref === savedRef || !entry ? `${stageLabel(stage)} on ${route}` : `${route} runs ${refWords(entry.ref)}`}
            </Badge>
          ) : null}
        </div>
      )}

      {action ? (
        <div className="space-y-1.5">
          <DisabledAction reason={attachReason}>
            {(p) => (
              <Button variant={primary ? 'primary' : 'secondary'} onClick={action.run} {...p}>
                {action.label}
              </Button>
            )}
          </DisabledAction>
          <p className="text-sm text-fg-muted">{action.detail}</p>
        </div>
      ) : savedRef && stage === 'active' && entry?.ref === savedRef ? (
        <p className="text-sm text-fg">
          {refWords(savedRef)} is active on {route}.{' '}
          <Link to="/app/policies" className="font-medium underline underline-offset-2">
            See it in Policies
          </Link>
        </p>
      ) : attachReason ? (
        // Nothing to do yet: the action stays visible with the reason it is not available.
        <DisabledAction reason={attachReason}>
          {(p) => (
            <Button variant={primary ? 'primary' : 'secondary'} {...p}>
              {route ? studioSaveAndWatchLabel(route) : 'Save and watch'}
            </Button>
          )}
        </DisabledAction>
      ) : null}

      {route ? <DryRunPanel target={savedRef ? { ref: savedRef } : { spec }} spec={spec} route={route} simulated={simulated} reason={dryReason} names={names} /> : null}

      {state && draft ? (
        <ReviewSaveDialog
          open
          onOpenChange={(o) => {
            if (!o) setDraft(null);
          }}
          state={state}
          draftYaml={draft.yaml}
          baseEtag={draft.etag}
          names={names}
          writeReason={writeReason}
          postReason={postReason}
          simulated={demo}
          onSaved={() => setDraft(null)}
          onReloadLatest={() => {
            setDraft(null);
            void queryClient.invalidateQueries({ queryKey: qkc.policies.all });
          }}
        />
      ) : null}
    </StudioStep>
  );
}
