/**
 * "Preview impact (free — replays past answers)" (SPEC §2.4 #26, §3.3 Policies): what the draft
 * would have done to the answers already stored, next to what the policy in effect did. Nothing
 * is re-checked and nothing is billed: the stored fast-check answers are replayed through the
 * decision rules, so checks that never ran on an answer cannot be replayed (the skipped counts).
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight } from 'lucide-react';
import { ChartTable } from '@/components/charts/ChartParts';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { dryRunSkipLabel, GATE_ACTION_ORDER, gateActionLabel } from '@/domain/controlPresentation';
import { formatCount } from '@/domain/format';
import { formatPercent, share } from '@/domain/rates';
import type { GateAction, PolicyDryRunResult } from '@/types/api';
import { windowLong } from '@/components/overview/overviewModel';

function actionsIn(result: PolicyDryRunResult): GateAction[] {
  return GATE_ACTION_ORDER.filter((a) => (result.by_action_current[a] ?? 0) > 0 || (result.by_action_proposed[a] ?? 0) > 0);
}

function Delta({ label, value }: { label: string; value: number }) {
  return (
    <div className="min-w-0 px-3 py-2">
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className="text-lg font-semibold text-fg tabular">{formatCount(value)}</dd>
    </div>
  );
}

/** `simulated`: the replayed answers came from mock checks (the demo stack). */
export function PreviewImpact({ result, simulated = false }: { result: PolicyDryRunResult; simulated?: boolean }) {
  const actions = actionsIn(result);
  const agreement = result.deep_audit_agreement;
  const wouldBlock = agreement.would_block_hallucinated + agreement.would_block_grounded + agreement.would_block_unaudited;

  if (result.evaluated === 0) {
    return (
      <div className="space-y-1 text-sm">
        <p className="text-fg">No stored answers to replay in {windowLong(result.window)}.</p>
        {result.skipped.map((s) => (
          <p key={s.reason} className="text-xs text-fg-muted">
            {dryRunSkipLabel(s.reason)}: {formatCount(s.count)}
          </p>
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-3" data-testid="preview-impact">
      <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
        <span>
          <span className="font-semibold tabular">{formatCount(result.changed)}</span> of {formatCount(result.evaluated)} answers from{' '}
          {windowLong(result.window)} would be handled differently ({formatPercent(share(result.changed, result.evaluated))}).
        </span>
        <EvidenceBadge
          label={simulated ? 'simulated' : 'measured'}
          basis="Replays the stored fast-check answers of your own traffic through the draft's rules."
          subject="Preview impact"
        />
      </p>
      <dl className="grid grid-cols-3 divide-x divide-border rounded-md border border-border">
        <Delta label="Newly blocked" value={result.newly_blocked} />
        <Delta label="Newly flagged" value={result.newly_flagged} />
        <Delta label="No longer flagged" value={result.no_longer_flagged} />
      </dl>
      <ChartTable
        caption="Answers by action, now and with the draft"
        columns={[
          { key: 'label', label: 'Action' },
          { key: 'current', label: 'Now', numeric: true },
          { key: 'proposed', label: 'With the draft', numeric: true },
        ]}
        rows={actions.map((a) => ({
          label: gateActionLabel(a),
          current: formatCount(result.by_action_current[a] ?? 0),
          proposed: formatCount(result.by_action_proposed[a] ?? 0),
        }))}
      />
      {wouldBlock > 0 ? (
        <p className="text-sm text-fg-muted">
          Of the {formatCount(wouldBlock)} answers the draft would block, the deep audit found{' '}
          <span className="font-medium text-fg tabular">{formatCount(agreement.would_block_hallucinated)}</span> hallucinated and{' '}
          <span className="font-medium text-fg tabular">{formatCount(agreement.would_block_grounded)}</span> grounded;{' '}
          {formatCount(agreement.would_block_unaudited)} were not audited.
        </p>
      ) : null}
      {result.examples.length > 0 ? (
        <div className="space-y-1">
          <h4 className="text-xs font-medium text-fg-muted">Examples</h4>
          <ul className="space-y-0.5 text-xs">
            {result.examples.slice(0, 5).map((e) => (
              <li key={e.trace_id}>
                <Link
                  to="/app/traffic/$traceId"
                  params={{ traceId: e.trace_id }}
                  className="inline-flex items-center gap-1 font-medium text-fg underline underline-offset-2"
                >
                  {gateActionLabel(e.current)}
                  <ArrowRight aria-hidden="true" className="size-3" />
                  <span className="sr-only">would become</span>
                  {gateActionLabel(e.proposed)}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {result.skipped.length > 0 || result.caveats.length > 0 ? (
        <ul className="space-y-0.5 text-xs text-fg-muted">
          {result.skipped.map((s) => (
            <li key={s.reason}>
              Not replayed: {dryRunSkipLabel(s.reason).toLowerCase()} ({formatCount(s.count)})
            </li>
          ))}
          {result.caveats.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
