/**
 * Studio step 5, Save a version (SPEC §7.3): the diff against the version the edit started from, a
 * changelog, and the save, which creates an immutable `evaluator_versions` row: `POST
 * /api/evaluators` for a new check (#31), or `POST /api/evaluators/:id/versions` with the
 * `base_version` the editor last saw (#32). A version saved elsewhere meanwhile answers 412: the
 * viewer reloads the latest (their edits stay) and saves again. Built-in and file checks are saved
 * as a new check of the viewer's own.
 */
import { Link } from '@tanstack/react-router';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, CircleCheck, RotateCw } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DiffView } from '@/components/common/DiffView';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { notify } from '@/components/common/notify';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Textarea } from '@/components/ui/input';
import { isApiError } from '@/data/errors';
import { qkc } from '@/data/queries';
import { formatCount } from '@/domain/format';
import { evaluatorIdFrom } from '@/lib/studio/draftToSpec';
import { sameSpec, specJson } from '@/lib/studio/specFields';
import type { EvaluatorMeta, EvaluatorSpec, EvaluatorVersion } from '@/types/api';
import { StudioStep } from './StudioStep';

/** The saved version a Studio edit builds on. */
export interface StudioBase {
  id: string;
  /** The latest version the editor has seen (sent as base_version). */
  latest: number;
  /** The spec of that version, for the diff. */
  spec: EvaluatorSpec;
  origin: EvaluatorMeta['origin'];
}

export interface SaveStepProps {
  spec: EvaluatorSpec;
  onSpecChange: (spec: EvaluatorSpec) => void;
  /** The version this session builds on (an edit, or what this session already saved). */
  base: StudioBase | null;
  /** Why the spec cannot be saved yet (errors in step 2), or null. */
  specReason: Reason | null;
  writeReason: Reason | null;
  taken: readonly string[];
  onSaved: (version: EvaluatorVersion) => void;
  /** Reloads the latest saved version after a 412 (the working spec stays). */
  onReloadLatest: () => Promise<void>;
  /** This is the step to do next: its action is the page's one primary button. */
  primary: boolean;
}

