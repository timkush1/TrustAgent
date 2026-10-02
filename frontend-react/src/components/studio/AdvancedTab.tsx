/**
 * Studio's Advanced tab (SPEC §7.3 step 2: "Advanced holds every other field"): the id, category,
 * description, the fields the question reads, evidence, every threshold, escalation, corroborators,
 * implied checks, the threshold basis, tags and what each option is not for. Parameters, option
 * examples and structured text stay in the JSON tab.
 */
import { Checkbox } from '@/components/ui/checkbox';
import { Field } from '@/components/ui/field';
import { Textarea } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { evaluatorCategoryLabel } from '@/domain/controlPresentation';
import { EVALUATOR_ID_PATTERN } from '@/lib/studio/draftToSpec';
import { fieldAt, parseList, parseThreshold, withField } from '@/lib/studio/specFields';
import type { EvaluatorSpec } from '@/types/api';
import { CommitInput } from './CommitInput';

const CATEGORIES: readonly EvaluatorSpec['category'][] = ['grounding', 'safety', 'compliance', 'quality', 'security', 'custom'];

const STATE_FIELDS: readonly { value: EvaluatorSpec['state_fields'][number]; label: string; description: string }[] = [
  { value: 'user_request', label: 'The user’s message', description: '`user_request`' },
  { value: 'assistant_response', label: 'The answer', description: '`assistant_response`' },
  { value: 'evidence', label: 'The app’s documents', description: '`evidence`' },
  { value: 'system_instructions', label: 'The system prompt’s instructions', description: '`system_instructions`' },
  { value: 'rules', label: 'Route rules (parameters)', description: '`rules.<name>`' },
];

const EVIDENCE = [
  { value: 'required', label: 'Required', description: 'Not checked without documents' },
  { value: 'optional', label: 'Optional', description: 'Uses documents when there are some' },
  { value: 'none', label: 'None', description: 'Never reads documents' },
] as const;

const ON_ERROR = [
  { value: 'escalate', label: 'Send to a deep audit', description: 'When the fast check cannot answer' },
  { value: 'ignore', label: 'Ignore', description: 'Treat as not checked' },
] as const;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

const thresholdText = (v: unknown) => (typeof v === 'number' ? String(v) : '');
const listText = (v: unknown) => (Array.isArray(v) ? v.join(', ') : '');

