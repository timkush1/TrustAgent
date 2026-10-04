/**
 * One route of the policy (SPEC §3.3 Policies, §7.2): how requests are matched, the mode it is set
 * to and the mode it really acts in ("Block → currently acting as Flag"), each downgrade with its
 * one-click fix, the block-readiness checklist, and the evaluators it runs (active or watching).
 * Changes made here go into the page's draft; nothing is saved until the viewer reviews it.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, Check, CircleAlert, Eye, ListChecks, SlidersHorizontal, TriangleAlert } from 'lucide-react';
import { useId, useState } from 'react';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { Term } from '@/components/common/Term';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { IconButton } from '@/components/ui/icon-button';
import { Select } from '@/components/ui/select';
import {
  actingAsText,
  calibrationStateView,
  downgradeText,
  modeView,
  POLICY_MODES,
  READINESS_ITEMS,
  readinessWords,
  stageLabel,
} from '@/domain/controlPresentation';
import { formatCount, formatRisk } from '@/domain/format';
import { copy, fillTemplate } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { BlockReadiness, CalibrationProgress, Downgrade, EffectiveRoute, PolicyMode } from '@/types/api';
import type { ThresholdEdit } from './policyDraft';
import { evaluatorDisplayName, evaluatorId, isDefaultRoute, matchSummary, routeTitle, settingFacts } from './routeWords';
import { ThresholdEditor } from './ThresholdEditor';

export type RouteFix = 'buffer_streams' | 'allow_uncalibrated' | 'stop_trusting_user_context';

export interface RouteCardProps {
  route: EffectiveRoute;
  /** The mode in the unsaved draft, when the viewer changed it. */
  draftMode: PolicyMode | null;
  /** Evaluator display names by id. */
  names: ReadonlyMap<string, string>;
  /** Calibration progress by evaluator id (for "Label N more responses"). */
  progress: ReadonlyMap<string, CalibrationProgress>;
  writeReason: Reason | null;
  onModeChange: (mode: PolicyMode) => void;
  onFix: (fix: RouteFix) => void;
  /** Put a threshold change into the draft and open the review (Preview impact). */
  onThresholds: (ref: string, edit: ThresholdEdit) => void;
  /** Open this check's threshold editor at once ("Adjust threshold" from a trace). */
  focusEvaluator?: string | null;
}

const linkClass = 'inline-flex items-center gap-0.5 text-xs font-medium text-fg underline underline-offset-2';

/** The evaluator that most needs labels among those missing calibration. */
function uncalibratedEvaluator(route: EffectiveRoute): string | null {
  const missing = route.block_readiness.find((b) => b.missing.includes('calibration'));
  if (missing) return evaluatorId(missing.evaluator);
  const raw = route.evaluators.find((e) => e.stage === 'active' && !calibrationStateView(e.calibration_state).measured);
  return raw ? evaluatorId(raw.ref) : null;
}

function labelsNeeded(p: CalibrationProgress | undefined): number | null {
  if (!p) return null;
  return Math.max(0, p.needed.n - p.human_labels.n, p.needed.pos - p.human_labels.pos, p.needed.neg - p.human_labels.neg);
}

function DowngradeFix({
  d,
  route,
  names,
  progress,
  writeReason,
  onFix,
}: { d: Downgrade } & Pick<RouteCardProps, 'route' | 'names' | 'progress' | 'writeReason' | 'onFix'>) {
  switch (d.code) {
    case 'stream_not_buffered':
      return (
        <DisabledAction reason={writeReason}>
          {(p) => (
            <Button size="sm" onClick={() => onFix('buffer_streams')} {...p}>
              {copy.policies.buffer_streams}
            </Button>
          )}
        </DisabledAction>
      );
    case 'uncalibrated':
    case 'snapshot_changed': {
      const id = uncalibratedEvaluator(route);
      const n = id ? labelsNeeded(progress.get(id)) : null;
      return (
        <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
          {id ? (
            <Link to="/app/evaluators/$id" params={{ id }} className={linkClass}>
              {n === null ? 'Label more responses' : fillTemplate(copy.policies.label_more, { n: formatCount(n) })}
              <span className="sr-only">for {evaluatorDisplayName(id, names)}</span>
              <ArrowRight aria-hidden="true" className="size-3" />
            </Link>
          ) : null}
          <DisabledAction reason={writeReason}>
            {(p) => (
              <ConfirmDialog
                title={`Allow uncalibrated blocking on ${routeTitle(route.name)}?`}
                description="Blocking will use raw fast-check probabilities, which have not been checked against your own labels. Answers can be blocked by mistake until the checks are calibrated."
                confirmLabel={copy.policies.allow_uncalibrated}
                confirmationText={route.name}
                onConfirm={() => onFix('allow_uncalibrated')}
                trigger={
                  <Button size="sm" variant="ghost" {...p}>
                    {copy.policies.allow_uncalibrated}
                  </Button>
                }
              />
            )}
          </DisabledAction>
        </span>
      );
    }
    case 'trusts_user_context':
      return (
        <DisabledAction reason={writeReason}>
          {(p) => (
            <Button size="sm" onClick={() => onFix('stop_trusting_user_context')} {...p}>
              Stop treating user messages as documents
            </Button>
          )}
        </DisabledAction>
      );
    case 'budget':
      return (
        <Link to="/app/settings/$section" params={{ section: 'budgets' }} className={linkClass}>
          Raise the daily cap
          <ArrowRight aria-hidden="true" className="size-3" />
        </Link>
      );
    case 'breaker_open':
      return (
        <Link to="/app/settings/$section" params={{ section: 'models' }} hash="last-error" className={linkClass}>
          See the last error
          <ArrowRight aria-hidden="true" className="size-3" />
        </Link>
      );
    case 'decision_model_off':
      return (
        <Link to="/app/settings/$section" params={{ section: 'connections' }} className={linkClass}>
          Connect OpenRouter
          <ArrowRight aria-hidden="true" className="size-3" />
        </Link>
      );
    default:
      return null;
  }
}

