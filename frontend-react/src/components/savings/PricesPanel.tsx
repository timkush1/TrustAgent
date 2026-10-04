/**
 * Settings → Budgets → Prices (D53): the price table cost estimates use, and your
 * overrides on top of it. GET /api/pricing serves the effective table (your overrides first, then
 * a price file, then the built-in vendor prices); the overrides are `pricing_overrides` in the
 * runtime settings, saved with If-Match like every setting. The server's ValidateOverrides refuses
 * a $0 price for a provider that bills, a repeat of an earlier override and a malformed entry,
 * shown on the row they concern, and warns about a price far above the vendor's.
 */
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, Plus, Trash2 } from 'lucide-react';
import { useId, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { describeError } from '@/components/common/describeError';
import { notify } from '@/components/common/notify';
import { ErrorState } from '@/components/common/StateViews';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { IconButton } from '@/components/ui/icon-button';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { isNotServed } from '@/data/queries';
import { pricingQuery } from '@/data/queries/costs';
import { qkcSecurity } from '@/data/queries/security';
import { costOverrideFinding, costOverrideNote, costPriceNote, costPricePair, costPriceProviderLabel, costPriceSourceWord } from '@/domain/presentation';
import type { FieldError, PriceEntry, SettingsView } from '@/types/api';
import {
  emptyRow,
  overrideFindings,
  overridesFromRows,
  rebaseOverrides,
  rowProblems,
  rowsFromOverrides,
  sameOverrides,
  type OverrideRow,
  type RowField,
} from './budgetsModel';
import { useSettingsDraft } from './useSettingsDraft';

const readOverrides = (s: SettingsView): PriceEntry[] => s.runtime.pricing_overrides;

const KNOWN_PROVIDERS = ['openrouter', 'typesafe', 'anthropic', 'openai', 'ollama'];

/** The effective table: what every cost estimate is priced with right now. */
function EffectiveTable() {
  const ds = useDataSource();
  const q = useQuery(pricingQuery(ds));
  if (q.isPending) return <Skeleton className="h-32 w-full" />;
  if (q.isError) {
    return isNotServed(q.error) ? (
      <p className="text-sm text-fg-muted">This proxy does not serve its price table (GET /api/pricing): it is older than this dashboard.</p>
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  }
  return (
    <div className="overflow-x-auto rounded-md border border-border">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">Prices in use, first match wins (version {q.data.version})</caption>
        <thead>
          <tr className="border-b border-border text-xs text-fg-muted">
            <th scope="col" className="px-3 py-1.5 text-left font-medium">
              Model
            </th>
            <th scope="col" className="px-3 py-1.5 text-right font-medium">
              USD per million tokens
            </th>
            <th scope="col" className="px-3 py-1.5 text-left font-medium">
              Source
            </th>
          </tr>
        </thead>
        <tbody>
          {q.data.entries.map((e, i) => (
            <tr key={`${e.provider}/${e.match}/${i}`} data-price-label={e.label ?? 'none'} className="border-b border-border last:border-b-0">
              <th scope="row" className="px-3 py-1.5 text-left align-top font-normal">
                <span className="block font-mono text-xs text-fg">{e.match}</span>
                <span className="block text-xs text-fg-muted">
                  {costPriceProviderLabel(e.provider)}
                  {costPriceNote(e.note) ? ` · ${costPriceNote(e.note)}` : ''}
                </span>
              </th>
              <td className="px-3 py-1.5 text-right align-top text-fg tabular">{costPricePair(e)}</td>
              <td className="px-3 py-1.5 align-top">
                {e.label !== 'vendor_price' ? (
                  // Your override, or an entry with no vendor behind it: never labelled Vendor.
                  <Badge size="sm">{costPriceSourceWord(e.label)}</Badge>
                ) : (
                  <EvidenceBadge
                    label="vendor"
                    basis={`${costPriceSourceWord(e.label)}${costPriceNote(e.note) ? `. ${costPriceNote(e.note)}` : ''}`}
                    sourceUrl={e.source}
                    asOf={e.as_of}
                    subject={`${e.match} price`}
                  />
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

interface OverrideEditorProps {
  rows: OverrideRow[];
  onChange: (rows: OverrideRow[]) => void;
  /** Client-side problems, shown once the viewer tried to save. */
  showProblems: boolean;
  /** The server's findings on `pricing_overrides[i]`, by row index. */
  serverFindings: Map<number, FieldError[]>;
  /** Saving is off here (read-only, not an admin, no database): the rows are shown, not edited. */
  readOnly: boolean;
  /** A save is in flight. */
  busy: boolean;
}

function OverrideEditor({ rows, onChange, showProblems, serverFindings, readOnly, busy }: OverrideEditorProps) {
  const listId = useId();
  const set = (id: string, patch: Partial<OverrideRow>) => onChange(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  if (rows.length === 0) {
    return <p className="text-sm text-fg-muted">No overrides: every estimate uses the prices above.</p>;
  }
  return (
    <ol className="space-y-3">
      <datalist id={listId}>
        {KNOWN_PROVIDERS.map((p) => (
          <option key={p} value={p} />
        ))}
      </datalist>
      {rows.map((row, index) => {
        const problems: Partial<Record<RowField, string>> = showProblems ? rowProblems(row) : {};
        const findings = serverFindings.get(index) ?? [];
        const byField = (f: RowField): string | undefined =>
          problems[f] ??
          (findings
            .filter((x) => x.path.endsWith(`.${f}`))
            .map((x) => costOverrideFinding(x, row.provider))
            .join(' ') ||
            undefined);
        const rowLevel = findings.filter((x) => !/\.(provider|match|input_per_mtok|output_per_mtok|note)$/.test(x.path));
        return (
          <li key={row.id} data-override-row={index} className="space-y-2 rounded-md border border-border p-3">
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs font-medium text-fg-muted">Override {index + 1}</p>
              {readOnly ? null : (
                <IconButton
                  icon={Trash2}
                  size="sm"
                  aria-label={`Remove override ${index + 1}`}
                  disabled={busy}
                  onClick={() => onChange(rows.filter((r) => r.id !== row.id))}
                />
              )}
            </div>
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
              <Field label="Provider" error={byField('provider')}>
                <Input value={row.provider} onValueChange={(v) => set(row.id, { provider: v })} readOnly={readOnly} list={listId} autoComplete="off" />
              </Field>
              <Field label="Model" description="A * matches any ending." error={byField('match')}>
                <Input value={row.match} onValueChange={(v) => set(row.id, { match: v })} readOnly={readOnly} placeholder="gpt-4o-mini*" className="font-mono" />
              </Field>
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              <Field label="Input, USD per million tokens" error={byField('input_per_mtok')}>
                <Input value={row.input} onValueChange={(v) => set(row.id, { input: v })} readOnly={readOnly} inputMode="decimal" className="tabular" />
              </Field>
              <Field label="Output, USD per million tokens" error={byField('output_per_mtok')}>
                <Input value={row.output} onValueChange={(v) => set(row.id, { output: v })} readOnly={readOnly} inputMode="decimal" className="tabular" />
              </Field>
              <Field label="Note" optional error={byField('note')}>
                <Input value={row.note} onValueChange={(v) => set(row.id, { note: v })} readOnly={readOnly} placeholder="negotiated rate" />
              </Field>
            </div>
            {rowLevel.length > 0 ? (
              <p role="alert" className="flex items-start gap-1 text-xs text-fg">
                <CircleAlert aria-hidden="true" className="mt-px size-3.5 shrink-0 text-tone-danger" />
                {rowLevel.map((x) => costOverrideFinding(x, row.provider)).join(' ')}
              </p>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}

export interface PricesPanelProps {
  settings: SettingsView;
  /** Why the overrides cannot be saved here (read-only, not an admin, no database), or null. */
  writeReason: Reason | null;
}

export function PricesPanel({ settings, writeReason }: PricesPanelProps) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  // The rows as edited, over the version they were based on (its ETag is the save's If-Match).
  const form = useSettingsDraft<PriceEntry[], OverrideRow[]>(settings, {
    read: readOverrides,
    toDraft: rowsFromOverrides,
    rebase: rebaseOverrides,
  });
  const rows = form.draft;
  const setRows = (next: OverrideRow[]) => form.setDraft(() => next);
  const [showProblems, setShowProblems] = useState(false);
  const [findings, setFindings] = useState<FieldError[]>([]);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);

  const dirty = !sameOverrides(rows, form.base);

  const byRow = new Map<number, FieldError[]>();
  for (const f of overrideFindings(findings)) byRow.set(f.index, [...(byRow.get(f.index) ?? []), f.finding]);

  const save = async () => {
    setShowProblems(true);
    if (rows.some((r) => Object.keys(rowProblems(r)).length > 0)) return;
    setPending(true);
    setError(null);
    setFindings([]);
    setWarnings([]);
    try {
      const view = await ds.patchSettings({ pricing_overrides: overridesFromRows(rows) }, form.etag);
      queryClient.setQueryData(qkcSecurity.settings, view);
      form.reset(view);
      setShowProblems(false);
      setWarnings(view.warnings.filter((w) => w.key === 'pricing_overrides').map((w) => costOverrideFinding({ code: w.code, message: w.message })));
      void queryClient.invalidateQueries({ queryKey: pricingQuery(ds).queryKey });
      notify.success('Prices saved');
    } catch (err) {
      if (isApiError(err) && err.status === 422 && overrideFindings(err.fieldErrors).length > 0) {
        setFindings(err.fieldErrors);
        // Findings on no row (the list as a whole) are shown with the save's failure, never dropped.
        if (err.fieldErrors.length > overrideFindings(err.fieldErrors).length) setError(err);
      } else setError(err);
    } finally {
      setPending(false);
    }
  };

  const reason: Reason | null = writeReason ?? (dirty ? null : { text: 'Nothing to save yet' });
  const failure = error ? describeError(error) : null;
  const onRow = new Set(isApiError(error) ? overrideFindings(error.fieldErrors).map((f) => f.finding) : []);
  const listFindings = isApiError(error) ? error.fieldErrors.filter((f) => !onRow.has(f)) : [];
  const stale = isApiError(error) && error.code === 'stale_version';

  return (
    <div className="space-y-4">
      <EffectiveTable />
      <section aria-labelledby="price-overrides" className="space-y-3">
        <div className="space-y-0.5">
          <h3 id="price-overrides" className="text-sm font-semibold text-fg">
            Your prices
          </h3>
          <p className="text-xs text-fg-muted">{costOverrideNote} The first one that matches a model wins.</p>
        </div>
        <OverrideEditor
          rows={rows}
          onChange={setRows}
          showProblems={showProblems}
          serverFindings={byRow}
          readOnly={writeReason !== null}
          busy={pending}
        />
        {warnings.length > 0 ? (
          <ul aria-label="Check these prices" className="space-y-1">
            {warnings.map((w, i) => (
              <li key={i} className="flex items-start gap-1 text-xs text-fg">
                <CircleAlert aria-hidden="true" className="mt-px size-3.5 shrink-0 text-tone-warning" />
                {w}
              </li>
            ))}
          </ul>
        ) : null}
        {failure ? (
          <div role="alert" className="space-y-1 rounded-md border border-border bg-surface px-3 py-2 text-sm">
            <p className="text-fg">Couldn’t save the prices: {failure.message}</p>
            {listFindings.map((f, i) => (
              <p key={i} className="text-fg">
                {costOverrideFinding(f)}
              </p>
            ))}
            {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
            {stale ? (
              <Button
                size="sm"
                onClick={() => {
                  // Drop the edits and follow the latest version (the refetch brings anything newer).
                  form.reset(settings);
                  setFindings([]);
                  setShowProblems(false);
                  setError(null);
                  void queryClient.invalidateQueries({ queryKey: qkcSecurity.settings });
                }}
              >
                Reload settings
              </Button>
            ) : null}
          </div>
        ) : null}
        <div className="flex flex-wrap items-center gap-2">
          {/* Where saving is off, the Save button says why; adding a row there would be a dead end. */}
          {writeReason === null ? (
            <Button size="sm" variant="ghost" disabled={pending} onClick={() => setRows([...rows, emptyRow()])}>
              <Plus aria-hidden="true" />
              Add a price
            </Button>
          ) : null}
          <DisabledAction reason={reason}>
            {(p) => (
              <Button size="sm" variant="primary" pending={pending} onClick={() => void save()} {...p}>
                Save prices
              </Button>
            )}
          </DisabledAction>
          {dirty && writeReason === null ? (
            <Button
              size="sm"
              variant="ghost"
              disabled={pending}
              onClick={() => {
                form.reset(settings);
                setFindings([]);
                setShowProblems(false);
                setError(null);
              }}
            >
              Discard changes
            </Button>
          ) : null}
        </div>
      </section>
    </div>
  );
}

