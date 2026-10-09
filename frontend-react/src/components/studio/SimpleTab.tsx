/**
 * Studio's Simple form (SPEC §7.3 step 2): the few choices that make a check, in plain words. Every
 * change is written back with lib/studio/simpleForm.ts, which touches only the fields the change
 * concerns, so Advanced and JSON-only fields survive. A spec the form cannot show (a scale question,
 * structured text) says why and points to the JSON tab.
 */
import { Plus, Trash2 } from 'lucide-react';
import { useId, useState } from 'react';
import { DisabledReason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Field } from '@/components/ui/field';
import { IconButton } from '@/components/ui/icon-button';
import { Input, Textarea } from '@/components/ui/input';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Slider } from '@/components/ui/slider';
import { Switch } from '@/components/ui/switch';
import { formatRisk } from '@/domain/format';
import {
  applySimpleForm,
  optionKeyProblem,
  readSimpleForm,
  RECOMMENDED_CORROBORATORS,
  ROLE_WEIGHTS,
  roleOf,
  switchAnswerType,
  type AnswerType,
  type SimpleForm,
  type SimpleOption,
} from '@/lib/studio/simpleForm';
import type { EvaluatorSpec } from '@/types/api';
import { studioCorroboratorName, studioQuestionParts } from './studioWords';

/**
 * The question as the fast check reads it, every name in backticks shown by its readable label
 * (its raw name on hover), so the Simple tab never makes the reader decode `rules.competitors`.
 */
function ReadsAs({ text, params }: { text: string; params: EvaluatorSpec['params'] }) {
  const parts = studioQuestionParts(text, params);
  if (!parts.some((p) => p.kind === 'name')) return null;
  return (
    <p data-reads-as="" className="text-sm text-fg-muted">
      <span className="font-medium text-fg">Reads as: </span>
      {parts.map((p, i) =>
        p.kind === 'text' ? (
          <span key={i}>{p.text}</span>
        ) : (
          <span key={i} title={p.raw} className="rounded-sm bg-surface-sunken px-1 py-px font-medium text-fg">
            {p.label}
          </span>
        ),
      )}
    </p>
  );
}

const ANSWER_TYPES = [
  { value: 'yes_no', label: 'Yes/No' },
  { value: 'pick_one', label: 'Pick one' },
] as const;

const ROLE_OPTIONS = [
  { value: 'problem', label: 'Problem' },
  { value: 'borderline', label: 'Borderline' },
  { value: 'fine', label: 'Fine' },
] as const;

type RoleValue = keyof typeof ROLE_WEIGHTS | 'custom';

function OptionRow({
  option,
  others,
  disabled,
  canRemove,
  removeReasonId,
  onChange,
  onRemove,
}: {
  option: SimpleOption;
  others: readonly string[];
  disabled: boolean;
  canRemove: boolean;
  /** The id of the line that says why an option cannot be removed. */
  removeReasonId: string;
  onChange: (next: SimpleOption) => void;
  onRemove: () => void;
}) {
  const [key, setKey] = useState(option.key);
  const [keyProblem, setKeyProblem] = useState<string | null>(null);
  const role = roleOf(option.weight);
  const roleOptions = role === null ? [...ROLE_OPTIONS, { value: 'custom' as const, label: `Custom ${formatRisk(option.weight)}` }] : ROLE_OPTIONS;

  const commitKey = () => {
    if (key === option.key) return;
    const problem = optionKeyProblem(key, others);
    setKeyProblem(problem);
    if (!problem) onChange({ ...option, key });
  };

  return (
    // Option names are long snake_case keys: they get room for about 34 characters, and the
    // role control moves under them where the row is narrower than that.
    <li className="grid gap-2 rounded-md border border-border p-3 sm:grid-cols-2 sm:items-start xl:grid-cols-[minmax(0,17rem)_minmax(0,1fr)_auto]">
      <Field label="Option name" error={keyProblem} description={keyProblem ? undefined : 'Jev reads this name as part of the question, so make it descriptive.'}>
        <Input
          value={key}
          className="font-mono text-xs"
          disabled={disabled}
          onChange={(e) => setKey(e.target.value)}
          onBlur={commitKey}
          onKeyDown={(e) => {
            if (e.key === 'Enter') commitKey();
          }}
        />
      </Field>
      <Field label="What it means">
        {/* Grows with its text where the browser can (field-sizing), so a long meaning is never cut off. */}
        <Textarea
          rows={2}
          className="min-h-9 resize-none [field-sizing:content]"
          value={option.what}
          disabled={disabled}
          onChange={(e) => onChange({ ...option, what: e.target.value })}
        />
      </Field>
      <div className="flex items-end gap-1 sm:col-span-2 xl:col-span-1 xl:pt-5">
        <SegmentedControl<RoleValue>
          aria-label={`How bad is ${option.key}`}
          size="sm"
          options={roleOptions}
          value={role ?? 'custom'}
          disabled={disabled}
          onValueChange={(v) => {
            if (v !== 'custom') onChange({ ...option, weight: ROLE_WEIGHTS[v] });
          }}
        />
        <IconButton
          icon={Trash2}
          size="sm"
          aria-label={`Remove option ${option.key}`}
          disabled={disabled || !canRemove}
          aria-describedby={!disabled && !canRemove ? removeReasonId : undefined}
          onClick={onRemove}
        />
      </div>
    </li>
  );
}

