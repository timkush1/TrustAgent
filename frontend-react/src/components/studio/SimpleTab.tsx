/**
 * Studio's Simple form (SPEC §7.3 step 2): the few choices that make a check, in plain words. Every
 * change is written back with lib/studio/simpleForm.ts, which touches only the fields the change
 * concerns, so Advanced and JSON-only fields survive. A spec the form cannot show (a scale question,
 * structured text) says why and points to the JSON tab.
 */
import { Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
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
import { studioCorroboratorName } from './studioWords';

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
  onChange,
  onRemove,
}: {
  option: SimpleOption;
  others: readonly string[];
  disabled: boolean;
  canRemove: boolean;
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
    <li className="grid gap-2 rounded-md border border-border p-3 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] sm:items-start">
      <Field label="Option name" error={keyProblem} description={keyProblem ? undefined : 'Part of the question: say what it means'}>
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
        <Input value={option.what} disabled={disabled} onChange={(e) => onChange({ ...option, what: e.target.value })} />
      </Field>
      <div className="flex items-end gap-1 sm:pt-5">
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
        <IconButton icon={Trash2} size="sm" aria-label={`Remove option ${option.key}`} disabled={disabled || !canRemove} onClick={onRemove} />
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
      <Field label="What should it catch?" description="The question the fast check answers. Name what it reads in backticks, such as `assistant_response`.">
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
                onChange={(next) => set({ ...form, options: form.options.map((x, j) => (j === i ? next : x)) })}
                onRemove={() => set({ ...form, options: form.options.filter((_, j) => j !== i) })}
              />
            ))}
          </ul>
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
