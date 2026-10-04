/**
 * One provider on Settings → Connections (SPEC §3.3; ON-5): whether a key is connected and
 * where it comes from, what it is used for, where it may be sent, how its last check went, and the
 * actions: Connect (OpenRouter), Paste, Test (with "check again" after a failure) and Remove.
 * A key set in .env is read-only here; one set in the engine's .env is invisible to the gateway, so
 * it cannot be tested from here either (D80).
 */
import { useQueryClient } from '@tanstack/react-query';
import { CircleSlash, FlaskConical, LogIn } from 'lucide-react';
import { useEffect, useId, useState } from 'react';
import { DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { KeyValueList } from '@/components/common/KeyValue';
import { Badge, Chip } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card';
import { formatDateTime, formatRelativeTime } from '@/domain/format';
import { commandText, copy, secUsedByLabel, secVerifyView } from '@/domain/presentation';
import { useStartConnect, type ConnectOutcome } from '@/components/setup/connectFlow';
import { qkcSecurity } from '@/data/queries/security';
import type { CredentialVerifyRequest, CredentialView, SettingsView } from '@/types/api';
import { credentialState, defaultBindTargets, simulatedOn, type CardInfo } from './credentialModel';
import { CredentialResult } from './CredentialResult';
import { PasteKeyDialog } from './PasteKeyDialog';
import { RemoveCredential } from './RemoveCredential';
import { EnvManagedNote, GroupedAction, ProblemNote } from './SettingsKit';
import { useVerifyCredential } from './useCredentialWrites';

export interface CredentialCardProps {
  card: CardInfo;
  items: readonly CredentialView[];
  settings: SettingsView | undefined;
  /** Why key writes are unavailable here (read-only, demo, admin, store), or null. */
  writeReason: Reason | null;
  /** Why a check is unavailable here (read-only, demo, admin), or null. */
  testReason: Reason | null;
}

function StatusBadge({ credential }: { credential: CredentialView | null }) {
  if (!credential) {
    return (
      <Badge emphasis="quiet" icon={CircleSlash}>
        Not connected
      </Badge>
    );
  }
  if (credential.undecryptable) return <Badge tone="danger" emphasis="tint">Can't be read</Badge>;
  const view = secVerifyView(credential.last_verify_status);
  return (
    <Badge tone={view.tone} emphasis={view.tone === 'neutral' ? 'quiet' : 'tint'} icon={view.icon}>
      {view.label}
    </Badge>
  );
}

function UsedFor({ credential }: { credential: CredentialView }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-sm">
      <span className="text-fg-muted">Used for</span>
      {credential.used_by.length > 0 ? (
        credential.used_by.map((u) => <Chip key={u}>{secUsedByLabel(u)}</Chip>)
      ) : (
        <span className="text-fg-subtle">nothing yet</span>
      )}
    </div>
  );
}