function newOptionKey(existing: readonly string[]): string {
  for (let n = 1; ; n++) {
    const key = `new_option_${n}`;
    if (!existing.includes(key)) return key;
  }
}

export function SimpleTab({ spec, onChange, disabled = false, onOpenJson }: { spec: EvaluatorSpec; onChange: (spec: EvaluatorSpec) => void; disabled?: boolean; onOpenJson: () => void }) {
  const removeReasonId = useId();
  const read = readSimpleForm(spec);
  if (!read.ok) {
    return (
      <div className="space-y-2 rounded-md border border-border bg-surface-sunken p-3 text-sm">
        <p className="text-fg">{read.reason}</p>
        <Button size="sm" onClick={onOpenJson}>
          Open the JSON tab
        </Button>
      </div>
    );
  }
  const form = read.form;
  const set = (next: SimpleForm) => onChange(applySimpleForm(spec, next));
  const keys = form.options.map((o) => o.key);

  return (
    <div className="space-y-4">
      <Field label="Name" description="Shown on traces, in the library and in alerts.">
        <Input value={form.name} maxLength={80} disabled={disabled} onChange={(e) => set({ ...form, name: e.target.value })} />
      </Field>
      {/* The plain-English reading comes first; the raw question, with its backtick names, under it. */}
      <Field
        label="The question the fast check answers"
        description="A name in backticks points it at part of the request: `assistant_response` is your model's answer, `user_request` the user's message, `evidence` your documents, and `rules.…` one of the check's settings, such as `rules.brand_voice`."
      >
        <ReadsAs text={form.instructions} params={spec.params} />
        <Textarea rows={3} value={form.instructions} disabled={disabled} onChange={(e) => set({ ...form, instructions: e.target.value })} />
      </Field>
      <div className="space-y-1.5">
        <p className="text-sm font-medium text-fg">Answer type</p>
        <SegmentedControl<AnswerType>
          aria-label="Answer type"
          options={ANSWER_TYPES}
          value={form.answerType}
          disabled={disabled}
          onValueChange={(v) => set(switchAnswerType(form, v))}
        />
        <p className="text-xs text-fg-muted">A scale (1 to 5) is available in the JSON tab only: it calibrates poorly.</p>
      </div>

      {form.answerType === 'yes_no' ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="“Yes” means">
            <Input value={form.yes} disabled={disabled} onChange={(e) => set({ ...form, yes: e.target.value })} />
          </Field>
          <Field label="“No” means">
            <Input value={form.no} disabled={disabled} onChange={(e) => set({ ...form, no: e.target.value })} />
          </Field>
          <div className="space-y-1.5 sm:col-span-2">
            <p className="text-sm font-medium text-fg">It's a problem when the answer is</p>
            <SegmentedControl<'yes' | 'no'>
              aria-label="It's a problem when the answer is"
              size="sm"
              options={[
                { value: 'yes', label: 'Yes' },
                { value: 'no', label: 'No' },
              ]}
              value={form.problemWhen}
              disabled={disabled}
              onValueChange={(v) => set({ ...form, problemWhen: v })}
            />
          </div>
        </div>
      ) : (
        <div className="space-y-2">
          <p className="text-sm font-medium text-fg">Options</p>
          <p className="text-xs text-fg-muted">Problem counts fully toward the risk, Borderline half, Fine not at all. Keep one option for answers with nothing to judge.</p>
          <ul className="space-y-2">
            {form.options.map((o, i) => (
              <OptionRow
                key={`${i}-${o.key}`}
                option={o}
                others={keys.filter((k) => k !== o.key)}
                disabled={disabled}
                canRemove={form.options.length > 2}
                removeReasonId={removeReasonId}
                onChange={(next) => set({ ...form, options: form.options.map((x, j) => (j === i ? next : x)) })}
                onRemove={() => set({ ...form, options: form.options.filter((_, j) => j !== i) })}
              />
            ))}
          </ul>
          {!disabled && form.options.length <= 2 ? (
            <DisabledReason id={removeReasonId} reason={{ text: 'A pick-one question needs at least two options, so these two cannot be removed.' }} />
          ) : null}
          <Button size="sm" disabled={disabled} onClick={() => set({ ...form, options: [...form.options, { key: newOptionKey(keys), what: '', weight: 0 }] })}>
            <Plus aria-hidden="true" />
            Add an option
          </Button>
        </div>
      )}

      <Checkbox
        label="Needs the app's documents"
        description="Checks the answer against the documents in the request; without documents it is not checked."
        checked={form.needsDocuments}
        disabled={disabled}
        onCheckedChange={(checked) => set({ ...form, needsDocuments: checked })}
      />
      <Slider
        label="Flag when the risk reaches"
        value={form.warnAt}
        min={0}
        max={1}
        step={0.05}
        disabled={disabled}
        description="Lower flags more answers. Test it in step 3; quick calibration in step 4 suggests a value from your labels."
        onValueChange={(v) => set({ ...form, warnAt: v })}
      />
      <Switch
        label="Allow this check to block"
        description={`Only block when another signal agrees: ${RECOMMENDED_CORROBORATORS.map((c) => studioCorroboratorName(c)).join(', ')}. Blocking also needs a calibration (step 7).`}
        checked={form.allowBlock}
        disabled={disabled}
        onCheckedChange={(checked) => set({ ...form, allowBlock: checked })}
      />
    </div>
  );
}
