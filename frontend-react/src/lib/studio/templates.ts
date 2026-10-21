/**
 * "Start from a template" (SPEC §7.3 step 1, the primary start when no judge is configured): a
 * copy of a built-in evaluator as a new check of the viewer's own, or a blank Yes/No check. The
 * copy is a new evaluator (its own id, version 1, not built-in) that names the template as its
 * `parent`, so the library can show where it came from.
 */
import { BUILTIN_TEMPLATES, FACTUAL_CLAIMS_ID } from '@/lib/decision';
import type { EvaluatorSpec } from '@/types/api';
import { evaluatorIdFrom } from './draftToSpec';

export interface StudioTemplate {
  ref: string;
  name: string;
  category: EvaluatorSpec['category'];
  description: string | null;
}

/** The built-in templates a viewer can start from (the internal factual_claims is not one). */
export const STUDIO_TEMPLATES: readonly StudioTemplate[] = Object.entries(BUILTIN_TEMPLATES)
  .filter(([, spec]) => spec.id !== FACTUAL_CLAIMS_ID)
  .map(([ref, spec]) => ({ ref, name: spec.name, category: spec.category, description: spec.description ?? null }))
  .sort((a, b) => a.name.localeCompare(b.name));

/** The value of the "blank check" choice in the template picker. */
export const BLANK_TEMPLATE = 'blank';

/** A copy of the template `ref` as a new check, with an id not in `taken`. */
export function specFromTemplate(ref: string, taken: Iterable<string> = []): EvaluatorSpec {
  const template = BUILTIN_TEMPLATES[ref];
  if (!template) throw new Error(`There is no template ${ref}.`);
  const copy = structuredClone(template) as EvaluatorSpec;
  delete copy.builtin;
  delete copy.changelog;
  return {
    ...copy,
    id: evaluatorIdFrom(`${template.id} custom`, [...taken, template.id]),
    version: 1,
    name: `${template.name} (custom)`.slice(0, 80),
    parent: ref,
  };
}

/** A blank Yes/No check that reads the answer, ready for the Simple form. */
export function blankSpec(taken: Iterable<string> = []): EvaluatorSpec {
  return {
    spec_version: 'evaluator/v1',
    id: evaluatorIdFrom('custom check', taken),
    version: 1,
    name: 'Custom check',
    category: 'custom',
    state_fields: ['assistant_response'],
    question: {
      type: 'noul',
      instructions: 'Does `assistant_response` do what this check looks for?',
      criteria: { true: 'It does', false: 'It does not' },
    },
    evidence: { requires: 'none' },
    signal: {},
    outcomes: { warn_at: 0.6, critical_at: null, block_eligible: false, requires_corroboration: [], implies: [] },
    escalation: { escalate_from: null, numeric_or_date: false, on_error: 'ignore' },
  };
}
