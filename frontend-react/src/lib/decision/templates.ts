/**
 * The built-in evaluator templates (contracts/evaluators/<id>.v<version>.json, SPEC §2.8), keyed by
 * ref ("<id>@<version>"). Released templates are immutable; the question_hash vectors pin them.
 */
import type { EvaluatorSpec } from '@/lib/contracts/types.gen';
import { parseSpec, specRef, type Spec } from './spec';

const files = import.meta.glob<EvaluatorSpec>('@contracts/evaluators/*.json', { eager: true, import: 'default' });

/** Every built-in template document, keyed by ref. */
export const BUILTIN_TEMPLATES: Readonly<Record<string, EvaluatorSpec>> = Object.freeze(
  Object.fromEntries(Object.values(files).map((doc) => [`${doc.id}@${doc.version}`, doc])),
);

const parsed = new Map<string, Spec>();

/** The parsed built-in template for ref, or undefined when there is none. */
export function builtinSpec(ref: string): Spec | undefined {
  let spec = parsed.get(ref);
  if (spec === undefined && Object.hasOwn(BUILTIN_TEMPLATES, ref)) {
    spec = parseSpec(BUILTIN_TEMPLATES[ref]);
    if (specRef(spec) !== ref) throw new Error(`template ${ref} declares ${specRef(spec)}`);
    parsed.set(ref, spec);
  }
  return spec;
}