function Readiness({ items, names }: { items: readonly BlockReadiness[]; names: ReadonlyMap<string, string> }) {
  if (items.length === 0) {
    return <p className="text-xs text-fg-muted">No check on this route is allowed to block.</p>;
  }
  return (
    <ul className="space-y-2">
      {items.map((b) => {
        const shown = READINESS_ITEMS.filter((item) => item !== 'mode' || b.missing.includes('mode'));
        const done = shown.filter((item) => !b.missing.includes(item)).length;
        const next = shown.find((item) => b.missing.includes(item));
        return (
          <li key={b.evaluator} className="space-y-1">
            <p className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-sm">
              {b.ready ? (
                <Check aria-hidden="true" className="size-3.5 text-fg-muted" />
              ) : (
                <CircleAlert aria-hidden="true" className="size-3.5 text-tone-warning" />
              )}
              <span className="font-medium text-fg">{evaluatorDisplayName(b.evaluator, names)}</span>
              <span className="text-xs text-fg-muted">{b.ready ? 'ready to block' : 'not ready to block'}</span>
              {b.ready ? null : (
                <span className="text-xs text-fg-muted tabular" title={next ? readinessWords(next).missing : undefined}>
                  · {done} of {shown.length} done{next ? ` · next: ${readinessWords(next).label.toLowerCase()}` : ''}
                </span>
              )}
            </p>
            {/* The whole checklist is one click away, not repeated open on every check of every route. */}
            <details className="pl-5 text-xs">
              <summary className="cursor-pointer text-fg-muted select-none">All {shown.length} requirements</summary>
              <ul className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5">
                {shown.map((item) => {
                  const missing = b.missing.includes(item);
                  const words = readinessWords(item);
                  return (
                    <li key={item} data-missing={missing || undefined} title={missing ? words.missing : undefined} className={cn('inline-flex items-center gap-1', missing ? 'text-fg' : 'text-fg-muted')}>
                      {missing ? (
                        <CircleAlert aria-hidden="true" className="size-3 text-tone-warning" />
                      ) : (
                        <Check aria-hidden="true" className="size-3" />
                      )}
                      {words.label}
                      <span className="sr-only">{missing ? `: missing. ${words.missing}` : ': met'}</span>
                    </li>
                  );
                })}
              </ul>
            </details>
          </li>
        );
      })}
    </ul>
  );
}

