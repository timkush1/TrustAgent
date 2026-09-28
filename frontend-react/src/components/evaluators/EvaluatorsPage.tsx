/**
 * Evaluators (SPEC §3.3): "What do we check, and how well?" The library: every check with its
 * category, the routes it runs on and how far its calibration is. Filters by name, category and
 * whether it runs anywhere; archived evaluators on request. Studio (new checks) arrives in P4 and
 * says so.
 */
import { useQueries, useQuery } from '@tanstack/react-query';
import { ListChecks, SearchX, Sparkles } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction } from '@/components/common/DisabledReason';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { NAV_ITEMS } from '@/components/layout/nav';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import { evaluatorQuery, evaluatorsQuery, isNotServed } from '@/data/queries';
import { evaluatorCategoryLabel } from '@/domain/controlPresentation';
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { EvaluatorCard } from './EvaluatorCard';
import { P4_REASONS } from './evaluatorWords';

type Attached = 'all' | 'attached' | 'unused';

export function EvaluatorsPage() {
  const ds = useDataSource();
  const [archived, setArchived] = useState(false);
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState('all');
  const [attached, setAttached] = useState<Attached>('all');
  const list = useQuery(evaluatorsQuery(ds, archived));
  const showLoading = useDelayedLoading(list.isPending);
  const items = useMemo(() => list.data?.items ?? [], [list.data]);
  const details = useQueries({ queries: items.map((e) => ({ ...evaluatorQuery(ds, e.id), retry: false })) });

  const categories = useMemo(() => [...new Set(items.map((e) => e.category))].sort(), [items]);
  const visible = items
    .map((meta, i) => ({ meta, detail: details[i] }))
    .filter(({ meta }) => {
      const q = query.trim().toLowerCase();
      if (q && !meta.name.toLowerCase().includes(q) && !meta.id.includes(q)) return false;
      if (category !== 'all' && meta.category !== category) return false;
      if (attached === 'attached' && meta.attached_routes.length === 0) return false;
      if (attached === 'unused' && meta.attached_routes.length > 0) return false;
      return true;
    });
  const filtered = query.trim() !== '' || category !== 'all' || attached !== 'all';

  let body;
  if (list.isPending) {
    body = showLoading ? (
      <div aria-busy="true" className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <span role="status" className="sr-only">
          {copy.states.loading}
        </span>
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} className="h-56 w-full" />
        ))}
      </div>
    ) : null;
  } else if (list.isError) {
    body = isNotServed(list.error) ? (
      <DegradedState
        title="The evaluator library is not available on this proxy"
        reason="It does not serve GET /api/evaluators yet (an older version)."
        stillWorks="Routes still run the checks listed in config/policies.yaml."
      />
    ) : (
      <ErrorState error={list.error} onRetry={() => void list.refetch()} />
    );
  } else if (items.length === 0) {
    body = (
      <EmptyState icon={ListChecks} title="No evaluators yet">
        <p>Built-in checks appear here when the proxy loads them from contracts/evaluators.</p>
      </EmptyState>
    );
  } else if (visible.length === 0) {
    body = (
      <EmptyState icon={SearchX} title="No evaluators match these filters">
        <Button
          size="sm"
          onClick={() => {
            setQuery('');
            setCategory('all');
            setAttached('all');
          }}
        >
          Clear filters
        </Button>
      </EmptyState>
    );
  } else {
    body = (
      <section aria-labelledby="evaluator-library-title">
        <h2 id="evaluator-library-title" className="sr-only">
          Evaluator library
        </h2>
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {visible.map(({ meta, detail }) => (
            <EvaluatorCard key={meta.id} meta={meta} detail={detail} />
          ))}
        </div>
      </section>
    );
  }

  return (
    <div>
      <PageHeader
        description={NAV_ITEMS[2].question}
        actions={
          <DisabledAction reason={P4_REASONS.studio}>
            {(p) => (
              <Button {...p}>
                <Sparkles aria-hidden="true" />
                New check
              </Button>
            )}
          </DisabledAction>
        }
      >
        <div role="group" aria-label="Filters" className="flex flex-wrap items-center gap-2">
          <Input
            type="search"
            aria-label="Search evaluators"
            placeholder="Search by name"
            value={query}
            onValueChange={setQuery}
            className="w-56"
          />
          <Select
            aria-label="Category"
            value={category}
            onValueChange={(v) => setCategory(v ?? 'all')}
            options={[{ value: 'all', label: 'All categories' }, ...categories.map((c) => ({ value: c, label: evaluatorCategoryLabel(c) }))]}
            className="w-44"
          />
          <Select<Attached>
            aria-label="Routes"
            value={attached}
            onValueChange={(v) => setAttached(v ?? 'all')}
            options={[
              { value: 'all', label: 'On any route or none' },
              { value: 'attached', label: 'Runs on a route' },
              { value: 'unused', label: 'Not on any route' },
            ]}
            className="w-48"
          />
          <Switch label="Show archived" checked={archived} onCheckedChange={setArchived} className="ml-1" />
          {list.data ? (
            <span className="ml-auto text-xs text-fg-muted tabular">
              {filtered ? `${formatCount(visible.length)} of ${formatCount(items.length)}` : formatCount(items.length)} evaluators
            </span>
          ) : null}
        </div>
      </PageHeader>
      {body}
    </div>
  );
}
