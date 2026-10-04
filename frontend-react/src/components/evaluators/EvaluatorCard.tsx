/**
 * One evaluator in the library (SPEC §3.3 Evaluators): its name and category, where it came from,
 * the routes it runs on (active or watching), and its calibration progress with the label and
 * calibrate actions.
 */
import type { UseQueryResult } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Archive, Eye } from 'lucide-react';
import { Badge, Chip } from '@/components/ui/badge';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { evaluatorCategoryLabel, evaluatorOriginLabel, stageLabel } from '@/domain/controlPresentation';
import type { EvaluatorDetail, EvaluatorMeta } from '@/types/api';
import { CalibrationMeter } from './CalibrationMeter';

export function EvaluatorCard({ meta, detail }: { meta: EvaluatorMeta; detail: Pick<UseQueryResult<EvaluatorDetail>, 'data' | 'isPending' | 'isError' | 'error'> }) {
  const headingId = `evaluator-${meta.id}`;
  return (
    <Card className="flex flex-col">
      <article aria-labelledby={headingId} className="flex flex-1 flex-col gap-3 p-4">
        <header className="space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 id={headingId} className="text-base font-semibold text-fg">
              <Link to="/app/evaluators/$id" params={{ id: meta.id }} className="underline-offset-2 hover:underline">
                {meta.name}
              </Link>
            </h3>
            <Badge size="sm" emphasis="quiet">
              {evaluatorCategoryLabel(meta.category)}
            </Badge>
            {meta.archived ? (
              <Badge size="sm" emphasis="quiet" icon={Archive}>
                Archived
              </Badge>
            ) : null}
          </div>
          <p className="text-xs text-fg-muted">
            {evaluatorOriginLabel(meta.origin)} · version {meta.latest_version} · <span className="font-mono">{meta.id}</span>
          </p>
          {detail.data?.latest.spec.description ? <p className="line-clamp-2 text-sm text-fg-muted">{detail.data.latest.spec.description}</p> : null}
        </header>

        <div className="space-y-1">
          <h4 className="text-xs font-medium text-fg-muted">Runs on</h4>
          {meta.attached_routes.length === 0 ? (
            <p className="text-sm text-fg-muted">No route yet</p>
          ) : (
            <ul className="flex flex-wrap gap-1.5">
              {meta.attached_routes.map((r) => (
                <li key={`${r.route}-${r.stage}`}>
                  <Chip icon={r.stage === 'shadow' ? Eye : undefined}>
                    <span className="font-mono">{r.route}</span> · {stageLabel(r.stage)}
                  </Chip>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="mt-auto border-t border-border pt-3">
          {detail.isPending ? (
            <Skeleton className="h-16 w-full" />
          ) : detail.isError ? (
            <p className="text-xs text-fg-muted">Calibration progress is not available: {detail.error?.message}</p>
          ) : detail.data ? (
            <CalibrationMeter progress={detail.data.calibration_progress} detail={detail.data} compact />
          ) : null}
        </div>
      </article>
    </Card>
  );
}
