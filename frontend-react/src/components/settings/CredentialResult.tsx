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
import { copy, secBindingLabel, secJoinWords, secProbeReadyText, secSkipReasonText, secVerifyHintText, secVerifyView } from '@/domain/presentation';
import type { CredentialWriteResult } from '@/types/api';
import { probeCostBasis, probeCostLabel } from './credentialModel';

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
  // The proxy's own hint code for this check, in words (probe_skipped, data_policy, not_checked, …).
  const hint = secVerifyHintText(v?.hint);
  // OpenRouter accepted the key, but TrustAgent's own budget, limiter or breaker held the test
  // request back: the credits are not confirmed, so this is not "Ready".
  const probeSkipped = v?.ok === true && v.hint === 'probe_skipped';
  // A cost is shown only with its label (vendor, estimate, simulated).
  const costLabel = probeCostLabel(v?.cost_source);
  const cost = v?.cost_usd != null && costLabel ? formatUsd(v.cost_usd) : null;
  // Only OpenRouter keys carry a spending limit (D40, GET /api/v1/key). The proxy sends `limit_usd`
  // (null for no limit) only when OpenRouter's key check answered, with the hint `no_limit`; when
  // the field is left out, nobody asked, and the key may well have a limit.
  const noLimit = v?.ok === true && result.credential.provider === 'openrouter' && (v.hint === 'no_limit' || v.limit_usd === null);
  const ready = v?.ok === true && v.status === 'ok' && !probeSkipped;
  const Icon = v === null || probeSkipped ? Info : v.ok ? CircleCheck : CircleAlert;
  const tone =
    v === null || probeSkipped ? 'text-fg-muted' : v.ok ? 'text-tone-success' : v.status === 'unauthorized' ? 'text-tone-danger' : 'text-tone-warning';

  let headline: string;
  if (v === null) headline = 'Saved without a check.';
  else if (ready) headline = secProbeReadyText(v.latency_ms, cost);
  else if (probeSkipped) headline = hint ?? 'OpenRouter accepted the key; the credits are not confirmed yet.';
  else if (v.ok) headline = `Saved. ${hint ?? verify.hint ?? ''}`.trim();
  else if (v.status === 'no_credits') headline = copy.setup.step_connect.no_credits;
  // "Not checked" needs no label in front of its reason ("The fast check is off, so …").
  else if (v.status === 'unknown' && hint) headline = hint;
  else headline = `${verify.label}. ${hint ?? verify.hint ?? ''}`.trim();

  return (
    <div role="status" aria-live="polite" className="space-y-2 rounded-md border border-border bg-surface px-3 py-2.5 text-sm">
      <p className="flex flex-wrap items-start gap-1.5 text-fg">
        <Icon aria-hidden="true" className={`mt-0.5 size-4 shrink-0 ${tone}`} />
        <span className="tabular">{headline}</span>
        {ready && cost && costLabel ? (
          <EvidenceBadge label={costLabel} basis={probeCostBasis(costLabel)} subject="Key check cost" />
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
      {noLimit ? (
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
