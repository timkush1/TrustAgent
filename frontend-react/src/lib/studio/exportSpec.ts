/**
 * The static Studio's "Save" (D107): the check as an evaluator.v1 file the viewer copies or
 * downloads, to load into their own stack. It is the version a save would create there from this
 * library (latest + 1: version 1 for a new id), validated as the gateway validates a save (the
 * schema, then the hard rules), in the JSON tab's layout.
 *
 * Nothing secret may leave this way: the tab's OpenRouter key, or anything shaped like an
 * OpenRouter key, anywhere in the text refuses the export (a description or a param default could
 * carry a pasted key; the file is meant to be shared).
 */
import { checkSpecDocument, isSavable } from '@/lib/evaluator/checkSpec';
import type { EvaluatorSpec } from '@/types/api';
import { specJson } from './specFields';

/** An OpenRouter key's shape (D102: the key store's own check). */
const OPENROUTER_KEY = /sk-or-[A-Za-z0-9_-]{16,}/;
/** Shorter values than this are never treated as a secret to look for. */
const MIN_SECRET = 8;

export type SpecExport =
  | { ok: true; doc: EvaluatorSpec; json: string; fileName: string }
  | { ok: false; reason: 'invalid' | 'secret' };

/**
 * The export of `spec` given the newest version of its id the library has (0: none) and the
 * secrets the text must not contain (the tab's key).
 */
export function exportSpec(spec: EvaluatorSpec, latest: number, opts: { registry?: Iterable<string>; secrets?: readonly (string | null | undefined)[] } = {}): SpecExport {
  const doc = { ...spec, version: latest + 1 } as EvaluatorSpec;
  if (!isSavable(checkSpecDocument(doc, opts.registry ? { registry: opts.registry } : {}))) return { ok: false, reason: 'invalid' };
  const json = specJson(doc);
  const secrets = (opts.secrets ?? []).filter((s): s is string => typeof s === 'string' && s.length >= MIN_SECRET);
  if (OPENROUTER_KEY.test(json) || secrets.some((s) => json.includes(s))) return { ok: false, reason: 'secret' };
  return { ok: true, doc, json, fileName: `${doc.id}.v${doc.version}.json` };
}
