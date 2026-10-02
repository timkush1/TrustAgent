/**
 * The evidence-suggestion one-click fix (EVW-7), shown in the Overview's evidence coverage
 * card and loaded only there, on demand: a suggested tag pattern goes into a draft of the current
 * policy for the chosen route (policyDraft.addTagPattern), then into the policy review
 * (ReviewSaveDialog: validation, Preview impact, save with If-Match). Nothing changes until it is
 * saved.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { FilePlus2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction } from '@/components/common/DisabledReason';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Button } from '@/components/ui/button';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { evaluatorsQuery, isNotServed, policiesQuery } from '@/data/queries';
import { useAppMode } from '@/data/useSystem';
import type { EvidenceSuggestion } from '@/types/api';
import { addTagPattern, DraftError, draftRouteNames } from './policyDraft';
import { ReviewSaveDialog } from './ReviewSaveDialog';
import { policyWriteReason, routeTitle } from './routeWords';

/**
 * "Add to a route": the suggested pattern goes into a draft of the current policy, then into the
 * policy review (ReviewSaveDialog), which validates it, replays past answers and saves it.
 */
export default function AddTagPatternAction({ pattern }: { pattern: NonNullable<EvidenceSuggestion['pattern']> }) {
  const ds = useDataSource();
  const { demo } = useAppMode();
  const instanceReason = useWriteReason('admin');
  const postReason = useWriteReason('post');
  const policies = useQuery({ ...policiesQuery(ds), retry: false });
  const evaluators = useQuery({ ...evaluatorsQuery(ds), retry: false });
  const names = useMemo(() => new Map((evaluators.data?.items ?? []).map((e) => [e.id, e.name])), [evaluators.data]);
  const [route, setRoute] = useState('default');
  const [draft, setDraft] = useState<{ yaml: string; baseEtag: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  if (policies.isPending) return <Skeleton className="h-8 w-48" />;
  if (policies.isError) {
    return isNotServed(policies.error) ? (
      <Link to="/app/policies" className="text-xs font-medium text-fg underline underline-offset-2">
        Add it to a route’s evidence.tag_patterns
      </Link>
    ) : (
      <p className="text-xs text-fg-muted">The policy could not be read, so the pattern cannot be added here: {policies.error.message}</p>
    );
  }
  const state = policies.data;
  const writeReason = policyWriteReason(state, instanceReason);
  const routes = draftRouteNames(state.yaml);

  const review = () => {
    setError(null);
    try {
      setDraft({ yaml: addTagPattern(state.yaml, route, pattern), baseEtag: state.etag });
    } catch (err) {
      setError(err instanceof DraftError ? err.message : String(err));
    }
  };

  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          aria-label="Route for this pattern"
          value={route}
          onValueChange={(v) => v && setRoute(v)}
          options={routes.map((r) => ({ value: r, label: r === 'default' ? 'Default route (every route without its own list)' : routeTitle(r) }))}
          className="w-64"
        />
        <DisabledAction reason={writeReason}>
          {(p) => (
            <Button size="sm" onClick={review} {...p}>
              <FilePlus2 aria-hidden="true" />
              Add to this route…
            </Button>
          )}
        </DisabledAction>
      </div>
      {error ? (
        <p role="alert" className="text-xs text-fg">
          {error}
        </p>
      ) : null}
      {draft ? (
        <ReviewSaveDialog
          open
          onOpenChange={(open) => !open && setDraft(null)}
          state={state}
          draftYaml={draft.yaml}
          baseEtag={draft.baseEtag}
          names={names}
          writeReason={writeReason}
          postReason={postReason}
          simulated={demo}
          onSaved={() => setDraft(null)}
          onReloadLatest={() => {
            setDraft(null);
            void policies.refetch();
          }}
        />
      ) : null}
    </div>
  );
}
