/**
 * Studio's edits to the policy (SPEC §7.3 step 6): "Save and watch on route X" attaches a saved
 * evaluator version as a shadow evaluator, "Make active" moves it to active, and a new version of
 * an evaluator the route already runs replaces the old ref. The result is a policy draft the viewer
 * reviews and saves through the Policies page's review dialog (diff, validation, Preview impact,
 * If-Match); nothing here saves.
 *
 * The edits go through components/policies/policyDraft.ts (imported read-only), which keeps the
 * YAML's comments and layout so the reviewed diff shows only this change. A route's `evaluators`
 * list replaces the defaults' list (SPEC §2.7), so editing a route that inherits it writes the
 * route's own copy of the inherited list with the change; the default route edits `defaults`.
 */
import { isMap, isSeq, parseDocument, type Document, type YAMLMap, type YAMLSeq } from 'yaml';
import { DEFAULT_ROUTE, DraftError, keepOriginalFormatting } from '@/components/policies/policyDraft';

export type EvaluatorStage = 'active' | 'shadow';

/** One entry of a route's `evaluators` list (policy.v1 RouteEvaluatorRef), as written. */
export interface RouteEvaluatorEntry {
  ref: string;
  stage?: EvaluatorStage;
  [field: string]: unknown;
}

export interface RouteEvaluators {
  entries: RouteEvaluatorEntry[];
  /** Whether the route writes its own list (false: it inherits the defaults' list). */
  own: boolean;
}

/** The evaluator id of a ref ("blames_customer@2" → "blames_customer"). */
export function refId(ref: string): string {
  const at = ref.lastIndexOf('@');
  return at === -1 ? ref : ref.slice(0, at);
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

function entriesOf(value: unknown): RouteEvaluatorEntry[] | null {
  if (!Array.isArray(value)) return null;
  return value.filter((e): e is RouteEvaluatorEntry => isObj(e) && typeof e.ref === 'string');
}

const STRINGIFY = { lineWidth: 0, flowCollectionPadding: false } as const;

function parse(yaml: string): Document {
  const doc = parseDocument(yaml);
  if (doc.errors.length > 0) throw new DraftError(`The policy YAML does not parse: ${doc.errors[0].message}`);
  return doc;
}

/** Where a route's settings live: `defaults` for the default route, else its entry in `routes`. */
function routeBase(doc: Document, route: string): (string | number)[] {
  if (route === DEFAULT_ROUTE) return ['defaults'];
  const routes = doc.get('routes');
  const index = isSeq(routes) ? routes.items.findIndex((item) => isMap(item) && item.get('name') === route) : -1;
  if (index === -1) throw new DraftError(`The policy has no route named ${route}.`);
  return ['routes', index];
}

/** The evaluators a route runs in a policy draft: its own list, else the defaults' list. */
export function routeEvaluators(yaml: string, route: string): RouteEvaluators {
  const doc = parse(yaml);
  const base = routeBase(doc, route);
  const own = entriesOf(valueAt(doc, [...base, 'evaluators']));
  if (own) return { entries: own, own: true };
  return { entries: entriesOf(valueAt(doc, ['defaults', 'evaluators'])) ?? [], own: false };
}

/** The plain value at `path` (a collection as JS arrays and objects). */
function valueAt(doc: Document, path: (string | number)[]): unknown {
  const node: unknown = doc.getIn(path, true);
  if (isMap(node) || isSeq(node)) return node.toJSON();
  return node;
}

/** The entry for an evaluator id on a route, if the route runs it (any version). */
export function findEvaluator(yaml: string, route: string, id: string): RouteEvaluatorEntry | undefined {
  return routeEvaluators(yaml, route).entries.find((e) => refId(e.ref) === id);
}

/**
 * Runs `change` on the route's own `evaluators` sequence (created from the inherited list when the
 * route has none), and keeps the original text of every line the change did not touch.
 */
function editList(yaml: string, route: string, change: (seq: YAMLSeq, doc: Document, entries: RouteEvaluatorEntry[]) => void): string {
  const doc = parse(yaml);
  const base = routeBase(doc, route);
  const { entries, own } = routeEvaluators(yaml, route);
  let seq = doc.getIn([...base, 'evaluators'], true);
  if (!own || !isSeq(seq)) {
    doc.setIn([...base, 'evaluators'], doc.createNode(entries));
    seq = doc.getIn([...base, 'evaluators'], true);
  }
  change(seq as YAMLSeq, doc, entries);
  return keepOriginalFormatting(yaml, doc.toString(STRINGIFY));
}

function entryAt(seq: YAMLSeq, entries: readonly RouteEvaluatorEntry[], id: string, route: string): YAMLMap {
  const at = entries.findIndex((e) => refId(e.ref) === id);
  // Entries skip malformed items, so find the node by its ref rather than by position.
  const node = at === -1 ? undefined : seq.items.find((item) => isMap(item) && item.get('ref') === entries[at].ref);
  if (!node) throw new DraftError(`${route} does not run ${id}.`);
  return node as YAMLMap;
}

/**
 * Attaches `ref` to `route` at `stage` (Studio's default is shadow: watched, never enforced). A
 * route runs an evaluator id once, so a route that already runs another version of it is refused:
 * use `replaceEvaluatorRef` for that.
 */
export function attachEvaluator(yaml: string, route: string, ref: string, stage: EvaluatorStage = 'shadow'): string {
  const { entries } = routeEvaluators(yaml, route);
  const existing = entries.find((e) => refId(e.ref) === refId(ref));
  if (existing) {
    throw new DraftError(
      existing.ref === ref ? `${route} already runs ${ref}.` : `${route} already runs ${existing.ref}; replace it with ${ref} instead.`,
    );
  }
  if (entries.length >= 16) throw new DraftError(`${route} already runs 16 evaluators, the most a route can have.`);
  return editList(yaml, route, (seq, doc) => seq.add(doc.createNode({ ref, stage })));
}

/** Sets the stage of the evaluator `id` on `route` ("Make active", or back to watching). */
export function setEvaluatorStage(yaml: string, route: string, id: string, stage: EvaluatorStage): string {
  return editList(yaml, route, (seq, _doc, entries) => entryAt(seq, entries, id, route).set('stage', stage));
}

/**
 * Points the route's entry for `toRef`'s evaluator id at `toRef` (a new version), keeping its
 * stage, parameters and thresholds.
 */
export function replaceEvaluatorRef(yaml: string, route: string, toRef: string): string {
  const current = findEvaluator(yaml, route, refId(toRef));
  if (!current) throw new DraftError(`${route} does not run ${refId(toRef)}.`);
  if (current.ref === toRef) throw new DraftError(`${route} already runs ${toRef}.`);
  return editList(yaml, route, (seq, _doc, entries) => entryAt(seq, entries, refId(toRef), route).set('ref', toRef));
}