export function RouteCard({ route, draftMode, names, progress, writeReason, onModeChange, onFix, onThresholds, focusEvaluator = null }: RouteCardProps) {
  const headingId = useId();
  const linked = focusEvaluator && route.evaluators.some((e) => e.ref === focusEvaluator) ? focusEvaluator : null;
  const [editing, setEditing] = useState<string | null>(linked);
  const mode = modeView(route.mode);
  const acting = actingAsText(route.mode, route.effective_mode);
  const shownMode = draftMode ?? route.mode;
  const enforcing = shownMode === 'block' || shownMode === 'fallback' || route.block_readiness.length > 0;

  return (
    <Card className="flex flex-col" data-route={route.name}>
      <article aria-labelledby={headingId} className="flex flex-1 flex-col gap-4 p-4">
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <h3 id={headingId} className={cn('text-base font-semibold text-fg', !isDefaultRoute(route.name) && 'font-mono text-sm')}>
              {routeTitle(route.name)}
            </h3>
            <ul className="space-y-0.5 text-xs text-fg-muted">
              {matchSummary(route.name, route.match).map((m) => (
                <li key={m}>{m}</li>
              ))}
            </ul>
          </div>
          <DisabledAction reason={writeReason} layout="stacked" className="items-end">
            {(p) => (
              <Select<PolicyMode>
                aria-label={`Mode for ${routeTitle(route.name)}`}
                options={POLICY_MODES.map((m) => ({ value: m, label: modeView(m).label, description: modeView(m).description }))}
                value={shownMode}
                onValueChange={(m) => m && m !== shownMode && onModeChange(m)}
                disabled={p.disabled}
                className="w-36"
              />
            )}
          </DisabledAction>
        </header>

        {draftMode && draftMode !== route.mode ? (
          <p className="text-xs text-fg-muted">
            Unsaved: {mode.label} → {modeView(draftMode).label}. Review and save to apply it.
          </p>
        ) : null}

        {acting ? (
          <div className="space-y-2 rounded-md border border-dashed border-border-strong px-3 py-2">
            <p className="flex items-center gap-1.5 text-sm font-medium text-fg">
              <TriangleAlert aria-hidden="true" className="size-4 text-tone-warning" />
              {acting}
            </p>
            {route.downgrades.length > 0 ? (
              <ul className="space-y-2">
                {route.downgrades.map((d) => (
                  <li key={d.code} className="space-y-1">
                    <p className="text-sm text-fg-muted">{downgradeText(d)}</p>
                    <DowngradeFix d={d} route={route} names={names} progress={progress} writeReason={writeReason} onFix={onFix} />
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : route.downgrades.length > 0 ? (
          <ul className="space-y-2">
            {route.downgrades.map((d) => (
              <li key={d.code} className="space-y-1 text-sm text-fg-muted">
                <p className="flex items-center gap-1.5">
                  <TriangleAlert aria-hidden="true" className="size-3.5 text-tone-warning" />
                  {downgradeText(d)}
                </p>
                <DowngradeFix d={d} route={route} names={names} progress={progress} writeReason={writeReason} onFix={onFix} />
              </li>
            ))}
          </ul>
        ) : null}

        {enforcing ? (
          <section aria-label={`Block readiness for ${routeTitle(route.name)}`} className="space-y-2">
            <h4 className="flex items-center gap-1.5 text-xs font-medium text-fg-muted">
              <ListChecks aria-hidden="true" className="size-3.5" />
              <span>
                Ready to <Term id="block">block</Term>?
              </span>
            </h4>
            <Readiness items={route.block_readiness} names={names} />
          </section>
        ) : null}

        <section aria-label={`Checks on ${routeTitle(route.name)}`} className="space-y-2">
          <h4 className="text-xs font-medium text-fg-muted">
            <Term id="evaluator">Checks</Term> on this route
          </h4>
          {route.evaluators.length === 0 ? (
            <p className="text-sm text-fg-muted">No checks: only instant checks run.</p>
          ) : (
            <ul className="divide-y divide-border">
              {route.evaluators.map((e) => {
                const id = evaluatorId(e.ref);
                const cal = calibrationStateView(e.calibration_state);
                return (
                  <li key={e.ref} className="space-y-2 py-1.5">
                    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                      <span className="flex min-w-0 items-center gap-2">
                        <Link to="/app/evaluators/$id" params={{ id }} className="truncate text-sm font-medium text-fg underline-offset-2 hover:underline">
                          {evaluatorDisplayName(e.ref, names)}
                        </Link>
                        <Badge size="sm" emphasis={e.stage === 'shadow' ? 'outline' : 'quiet'} icon={e.stage === 'shadow' ? Eye : undefined}>
                          {stageLabel(e.stage)}
                        </Badge>
                        {e.resolved ? null : (
                          <span className="inline-flex items-center gap-1 text-xs text-fg">
                            <TriangleAlert aria-hidden="true" className="size-3 text-tone-warning" />
                            Not in the library
                          </span>
                        )}
                      </span>
                      <span className="flex items-center gap-1">
                        <span className="text-xs text-fg-muted tabular">
                          {cal.label} · flag at {formatRisk(e.warn_at)}
                          {e.critical_at !== null ? ` · block at ${formatRisk(e.critical_at)}` : ''}
                        </span>
                        {e.resolved ? (
                          <IconButton
                            size="sm"
                            icon={SlidersHorizontal}
                            aria-label={`${copy.trace.adjust_threshold}: ${evaluatorDisplayName(e.ref, names)}`}
                            aria-expanded={editing === e.ref}
                            onClick={() => setEditing((cur) => (cur === e.ref ? null : e.ref))}
                          />
                        ) : null}
                      </span>
                    </div>
                    {editing === e.ref ? (
                      <ThresholdEditor
                        route={route.name}
                        evaluator={e}
                        name={evaluatorDisplayName(e.ref, names)}
                        writeReason={writeReason}
                        focusOnOpen={linked === e.ref}
                        onPreview={(edit) => {
                          onThresholds(e.ref, edit);
                          setEditing(null);
                        }}
                        onCancel={() => setEditing(null)}
                      />
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}
        </section>

        <details className="group mt-auto text-sm">
          <summary className="cursor-pointer text-xs font-medium text-fg-muted hover:text-fg">Route settings</summary>
          <dl className="mt-2 grid grid-cols-[minmax(8rem,max-content)_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
            {settingFacts(route).map((f) => (
              <div key={f.label} className="contents">
                <dt className="text-fg-muted">{f.label}</dt>
                <dd className="text-fg">{f.value}</dd>
              </div>
            ))}
          </dl>
        </details>
      </article>
    </Card>
  );
}
