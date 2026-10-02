/**
 * Studio's AI draft (evaluator-draft.v1, SPEC §2.8) as an evaluator spec the viewer can review,
 * test and save: the draft-only members (rationale, split suggestions, test samples) go, and an id
 * derived from the name and version 1 come in. Every other member keeps its value and its order,
 * the way the gateway turns a draft into a spec (Go decision.draftSpec). The draft's six test
 * samples become the test bench's first samples, labelled as the drafter intended.
 *
 * A draft is never saved or attached automatically (SPEC §7.3): this only prepares the editor.
 */
import type { EvaluatorDraft, EvaluatorSpec, StudioSample } from '@/types/api';

/** evaluator.v1 `id`. */
export const EVALUATOR_ID_PATTERN = /^[a-z][a-z0-9_]{1,47}$/;
const MAX_ID = 48;

/** The members of evaluator-draft.v1 that are not part of a spec. */
const DRAFT_ONLY = new Set(['rationale', 'split_suggestions', 'test_samples']);

/**
 * An evaluator id from a name: lower case, words joined by "_", starting with a letter, at most 48
 * characters; `taken` ids get a numeric suffix ("blames_customer_2").
 */
export function evaluatorIdFrom(name: string, taken: Iterable<string> = []): string {
  let base = name
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
  if (!/^[a-z]/.test(base)) base = `check_${base}`.replace(/_+$/, '');
  if (base.length < 2) base = 'custom_check';
  base = base.slice(0, MAX_ID).replace(/_+$/, '');
  const used = new Set(taken);
  if (!used.has(base)) return base;
  for (let n = 2; ; n++) {
    const suffix = `_${n}`;
    const candidate = `${base.slice(0, MAX_ID - suffix.length).replace(/_+$/, '')}${suffix}`;
    if (!used.has(candidate)) return candidate;
  }
}

/** The draft as a spec with `id` (derived from the name unless given) and version 1. */
export function draftToSpec(draft: EvaluatorDraft, options: { id?: string; taken?: Iterable<string> } = {}): EvaluatorSpec {
  const id = options.id ?? evaluatorIdFrom(draft.name, options.taken);
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(draft)) {
    if (key === 'spec_version') {
      out.spec_version = value;
      out.id = id;
      out.version = 1;
      continue;
    }
    if (!DRAFT_ONLY.has(key)) out[key] = structuredClone(value);
  }
  if (!('id' in out)) Object.assign(out, { id, version: 1 });
  return out as unknown as EvaluatorSpec;
}

/** The draft's test samples for the test bench ("d1"…"d6"), labelled "fire" / "no_fire" as drafted. */
export function draftSamples(draft: EvaluatorDraft): StudioSample[] {
  return draft.test_samples.map((s, i) => {
    const sample: StudioSample = { id: `d${i + 1}`, assistant_response: s.assistant_response, expected: s.should_fire ? 'fire' : 'no_fire' };
    if (s.user_request) sample.user_request = s.user_request;
    if (s.evidence && s.evidence.length > 0) sample.evidence = s.evidence.map((text, j) => ({ id: `d${i + 1}-e${j + 1}`, text }));
    return sample;
  });
}
