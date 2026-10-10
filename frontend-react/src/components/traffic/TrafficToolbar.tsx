/**
 * Above the Traffic list: the filters (outcome, route, model, and the reason a link brought in)
 * and the Live toggle. Below it, while paused, the "Paused · 12 new · Resume" pill.
 */
import { Link, useNavigate } from '@tanstack/react-router';
import { Pause, X } from 'lucide-react';
import { useId } from 'react';
import { Button } from '@/components/ui/button';
import { OUTCOME_IDS } from '@/domain/contracts';
import { outcomeView } from '@/domain/presentation';
import { formatCount } from '@/domain/format';
import { cn } from '@/lib/cn';
import { hasFilters, reasonFilterLabel, reasonWindowLabel, type TrafficFilters } from './trafficRows';
import { useReasonBound } from './useTrafficRows';

interface Option {
  value: string;
  label: string;
}

function FilterSelect({
  label,
  allLabel,
  value,
  options,
  onChange,
}: {
  label: string;
  allLabel: string;
  value: string | undefined;
  options: readonly Option[];
  onChange: (value: string | undefined) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center">
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <select
        id={id}
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value || undefined)}
        className={cn(
          'h-8 max-w-48 rounded-md border bg-surface px-2 text-sm hover:bg-surface-hover',
          value ? 'border-border-strong font-medium text-fg' : 'border-border text-fg-muted',
        )}
      >
        <option value="">{allLabel}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </div>
  );
}

const OUTCOME_OPTIONS: Option[] = OUTCOME_IDS.map((id) => ({ value: id, label: outcomeView(id).label }));

export function TrafficToolbar({
  filters,
  routes,
  models,
  live,
  onLiveChange,
}: {
  filters: TrafficFilters;
  routes: readonly string[];
  models: readonly string[];
  live: boolean;
  onLiveChange: (live: boolean) => void;
}) {
  const navigate = useNavigate({ from: '/app/traffic/' });
  // The reason chip says what it is read over (the proxy looks back a fixed number of days).
  const reasonWindow = reasonWindowLabel(useReasonBound());
  const setFilter = (key: keyof TrafficFilters, value: string | undefined) =>
    void navigate({ search: (prev) => ({ ...prev, [key]: value }) });

  return (
    <div className="flex flex-wrap items-center gap-2">
      <div role="group" aria-label="Filters" className="flex flex-1 flex-wrap items-center gap-2">
        <FilterSelect
          label="Outcome"
          allLabel="All outcomes"
          value={filters.outcome}
          options={OUTCOME_OPTIONS}
          onChange={(v) => setFilter('outcome', v)}
        />
        <FilterSelect
          label="Route"
          allLabel="All routes"
          value={filters.route}
          options={routes.map((r) => ({ value: r, label: r }))}
          onChange={(v) => setFilter('route', v)}
        />
        <FilterSelect
          label="Model"
          allLabel="All models"
          value={filters.model}
          options={models.map((m) => ({ value: m, label: m }))}
          onChange={(v) => setFilter('model', v)}
        />
        {filters.reason ? (
          <span className="inline-flex h-8 items-center gap-1 rounded-md border border-border-strong bg-surface pr-1 pl-2 text-sm text-fg">
            <span className="text-fg-muted">Reason:</span> {reasonFilterLabel(filters.reason)}
            {reasonWindow ? <span className="text-fg-muted"> · {reasonWindow}</span> : null}
            <Link
              from="/app/traffic/"
              search={(prev) => ({ ...prev, reason: undefined })}
              aria-label="Remove the reason filter"
              className="inline-flex size-6 items-center justify-center rounded-sm text-fg-muted hover:bg-surface-hover hover:text-fg"
            >
              <X aria-hidden="true" className="size-3.5" />
            </Link>
          </span>
        ) : null}
        {hasFilters(filters) ? (
          <Link
            from="/app/traffic/"
            search={(prev) => ({ live: prev.live })}
            className="px-1 text-sm text-fg-muted underline underline-offset-2 hover:text-fg"
          >
            Clear filters
          </Link>
        ) : null}
      </div>
      <Button
        size="sm"
        aria-pressed={live}
        aria-keyshortcuts="Space"
        onClick={() => onLiveChange(!live)}
        className={cn(live && 'bg-surface-hover')}
      >
        <span
          aria-hidden="true"
          className={cn('size-2 rounded-full border border-fg', live ? 'bg-fg' : 'bg-transparent')}
        />
        Live
      </Button>
    </div>
  );
}

/** "Paused · 12 new · Resume", floating at the bottom while the list is paused. */
export function PausedPill({ held, onResume }: { held: number; onResume: () => void }) {
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 z-30 flex justify-center">
      <button
        type="button"
        onClick={onResume}
        className="pointer-events-auto inline-flex h-8 items-center gap-1.5 rounded-full border border-border-strong bg-surface-raised px-3 text-sm text-fg shadow-popup hover:bg-surface-hover"
      >
        <Pause aria-hidden="true" className="size-3.5" />
        <span className="tabular">
          {held > 0 ? `Paused · ${formatCount(held)} new · ` : 'Paused · '}
          <span className="font-medium underline underline-offset-2">Resume</span>
        </span>
      </button>
    </div>
  );
}