export function CredentialCard({ card, items, settings, writeReason, testReason }: CredentialCardProps) {
  const state = credentialState(card, items);
  const credential = state.kind === 'none' ? null : state.credential;
  const verify = useVerifyCredential();
  const connect = useStartConnect('/app/settings/connections');
  const qc = useQueryClient();
  // The last outcome shown on this card: a Connect OpenRouter that returned here (/connect/callback
  // leaves it in the cache; read once, then dropped), or this card's own test.
  const [outcome, setOutcome] = useState<ConnectOutcome | null>(() =>
    card.name === 'openrouter' ? (qc.getQueryData<ConnectOutcome>(qkcSecurity.connectResult) ?? null) : null,
  );
  useEffect(() => {
    if (card.name === 'openrouter') qc.removeQueries({ queryKey: qkcSecurity.connectResult, exact: true });
  }, [qc, card.name]);
  const doctor = commandText('doctor') ?? 'make doctor';

  const reasonId = useId();
  // A key set in .env has no write actions here at all: the note says where to change it.
  const envReason = state.kind === 'env' || state.kind === 'engine_env';
  let checkReason: Reason | null = testReason;
  if (!checkReason && state.kind === 'engine_env') checkReason = { text: `Check it with ${doctor}` };
  if (!checkReason && state.kind === 'undecryptable') checkReason = { text: 'Paste the key again first' };

  // The one reason line under the actions: the write reason, or for a key set in .env the test's.
  const sharedReason = envReason ? checkReason : writeReason;
  const check = (request?: CredentialVerifyRequest) => {
    // Whether simulated checks were on before this check, so a bind can say they are now off (D76).
    const wasSimulated = simulatedOn(settings);
    verify.mutate({ name: card.name, request }, { onSuccess: (result) => setOutcome({ result, wasSimulated }) });
  };
  const test = () => check();
  const checkAgain = () => check({ bind: defaultBindTargets(card, settings) });

  return (
    <Card data-credential={card.name}>
      <CardHeader
        level={2}
        title={
          <span className="flex flex-wrap items-center gap-2">
            {card.title}
            {card.recommended ? <Badge emphasis="quiet">Recommended</Badge> : null}
          </span>
        }
        description={card.purpose}
        actions={<StatusBadge credential={credential} />}
      />
      <CardContent className="space-y-3">
        {credential && state.kind !== 'engine_env' ? (
          <KeyValueList
            items={[
              { label: 'Key', value: credential.masked || null, mono: true },
              ...(credential.fingerprint ? [{ label: 'Fingerprint', value: credential.fingerprint, mono: true }] : []),
              { label: 'Only sent to', value: credential.allowed_hosts.length > 0 ? credential.allowed_hosts.join(', ') : null, mono: true },
              {
                label: 'Last checked',
                value: credential.last_verified_at ? (
                  <time dateTime={credential.last_verified_at} title={formatDateTime(credential.last_verified_at)}>
                    {formatRelativeTime(credential.last_verified_at)}
                  </time>
                ) : (
                  'Not checked yet'
                ),
              },
            ]}
          />
        ) : null}
        {credential ? <UsedFor credential={credential} /> : null}
        {state.kind === 'env' ? <EnvManagedNote keys={[card.env]} /> : null}
        {state.kind === 'engine_env' ? <EnvManagedNote keys={[card.env]} engine /> : null}
        {state.kind === 'undecryptable' ? <p className="text-sm text-fg">{copy.settings.credential_undecryptable}</p> : null}
        {outcome ? (
          <CredentialResult result={outcome.result} wasSimulated={outcome.wasSimulated} onCheckAgain={checkAgain} checkingAgain={verify.isPending} />
        ) : null}
        {verify.isError ? <ProblemNote error={verify.error} title="The check did not run" /> : null}
        {connect.isError ? <ProblemNote error={connect.error} title="Connect OpenRouter did not start" /> : null}
      </CardContent>
      <CardFooter className="flex flex-col items-start gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {envReason ? null : (
            <>
              {card.name === 'openrouter' ? (
                <GroupedAction reason={writeReason} reasonId={reasonId}>
                  {(p) => (
                    <Button size="sm" variant={credential ? 'secondary' : 'primary'} onClick={() => connect.mutate()} pending={connect.isPending} {...p}>
                      <LogIn aria-hidden="true" />
                      {credential ? 'Connect again' : copy.setup.step_connect.primary}
                    </Button>
                  )}
                </GroupedAction>
              ) : null}
              <PasteKeyDialog
                card={card}
                settings={settings}
                writeReason={writeReason}
                reasonId={reasonId}
                label={credential ? 'Replace key' : card.name === 'openrouter' ? copy.setup.step_connect.paste_key : 'Paste a key'}
              />
            </>
          )}
          {credential ? (
            <GroupedAction reason={checkReason} reasonId={checkReason === sharedReason ? reasonId : undefined}>
              {(p) => (
                <Button size="sm" onClick={test} pending={verify.isPending} aria-label={`Test the ${card.title} key`} {...p}>
                  <FlaskConical aria-hidden="true" />
                  Test
                </Button>
              )}
            </GroupedAction>
          ) : null}
          {credential && !envReason ? <RemoveCredential credential={credential} writeReason={writeReason} reasonId={reasonId} /> : null}
        </div>
        {sharedReason ? <DisabledReason id={reasonId} reason={sharedReason} /> : null}
      </CardFooter>
    </Card>
  );
}
