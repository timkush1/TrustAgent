/**
 * Settings → Connections (SPEC §3.3; ON-5): the keys TrustAgent uses to check answers.
 * Cards for OpenRouter, TypeSafe, Anthropic, OpenAI and another gateway, each with Connect or Paste,
 * Test, Remove and "Used for"; keys set in .env (the gateway's or the engine's) read-only; keys the
 * master key cannot read; and the credentials store when keys cannot be saved here (D32). Upstream
 * keys saved for your own model provider are listed after the cards.
 */
import { useQuery } from '@tanstack/react-query';
import { KeyRound } from 'lucide-react';
import { useDataSource } from '@/app/services';
import { KeyValueList } from '@/components/common/KeyValue';
import { PageHeader } from '@/components/common/PageHeader';
import { QueryState } from '@/components/common/QueryState';
import { EmptyState, LoadingState } from '@/components/common/StateViews';
import { DisabledAction, DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { settingsQuery, statusQuery } from '@/data/queries';
import { credentialsQuery } from '@/data/queries/security';
import { copy, secCredentialLabel, secUsedByLabel } from '@/domain/presentation';
import type { CredentialView } from '@/types/api';
import { CARDS, envVarOf, otherCredentials } from './credentialModel';
import { CredentialCard } from './CredentialCard';
import { RemoveCredential } from './RemoveCredential';
import { EnvManagedNote, SettingsWarnings, StoreNotice } from './SettingsKit';
import { useCredentialWriteReason, useSettingsWriteReason } from './useSettingsAccess';

function UpstreamKeys({ items, writeReason }: { items: readonly CredentialView[]; writeReason: ReturnType<typeof useCredentialWriteReason> }) {
  if (items.length === 0) return null;
  return (
    <Card>
      <CardHeader level={2} title="Upstream keys" description="Keys the gateway adds when it forwards your app's requests to your model provider. Each is sent only to the addresses listed." />
      <CardContent>
        <ul aria-label="Upstream keys" className="divide-y divide-border">
          {items.map((c) => (
            <li key={c.name} className="flex flex-wrap items-start justify-between gap-3 py-3">
              <div className="min-w-0 flex-1 space-y-1">
                <p className="text-sm font-medium text-fg">{secCredentialLabel(c.name)}</p>
                <KeyValueList
                  items={[
                    { label: 'Key', value: c.masked || null, mono: true },
                    { label: 'Only sent to', value: c.allowed_hosts.join(', ') || null, mono: true },
                    { label: 'Used for', value: c.used_by.length > 0 ? c.used_by.map(secUsedByLabel).join(', ') : 'nothing yet' },
                  ]}
                />
                {c.source === 'env' ? <EnvManagedNote keys={[envVarOf(c.name) ?? c.name]} /> : null}
              </div>
              {c.source === 'env' ? null : <RemoveCredential credential={c} writeReason={writeReason} />}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

export function ConnectionsSection() {
  const ds = useDataSource();
  const status = useQuery(statusQuery(ds));
  const settings = useQuery(settingsQuery(ds));
  const writeReason = useCredentialWriteReason();
  const testReason = useSettingsWriteReason();
  const scopes = status.data?.auth.caller_scopes;
  const admin = scopes ? scopes.includes('admin') : !status.isPending;
  const credentials = useQuery({ ...credentialsQuery(ds), enabled: admin });
  // On the demo stack every card would repeat the same reason and fix: the page says it once, and
  // each card keeps a short reason beside its disabled buttons.
  const demo = writeReason === 'demo' || testReason === 'demo';
  const short = (r: Reason | null): Reason | null => (r === 'demo' ? { text: 'Not on the demo stack' } : r);
  const simulated = status.data?.decision_model.backend === 'mock';

  let body;
  if (status.isPending) {
    body = <LoadingState rows={4} />;
  } else if (!admin) {
    body = (
      <EmptyState
        icon={KeyRound}
        title="Only admins see connections"
        action={<DisabledAction reason="needs_admin">{(p) => <Button {...p}>{copy.setup.step_connect.primary}</Button>}</DisabledAction>}
      >
        This browser is signed in without the admin scope.
      </EmptyState>
    );
  } else {
    body = (
      <QueryState query={credentials} loading={<LoadingState rows={5} />}>
        {(data) => (
          <div className="space-y-4">
            {demo ? (
              <div className="space-y-1">
                <p className="text-sm text-fg">This demo runs simulated checks (a mock fast check and a mock judge), so no keys are connected here. You add keys on your own stack.</p>
                <DisabledReason reason="demo" />
              </div>
            ) : data.items.length === 0 ? (
              <p className="text-sm text-fg">
                {simulated ? 'No key is connected, so the checks are simulated. ' : 'Nothing is connected yet. '}Connect OpenRouter to run both checks for real with one key.
              </p>
            ) : null}
            <div className="grid gap-4 xl:grid-cols-2">
              {CARDS.map((card) => (
                <CredentialCard
                  key={card.name}
                  card={card}
                  items={data.items}
                  settings={settings.data}
                  writeReason={short(writeReason)}
                  testReason={short(testReason)}
                />
              ))}
            </div>
            <UpstreamKeys items={otherCredentials(data.items)} writeReason={short(writeReason)} />
          </div>
        )}
      </QueryState>
    );
  }

  return (
    <div className="space-y-5">
      <PageHeader description="The keys TrustAgent uses to check answers. Connect OpenRouter to run both checks with one key, or paste a key per provider. Saved keys are encrypted and only ever shown masked." />
      <p className="text-sm text-fg-muted">{copy.setup.step_connect.privacy}</p>
      <StoreNotice />
      {/* StoreNotice already names the keys the master key cannot read. */}
      {settings.data ? <SettingsWarnings warnings={settings.data.warnings} skip={status.data ? ['master_key_changed'] : []} /> : null}
      {body}
    </div>
  );
}
