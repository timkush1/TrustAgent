/**
 * The outcome of a key write or check (CredentialWriteResult, P4-PLAN D75, D76, D87), in words:
 * "Ready · 231 ms · $0.00001", what the key is now used for, why a check was skipped, and what to do
 * when it failed. No credits gets "I added credits — check again"; a key with no spending limit gets
 * the callout to set one. Shared by Settings → Connections and the setup wizard.
 */
import { CircleAlert, CircleCheck, Info, RotateCw } from 'lucide-react';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { Button } from '@/components/ui/button';
import { formatUsd } from '@/domain/format';
import { copy, secBindingLabel, secJoinWords, secProbeReadyText, secSkipReasonText, secVerifyView } from '@/domain/presentation';
import type { CredentialWriteResult, ValueLabel } from '@/types/api';

const COST_LABELS: Partial<Record<string, ValueLabel>> = { reported: 'vendor', computed: 'estimate', simulated: 'simulated' };

export interface CredentialResultProps {
  result: CredentialWriteResult;
  /** Simulated checks were on before this write (D76: a binding turns them off). */
  wasSimulated?: boolean;
  /** "I added credits — check again" (#57 with bind); left out where there is nothing to re-check. */
  onCheckAgain?: () => void;
  checkingAgain?: boolean;
}

export function CredentialResult({ result, wasSimulated = false, onCheckAgain, checkingAgain = false }: CredentialResultProps) {
  const v = result.verification;
  const { applied, skipped } = result.bindings;
  const verify = secVerifyView(v?.status);
  const costLabel = v?.cost_source ? COST_LABELS[v.cost_source] : undefined;
  const Icon = v === null ? Info : v.ok ? CircleCheck : CircleAlert;
  const tone = v === null ? 'text-fg-muted' : v.ok ? 'text-tone-success' : v.status === 'unauthorized' ? 'text-tone-danger' : 'text-tone-warning';

  let headline: string;
  if (v === null) headline = 'Saved without a check.';
  else if (v.ok && v.status === 'ok') headline = secProbeReadyText(v.latency_ms, v.cost_usd === null ? null : formatUsd(v.cost_usd));
  else if (v.ok) headline = `Saved. ${verify.hint ?? ''}`.trim();
  else if (v.status === 'no_credits') headline = copy.setup.step_connect.no_credits;
  else headline = `${verify.label}. ${verify.hint ?? ''}`.trim();

  return (
    <div role="status" aria-live="polite" className="space-y-2 rounded-md border border-border bg-surface px-3 py-2.5 text-sm">
      <p className="flex flex-wrap items-start gap-1.5 text-fg">
        <Icon aria-hidden="true" className={`mt-0.5 size-4 shrink-0 ${tone}`} />
        <span className="tabular">{headline}</span>
        {v?.ok && v.cost_usd !== null && costLabel ? (
          <EvidenceBadge label={costLabel} basis="The cost of this one key check, as the provider reported it." subject="Key check cost" />
        ) : null}
      </p>
      {!result.stored ? (
        <p className="pl-5.5 text-fg-muted">Nothing was saved: the key that is already in use keeps running your checks.</p>
      ) : null}
      {applied.length > 0 ? (
        <p className="pl-5.5 text-fg">Now used for: {secJoinWords(applied.map(secBindingLabel))}.</p>
      ) : null}
      {wasSimulated && applied.length > 0 ? <p className="pl-5.5 font-medium text-fg">{copy.setup.step_connect.simulated_off}.</p> : null}
      {skipped.length > 0 ? (
        <ul className="space-y-0.5 pl-5.5 text-fg-muted">
          {skipped.map((s) => (
            <li key={s.target}>{secSkipReasonText(s.reason, s.target)}</li>
          ))}
        </ul>
      ) : null}
      {v?.ok && v.status === 'ok' && v.limit_usd === null ? (
        <p className="flex items-start gap-1.5 rounded-sm bg-surface-sunken px-2 py-1.5 text-fg">
          <Info aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
          <span>
            {copy.setup.step_connect.no_limit}{' '}
            <a href="https://openrouter.ai/settings/keys" target="_blank" rel="noreferrer" className="font-medium underline underline-offset-2">
              Open openrouter.ai/settings/keys
            </a>
          </span>
        </p>
      ) : null}
      {v?.status === 'no_credits' && onCheckAgain ? (
        <div className="flex flex-wrap items-center gap-2 pl-5.5">
          <a href="https://openrouter.ai/settings/credits" target="_blank" rel="noreferrer" className="text-sm font-medium text-fg underline underline-offset-2">
            {copy.setup.step_connect.prerequisite_link}
          </a>
          <Button size="sm" onClick={onCheckAgain} pending={checkingAgain}>
            <RotateCw aria-hidden="true" />
            {copy.setup.step_connect.check_again}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