export function AdvancedTab({ spec, onChange, idLocked, disabled = false }: { spec: EvaluatorSpec; onChange: (spec: EvaluatorSpec) => void; idLocked: boolean; disabled?: boolean }) {
  const set = (path: readonly string[], value: unknown) => onChange(withField(spec, path, value));
  const threshold = (path: readonly string[], nullable: boolean) => (text: string) => {
    const v = parseThreshold(text);
    if (v === undefined) return 'Use a number from 0 to 1.';
    if (v === null && !nullable) return 'This threshold is required.';
    set(path, v);
    return null;
  };
  const fields = new Set(spec.state_fields);
  const criteria = spec.question.type === 'choice' && isObj(spec.question.criteria) ? spec.question.criteria : null;

  return (
    <div className="space-y-5">
      <div className="grid gap-3 sm:grid-cols-2">
        <CommitInput
          label="Id"
          mono
          value={spec.id}
          disabled={disabled || idLocked}
          description={idLocked ? 'A saved check keeps its id; a new version only changes what it asks.' : 'Lower-case letters, digits and underscores; used in policies as id@version.'}
          onCommit={(text) => {
            if (!EVALUATOR_ID_PATTERN.test(text)) return 'Use 2 to 48 lower-case letters, digits and underscores, starting with a letter.';
            set(['id'], text);
            return null;
          }}
        />
        <Field label="Category">
          <Select
            value={spec.category}
            disabled={disabled}
            options={CATEGORIES.map((c) => ({ value: c, label: evaluatorCategoryLabel(c) }))}
            onValueChange={(v) => v && set(['category'], v)}
          />
        </Field>
      </div>
      <Field label="Description" optional description="For people reading the library; the fast check never sees it.">
        <Textarea rows={2} maxLength={600} value={typeof spec.description === 'string' ? spec.description : ''} disabled={disabled} onChange={(e) => set(['description'], e.target.value || undefined)} />
      </Field>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-fg">What the question reads</legend>
        <div className="grid gap-2 sm:grid-cols-2">
          {STATE_FIELDS.map((f) => (
            <Checkbox
              key={f.value}
              label={f.label}
              description={f.description}
              checked={fields.has(f.value)}
              disabled={disabled}
              onCheckedChange={(checked) => {
                const next = STATE_FIELDS.map((s) => s.value).filter((v) => (v === f.value ? checked : fields.has(v)));
                set(['state_fields'], next);
              }}
            />
          ))}
        </div>
      </fieldset>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Documents">
          <Select value={spec.evidence.requires} disabled={disabled} options={EVIDENCE} onValueChange={(v) => v && set(['evidence', 'requires'], v)} />
        </Field>
        <CommitInput
          label="Fewest characters of documents"
          optional
          inputMode="numeric"
          value={typeof spec.evidence.min_chars === 'number' ? String(spec.evidence.min_chars) : ''}
          description="Less than this counts as no documents (default 40)."
          disabled={disabled}
          onCommit={(text) => {
            if (text.trim() === '') {
              set(['evidence', 'min_chars'], undefined);
              return null;
            }
            const n = Number(text);
            if (!Number.isInteger(n) || n < 0) return 'Use a whole number, 0 or more.';
            set(['evidence', 'min_chars'], n);
            return null;
          }}
        />
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-fg">Thresholds (raw risk, 0 to 1)</legend>
        <div className="grid gap-3 sm:grid-cols-2">
          <CommitInput label="Flag at" inputMode="decimal" value={thresholdText(spec.outcomes.warn_at)} disabled={disabled} onCommit={threshold(['outcomes', 'warn_at'], false)} />
          <CommitInput
            label="Treat as critical at"
            optional
            inputMode="decimal"
            value={thresholdText(spec.outcomes.critical_at)}
            description="Empty: never critical until a calibration measures it."
            disabled={disabled}
            onCommit={threshold(['outcomes', 'critical_at'], true)}
          />
          <CommitInput
            label="Send to a deep audit from"
            optional
            inputMode="decimal"
            value={thresholdText(fieldAt(spec, ['escalation', 'escalate_from']))}
            description="Must be below the flag threshold."
            disabled={disabled}
            onCommit={threshold(['escalation', 'escalate_from'], true)}
          />
          <CommitInput
            label="Lowest confidence to act on"
            optional
            inputMode="decimal"
            value={thresholdText(spec.outcomes.min_confidence)}
            disabled={disabled}
            onCommit={threshold(['outcomes', 'min_confidence'], true)}
          />
        </div>
      </fieldset>

      <div className="grid gap-3 sm:grid-cols-2">
        <Checkbox
          label="Numbers or dates decide it"
          description="Lets the number and date checks escalate an answer they disagree with."
          checked={fieldAt(spec, ['escalation', 'numeric_or_date']) === true}
          disabled={disabled}
          onCheckedChange={(checked) => set(['escalation', 'numeric_or_date'], checked)}
        />
        <Field label="When the fast check fails">
          <Select
            value={(fieldAt(spec, ['escalation', 'on_error']) as string | undefined) ?? 'escalate'}
            disabled={disabled}
            options={ON_ERROR}
            onValueChange={(v) => v && set(['escalation', 'on_error'], v)}
          />
        </Field>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <CommitInput
          label="Only block when these agree"
          optional
          mono
          value={listText(spec.outcomes.requires_corroboration)}
          placeholder="contradiction, det:numbers_not_in_evidence"
          description="Evaluator ids or instant checks (det:…), separated by commas."
          disabled={disabled}
          onCommit={(text) => {
            set(['outcomes', 'requires_corroboration'], parseList(text));
            return null;
          }}
        />
        <CommitInput
          label="Firing also means"
          optional
          mono
          value={listText(spec.outcomes.implies)}
          description="Evaluator ids this check implies, separated by commas."
          disabled={disabled}
          onCommit={(text) => {
            set(['outcomes', 'implies'], parseList(text));
            return null;
          }}
        />
        <CommitInput
          label="Tags"
          optional
          value={listText(spec.tags)}
          description="Up to 10, separated by commas."
          disabled={disabled}
          onCommit={(text) => {
            const tags = parseList(text);
            set(['tags'], tags.length > 0 ? tags : undefined);
            return null;
          }}
        />
      </div>
      <Field label="Why these thresholds" optional description="Where the numbers come from, for the next person to edit this check.">
        <Textarea rows={2} maxLength={400} value={spec.threshold_basis ?? ''} disabled={disabled} onChange={(e) => set(['threshold_basis'], e.target.value || undefined)} />
      </Field>

      {criteria ? (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">What each option is not for</legend>
          <p className="text-xs text-fg-muted">Helps the fast check tell close options apart.</p>
          {Object.entries(criteria).map(([key, option]) => {
            const what = typeof option === 'string' ? option : isObj(option) && typeof option.what === 'string' ? option.what : null;
            if (what === null) return null;
            const notFor = isObj(option) && typeof option.not_for === 'string' ? option.not_for : '';
            return (
              <Field key={key} label={key} optional>
                <Textarea
                  rows={1}
                  maxLength={600}
                  value={notFor}
                  disabled={disabled}
                  onChange={(e) => {
                    const base: Obj = isObj(option) ? { ...option } : { what };
                    if (e.target.value) base.not_for = e.target.value;
                    else delete base.not_for;
                    set(['question', 'criteria', key], base);
                  }}
                />
              </Field>
            );
          })}
        </fieldset>
      ) : null}
      <p className="text-xs text-fg-muted">Parameters, option examples and structured text are edited in the JSON tab.</p>
    </div>
  );
}
