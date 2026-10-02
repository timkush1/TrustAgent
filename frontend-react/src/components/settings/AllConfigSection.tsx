/**
 * Settings → All configuration (SPEC §3.3; P4-PLAN ON-4, D33, D79): every setting, its value and
 * where the value comes from, read-only. Secrets never appear: an admin sees the masked form, other
 * callers see "Set" or "Not set" (D79). When this proxy does not send its settings, the page falls
 * back to the built-in defaults from the key registry and says so.
 */
import { useQuery } from '@tanstack/react-query';
import { useDeferredValue, useEffect, useMemo, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DegradedState } from '@/components/common/DegradedState';
import { PageHeader } from '@/components/common/PageHeader';
import { LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { describeError } from '@/components/common/describeError';
import { settingsQuery } from '@/data/queries';
import type { SettingKeyView, SettingsView } from '@/types/api';
import { SettingsWarnings } from './SettingsKit';

const SOURCES: Record<SettingKeyView['source'], string> = {
  default: 'Default',
  dotenv: '.env file',
  env: 'Environment',
  dashboard: 'Dashboard',
  file: 'File',
  seed: 'Environment (first start)',
};

function sourceLabel(source: SettingKeyView['source']): string {
  return SOURCES[source] ?? 'Unknown';
}

/** What a value cell shows: never a secret (D79). */
function valueText(k: SettingKeyView): { text: string; muted: boolean } {
  if (k.secret) {
    if (k.value === '' || k.value === undefined) return { text: 'Not set', muted: true };
    if (k.value === 'set') return { text: 'Set', muted: false };
    return { text: k.value, muted: false };
  }
  return k.value === '' ? { text: 'Empty', muted: true } : { text: k.value, muted: false };
}

interface Group {
  name: string;
  keys: SettingKeyView[];
}

interface RegistryKey {
  name: string;
  services: string[];
  default: string;
  secret: boolean;
  advanced: boolean;
  restart: boolean;
  group: string;
}

/** The registry defaults, grouped as the proxy groups them (the degraded fallback). */
async function registryGroups(): Promise<Group[]> {
  const registry = (await import('@contracts/config/keys.v1.json')).default as unknown as {
    groups: { id: string; title: string }[];
    keys: RegistryKey[];
  };
  return registry.groups.map((g) => ({
    name: g.title,
    keys: registry.keys
      .filter((k) => k.group === g.id)
      .map((k) => ({
        key: k.name,
        env: null,
        value: k.secret ? '' : k.default,
        default: k.secret ? '' : k.default,
        source: 'default' as const,
        secret: k.secret,
        advanced: k.advanced,
        restart_required: k.restart,
        editable: false,
        services: k.services,
      })),
  }));
}

function ConfigTable({ group }: { group: Group }) {
  return (
    <Card>
      <CardHeader level={2} title={group.name} />
      <CardContent className="overflow-x-auto">
        <table className="w-full min-w-[36rem] border-collapse text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs text-fg-muted">
              <th scope="col" className="py-1.5 pr-3 font-medium">Setting</th>
              <th scope="col" className="py-1.5 pr-3 font-medium">Value</th>
              <th scope="col" className="py-1.5 pr-3 font-medium">From</th>
              <th scope="col" className="py-1.5 font-medium">Notes</th>
            </tr>
          </thead>
          <tbody>
            {group.keys.map((k) => {
              const value = valueText(k);
              const changed = !k.secret && k.default !== '' && k.value !== k.default;
              return (
                <tr key={k.key} className="border-b border-border last:border-0 align-top">
                  <th scope="row" className="py-1.5 pr-3 text-left font-mono text-xs font-normal break-all text-fg">
                    {k.key}
                  </th>
                  <td className={value.muted ? 'py-1.5 pr-3 text-fg-subtle' : 'py-1.5 pr-3 font-mono text-xs break-all text-fg'}>
                    {value.text}
                    {changed ? <span className="block font-sans text-2xs text-fg-subtle">default {k.default}</span> : null}
                  </td>
                  <td className="py-1.5 pr-3 text-fg-muted">{sourceLabel(k.source)}</td>
                  <td className="py-1.5">
                    <span className="flex flex-wrap gap-1">
                      {k.restart_required ? <Badge emphasis="quiet">Restart to change</Badge> : null}
                      {k.secret ? <Badge emphasis="quiet">Secret</Badge> : null}
                      {k.advanced ? <Badge emphasis="quiet">Advanced</Badge> : null}
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </CardContent>
    </Card>
  );
}

function ConfigView({ groups, view }: { groups: Group[]; view: SettingsView | null }) {
  const [filter, setFilter] = useState('');
  const [advanced, setAdvanced] = useState(false);
  const deferred = useDeferredValue(filter.trim().toUpperCase());
  const shown = useMemo(
    () =>
      groups
        .map((g) => ({ ...g, keys: g.keys.filter((k) => (advanced || !k.advanced || deferred !== '') && (deferred === '' || k.key.includes(deferred))) }))
        .filter((g) => g.keys.length > 0),
    [groups, advanced, deferred],
  );
  const total = groups.reduce((n, g) => n + g.keys.length, 0);
  return (
    <div className="space-y-4">
      {view ? <SettingsWarnings warnings={view.warnings} /> : null}
      {view?.dotenv_path ? (
        <p className="text-sm text-fg-muted">
          Values marked “.env file” come from <code className="font-mono text-xs text-fg">{view.dotenv_path}</code>. Change them there and restart.
        </p>
      ) : null}
      <div className="flex flex-wrap items-end gap-4">
        <Field label="Filter by name" className="w-full max-w-xs">
          <Input value={filter} onValueChange={setFilter} placeholder="TRUTHTABLE_JEV" autoComplete="off" spellCheck={false} />
        </Field>
        <Switch label="Show advanced settings" checked={advanced} onCheckedChange={setAdvanced} />
      </div>
      {shown.length === 0 ? (
        <p role="status" className="text-sm text-fg-muted">
          No setting matches “{filter.trim()}” among {total} settings.
        </p>
      ) : (
        shown.map((g) => <ConfigTable key={g.name} group={g} />)
      )}
    </div>
  );
}

export function AllConfigSection() {
  const ds = useDataSource();
  const settings = useQuery(settingsQuery(ds));
  const [fallback, setFallback] = useState<Group[] | null>(null);
  const failed = settings.isError && settings.data === undefined;
  useEffect(() => {
    if (!failed || fallback) return;
    let live = true;
    void registryGroups().then((g) => {
      if (live) setFallback(g);
    });
    return () => {
      live = false;
    };
  }, [failed, fallback]);
  const showLoading = useDelayedLoading(settings.isPending || (failed && !fallback));

  let body;
  if (settings.data) {
    body = <ConfigView groups={settings.data.groups} view={settings.data} />;
  } else if (failed && fallback) {
    body = (
      <>
        <DegradedState
          title="Showing the built-in defaults"
          reason={`This proxy did not send its settings: ${describeError(settings.error).message}`}
          stillWorks="The values below are the defaults every setting starts from; your stack may override them in .env."
        />
        <ConfigView groups={fallback} view={null} />
      </>
    );
  } else {
    body = showLoading ? <LoadingState rows={6} /> : null;
  }
  return (
    <div className="space-y-5">
      <PageHeader description="Every setting of this stack, its value and where it comes from. Read-only: secrets are never shown." />
      {body}
    </div>
  );
}
