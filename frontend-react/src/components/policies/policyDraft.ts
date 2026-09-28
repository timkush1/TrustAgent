/**
 * Edits to a policy draft (SPEC §2.7 policy/v1), made on the YAML text so comments and layout
 * survive: the one-click fixes ("Buffer streams", "Allow uncalibrated blocking"), the per-route
 * mode picker and the presets all go through here. Every edit re-serializes with the `yaml`
 * Document API and then keeps the original text of every line the edit did not touch, so the diff
 * the viewer reviews shows only their change.
 */
import { diffArrays } from 'diff';
import { isMap, isSeq, parseDocument, type Document } from 'yaml';
import presetsJson from '@contracts/policies/presets.v1.json';
import type { EvidenceSource, PolicyMode, RouteSettings } from '@/types/api';

/** The route that applies when no other route matches; edits to it go to `defaults`. */
export const DEFAULT_ROUTE = 'default';

/** Evidence sources when neither the route nor the defaults list them (SPEC §2.7). */
export const DEFAULT_EVIDENCE_SOURCES: readonly EvidenceSource[] = ['request_system', 'request_context', 'tool_results', 'extension'];

export interface PolicyPreset {
  id: string;
  name: string;
  description: string;
  ui_note?: string;
  settings: RouteSettings;
}

export const PRESETS: readonly PolicyPreset[] = (presetsJson as { presets: PolicyPreset[] }).presets;

export class DraftError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'DraftError';
  }
}

const STRINGIFY = { lineWidth: 0, flowCollectionPadding: false } as const;

function parse(yaml: string): Document {
  const doc = parseDocument(yaml, { keepSourceTokens: false });
  if (doc.errors.length > 0) throw new DraftError(`The policy YAML does not parse: ${doc.errors[0].message}`);
  return doc;
}

function routePath(doc: Document, route: string): (string | number)[] {
  if (route === DEFAULT_ROUTE) return ['defaults'];
  const routes = doc.get('routes');
  if (!isSeq(routes)) throw new DraftError(`The policy has no routes, so route ${route} cannot be changed.`);
  const index = routes.items.findIndex((item) => isMap(item) && item.get('name') === route);
  if (index === -1) throw new DraftError(`The policy has no route named ${route}.`);
  return ['routes', index];
}

const norm = (line: string) => line.replace(/\s+/g, ' ').trim();

/**
 * `edited`, but with every line that only changed in whitespace (the serializer's own spacing)
 * taken from `original`.
 */
export function keepOriginalFormatting(original: string, edited: string): string {
  const oldLines = original.split(/(?<=\n)/);
  const newLines = edited.split(/(?<=\n)/);
  let i = 0;
  let j = 0;
  let out = '';
  for (const part of diffArrays(oldLines.map(norm), newLines.map(norm))) {
    const n = part.count;
    if (part.added) {
      out += newLines.slice(j, j + n).join('');
      j += n;
    } else if (part.removed) {
      i += n;
    } else {
      out += oldLines.slice(i, i + n).join('');
      i += n;
      j += n;
    }
  }
  return out;
}

function edit(yaml: string, change: (doc: Document) => void): string {
  const doc = parse(yaml);
  change(doc);
  return keepOriginalFormatting(yaml, doc.toString(STRINGIFY));
}

/** Sets one field of a route (or of `defaults` for the default route). */
export function setRouteField(yaml: string, route: string, path: readonly string[], value: unknown): string {
  return edit(yaml, (doc) => doc.setIn([...routePath(doc, route), ...path], value));
}

export function setRouteMode(yaml: string, route: string, mode: PolicyMode): string {
  return setRouteField(yaml, route, ['mode'], mode);
}

/** "Buffer streams": block and fallback need the whole answer before it is released. */
export function bufferStreams(yaml: string, route: string): string {
  return setRouteField(yaml, route, ['streaming'], 'buffer');
}

/** "Allow uncalibrated blocking" (behind a typed confirmation in the UI). */
export function allowUncalibratedBlocking(yaml: string, route: string): string {
  return setRouteField(yaml, route, ['require_calibration'], false);
}

/** The evidence sources in effect for a route: its own list, else the defaults', else the built-in list. */
export function effectiveEvidenceSources(yaml: string, route: string): EvidenceSource[] {
  const doc = parse(yaml);
  const own = route === DEFAULT_ROUTE ? undefined : doc.getIn([...routePath(doc, route), 'evidence', 'sources']);
  const fromDefaults = doc.getIn(['defaults', 'evidence', 'sources']);
  const list = own ?? fromDefaults;
  if (isSeq(list)) return list.items.map((n) => String((n as { value?: unknown }).value ?? n)) as EvidenceSource[];
  return [...DEFAULT_EVIDENCE_SOURCES];
}

/**
 * Stops treating text pasted into user messages as documents (the `trusts_user_context` fix): the
 * route's evidence sources without `request_context`. Arrays replace, so the whole list is set.
 */
export function stopTrustingUserContext(yaml: string, route: string): string {
  const sources = effectiveEvidenceSources(yaml, route).filter((s) => s !== 'request_context');
  return setRouteField(yaml, route, ['evidence', 'sources'], sources);
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * Sets `value` at `path` the way routes merge over defaults (SPEC §2.7): a nested object merges
 * key by key into the map already there, arrays and scalars replace.
 */
function mergeIn(doc: Document, path: (string | number)[], value: unknown): void {
  if (isPlainObject(value) && isMap(doc.getIn(path, true))) {
    for (const [key, inner] of Object.entries(value)) mergeIn(doc, [...path, key], inner);
    return;
  }
  doc.setIn(path, structuredClone(value));
}

/**
 * Applies a preset's settings over a route like the policy merge: nested objects (evidence,
 * detectors, fallback, …) merge key by key, so a route's own `evidence.tag_patterns` survives a
 * preset that sets `evidence.sources`; arrays and scalars replace.
 */
export function applyPreset(yaml: string, route: string, preset: Pick<PolicyPreset, 'settings'>): string {
  return edit(yaml, (doc) => {
    const base = routePath(doc, route);
    for (const [key, value] of Object.entries(preset.settings)) mergeIn(doc, [...base, key], value);
  });
}

/** Whether two policy texts differ in anything but whitespace. */
export function yamlDiffers(a: string, b: string): boolean {
  return norm(a) !== norm(b);
}

/** The route names a draft defines, in order, with the default route first. */
export function draftRouteNames(yaml: string): string[] {
  try {
    const doc = parse(yaml);
    const routes = doc.get('routes');
    const names = isSeq(routes) ? routes.items.map((r) => (isMap(r) ? String(r.get('name') ?? '') : '')).filter(Boolean) : [];
    return [DEFAULT_ROUTE, ...names];
  } catch {
    return [DEFAULT_ROUTE];
  }
}