export function SaveStep({ spec, onSpecChange, base, specReason, writeReason, taken, onSaved, onReloadLatest, primary }: SaveStepProps) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const [changelog, setChangelog] = useState('');
  const [reloading, setReloading] = useState(false);
  const asVersion = base !== null && base.origin === 'dashboard' && base.id === spec.id;
  const nextVersion = asVersion ? base.latest + 1 : 1;
  const toSave: EvaluatorSpec = { ...spec, version: nextVersion };

  const save = useMutation({
    mutationFn: () =>
      asVersion
        ? ds.createEvaluatorVersion(spec.id, { spec: toSave, changelog: changelog.trim(), base_version: base.latest })
        : ds.createEvaluator({ spec: toSave, changelog: changelog.trim() }),
    onSuccess: (version) => {
      void queryClient.invalidateQueries({ queryKey: qkc.evaluators.all });
      notify.success(`Saved ${version.spec.name}, version ${version.version}`, { description: 'Watch it on a route next: nothing changes for users until you do.' });
      setChangelog('');
      onSaved(version);
    },
  });

  const unchanged = base !== null && base.id === spec.id && sameSpec(base.spec, spec);
  let reason: Reason | null = writeReason ?? specReason;
  if (!reason && unchanged) reason = { text: `No changes since version ${base?.latest ?? ''}` };
  if (!reason && changelog.trim() === '') reason = { text: 'Say what changed and why first' };

  const err = save.error;
  const status = isApiError(err) ? err.status : 0;
  const code = isApiError(err) ? err.code : '';
  const stale = status === 412 || code === 'stale_version';
  const fileManaged = code === 'file_managed';
  const builtin = status === 403 && asVersion;
  const idTaken = status === 409 && !fileManaged && !asVersion;
  const failure = err && !stale ? describeError(err) : null;
  const fieldErrors = isApiError(err) ? err.fieldErrors : [];

  const saveAsNew = () => {
    const id = evaluatorIdFrom(`${spec.id} custom`, [...taken, spec.id]);
    onSpecChange({ ...spec, id, parent: base ? `${base.id}@${base.latest}` : spec.parent });
    save.reset();
  };
  const suggestedId = idTaken ? evaluatorIdFrom(spec.id, [...taken, spec.id]) : null;

  const reload = async () => {
    setReloading(true);
    try {
      await onReloadLatest();
      save.reset();
    } finally {
      setReloading(false);
    }
  };

  return (
    <StudioStep
      n={5}
      id="step-save"
      title="Save a version"
      description="Versions never change once saved. Saving does not attach the check to any route."
    >
      <p className="text-sm text-fg">
        {asVersion ? (
          <>
            Saves <span className="font-mono text-xs">{spec.id}</span> as version {formatCount(nextVersion)}.
          </>
        ) : (
          <>
            Saves a new check, <span className="font-mono text-xs">{spec.id}</span>, as version 1.
          </>
        )}
      </p>
      <DiffView label="Changes to the check" before={base ? specJson({ ...base.spec, version: nextVersion }) : ''} after={specJson(toSave)} />
      <Field label="What changed and why" description="Kept with the version, for whoever reads its history.">
        <Textarea rows={2} maxLength={2000} value={changelog} onChange={(e) => setChangelog(e.target.value)} />
      </Field>
      <DisabledAction reason={reason}>
        {(p) => (
          <Button variant={primary ? 'primary' : 'secondary'} pending={save.isPending} onClick={() => save.mutate()} {...p}>
            {asVersion ? `Save version ${nextVersion}` : 'Save as a new check'}
          </Button>
        )}
      </DisabledAction>

      {stale ? (
        <div role="alert" className="space-y-2 rounded-md border border-border p-3 text-sm">
          <p className="flex items-start gap-1.5 text-fg">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
            Someone saved a newer version of this check while you were editing. Reload it to see what changed; your edits stay here.
          </p>
          <Button size="sm" pending={reloading} onClick={() => void reload()}>
            <RotateCw aria-hidden="true" />
            Reload the latest version
          </Button>
        </div>
      ) : null}
      {failure ? (
        <div role="alert" className="space-y-2 rounded-md border border-border p-3 text-sm">
          <p className="flex items-start gap-1.5 text-fg">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
            {idTaken ? `A check with the id ${spec.id} already exists.` : `Couldn't save: ${failure.message}`}
          </p>
          {fieldErrors.length > 0 ? (
            <ul className="list-disc space-y-0.5 pl-5 text-fg-muted">
              {fieldErrors.map((f, i) => (
                <li key={`${f.path}-${i}`}>
                  {f.path ? <span className="font-mono text-2xs">{f.path} · </span> : null}
                  {f.message}
                </li>
              ))}
            </ul>
          ) : null}
          {idTaken && suggestedId ? (
            <Button size="sm" onClick={() => onSpecChange({ ...spec, id: suggestedId })}>
              Use the id {suggestedId}
            </Button>
          ) : null}
          {fileManaged || builtin ? (
            <>
              <p className="text-fg-muted">{fileManaged ? 'This check comes from a file on the server, so it changes there.' : 'Built-in checks never get new versions.'} You can save your edit as a new check instead.</p>
              <Button size="sm" onClick={saveAsNew}>
                Save as a new check instead
              </Button>
            </>
          ) : null}
          {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
        </div>
      ) : null}
      {save.data ? (
        <p role="status" className="flex flex-wrap items-center gap-1.5 text-sm text-fg">
          <CircleCheck aria-hidden="true" className="size-4 text-tone-success" />
          Saved as <span className="font-mono text-xs">{save.data.evaluator_id}@{save.data.version}</span>.
          <Link to="/app/evaluators/$id" params={{ id: save.data.evaluator_id }} className="font-medium underline underline-offset-2">
            See it in the library
          </Link>
        </p>
      ) : null}
    </StudioStep>
  );
}
