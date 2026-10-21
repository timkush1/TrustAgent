/**
 * Policy warnings in plain words (CO-5). Every warning the gateway writes today carries a `code`
 * from the closed list of Go `internal/policy` WarningCodes and the values its words need
 * (`PolicyState.warning_details`, `ValidateResult.warnings`), so this module words it by code with
 * contracts/ui copy.policies.warnings.<code> and never reads the message. A warning without a code
 * (an older gateway) is matched on its text and worded with the same templates; anything it does
 * not recognise keeps the server's own sentence. The raw text always stays available as the
 * technical detail.
 */
import { modeView } from '@/domain/controlPresentation';
import { copy, fillTemplate, humanizeId } from '@/domain/presentation';
import type { FieldError, PolicyMode } from '@/types/api';
import { studioCorroboratorName } from '@/components/studio/studioWords';
import { DEFAULT_ROUTE } from './policyDraft';
import { evaluatorDisplayName, routeTitle } from './routeWords';

export interface PolicyWarningContext {
  /** The policy's routes in document order (`routes[i]` is the i-th; the default route excluded). */
  routes: readonly string[];
  /** Evaluator display names by id. */
  names: ReadonlyMap<string, string>;
}

export interface PolicyWarningView {
  /** The route it is about, when the warning names one (`default` for `defaults.*`). */
  route: string | null;
  /** The warning in plain words. */
  text: string;
  /** What the server sent, for the technical-detail disclosure. */
  detail: string;
}

/** A warning with its code and details (`PolicyState.warning_details[i]`, `ValidateResult.warnings[i]`). */
export type CodedWarning = Pick<FieldError, 'path' | 'message' | 'code' | 'details'>;

const WORDS = copy.policies.warnings;

const PREFIX = /^(?<path>(?:routes\[\d+\]|defaults|overrides|version|routes)(?:\.[a-z_]+|\[\d+\])*)(?: \(line \d+, col \d+\))?: (?<message>.+)$/s;

function routeOf(path: string, ctx: PolicyWarningContext): string | null {
  const m = /^routes\[(\d+)\]/.exec(path);
  if (m) return ctx.routes[Number(m[1])] ?? null;
  if (path.startsWith('defaults')) return DEFAULT_ROUTE;
  return null;
}

function modeWord(raw: string): string {
  const known = ['observe', 'flag', 'block', 'fallback'].includes(raw);
  return known ? modeView(raw as PolicyMode).label : humanizeId(raw);
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** "A, B or C". */
function orList(items: readonly string[]): string {
  return items.length <= 1 ? (items[0] ?? '') : `${items.slice(0, -1).join(', ')} or ${items[items.length - 1]}`;
}

/**
 * A blocking route where none of a check's corroborators can fire (D98): the check flags but never
 * blocks. Instant checks need documents (the number and date checks also run on a check's rules),
 * so documents are the fix when one is listed; otherwise attaching one of the listed checks is.
 */
function noCorroborator(ref: string, ids: readonly string[], ctx: PolicyWarningContext): string {
  const names = orList(ids.map((x) => studioCorroboratorName(x, ctx.names)));
  const fix = ids.some((x) => x.startsWith('det:')) ? WORDS.no_corroborator_fix_documents : fillTemplate(WORDS.no_corroborator_fix_attach, { corroborators: names });
  return `${fillTemplate(WORDS.no_corroborator, { check: evaluatorDisplayName(ref, ctx.names), corroborators: names })} ${fix}`;
}

function impliesUnattached(ref: string, implied: string, ctx: PolicyWarningContext): string {
  return fillTemplate(WORDS.implies_unattached, { check: evaluatorDisplayName(ref, ctx.names), implied: evaluatorDisplayName(implied, ctx.names) });
}

const text = (v: unknown): string | null => (typeof v === 'string' && v !== '' ? v : null);
const count = (v: unknown): string | null => (typeof v === 'number' && Number.isFinite(v) ? String(v) : text(v));

/** Words for a coded warning; null for a code without words, or details its words need missing. */
function byCode(w: CodedWarning, ctx: PolicyWarningContext): string | null {
  const d = w.details ?? {};
  switch (w.code) {
    case 'no_corroborator': {
      const ref = text(d.ref);
      const ids = Array.isArray(d.corroborators) ? d.corroborators.filter((x): x is string => typeof x === 'string' && x !== '') : [];
      return ref && ids.length > 0 ? noCorroborator(ref, ids, ctx) : null;
    }
    case 'implies_unattached': {
      const ref = text(d.ref);
      const implied = text(d.implies);
      return ref && implied ? impliesUnattached(ref, implied, ctx) : null;
    }
    case 'fail_closed_not_enforcing':
    case 'sample_rate_not_observe': {
      const mode = text(d.mode);
      return mode ? fillTemplate(WORDS[w.code], { mode: modeWord(mode) }) : null;
    }
    case 'trusts_user_context':
      return WORDS.trusts_user_context;
    case 'client_rules_enforcing': {
      const route = text(d.route);
      return route ? fillTemplate(WORDS.client_rules_enforcing, { route: routeTitle(route) }) : null;
    }
    case 'newest_version_invalid': {
      const bad = count(d.bad_version);
      const active = count(d.active_version);
      return bad && active ? fillTemplate(WORDS.newest_version_invalid, { bad_version: bad, active_version: active }) : null;
    }
    case 'file_diverged': {
      const path = text(d.path);
      return path ? fillTemplate(WORDS.file_diverged, { path }) : null;
    }
    default:
      return null;
  }
}

/** Rephrases one known message of a gateway without codes; null when it is not one it writes. */
function rephrase(message: string, ctx: PolicyWarningContext): string | null {
  const name = (ref: string) => evaluatorDisplayName(ref, ctx.names);
  let m = /^(\S+) blocks only when one of (.+) agrees, and none of them can run on this route$/.exec(message);
  if (m) return noCorroborator(m[1], m[2].split(', ').filter((x) => x !== ''), ctx);
  // An older gateway warned for each corroborator a route did not attach, although any one that can
  // fire is enough: say what it is, without claiming the check cannot block.
  m = /^(\S+) is corroborated by (\S+), which this route does not attach$/.exec(message);
  if (m) return `${name(m[2])} is one of the signals that can confirm ${name(m[1])} before it blocks, and this route does not run it.`;
  m = /^(\S+) implies (\S+), which this route does not attach, so their disagreement is never checked$/.exec(message);
  if (m) return impliesUnattached(m[1], m[2], ctx);
  m = /^fail: closed only matters in block and fallback modes \(this route is (\w+)\)/.exec(message);
  if (m) return fillTemplate(WORDS.fail_closed_not_enforcing, { mode: modeWord(m[1]) });
  m = /^sample_rate applies to observe routes only; this (\w+) route checks every request$/.exec(message);
  if (m) return fillTemplate(WORDS.sample_rate_not_observe, { mode: modeWord(m[1]) });
  if (/^the \w+ route trusts user-message context/.test(message)) return WORDS.trusts_user_context;
  m = /^client_rules lets end users rewrite rules on the enforcing route "([^"]+)"$/.exec(message);
  if (m) return fillTemplate(WORDS.client_rules_enforcing, { route: routeTitle(m[1]) });
  return null;
}

/** The route a coded warning names (`default` for the defaults), else the route of its path. */
function routeOfCoded(w: CodedWarning, ctx: PolicyWarningContext): string | null {
  const route = text(w.details?.route);
  if (route) return route === DEFAULT_ROUTE ? DEFAULT_ROUTE : route;
  return w.path ? routeOf(w.path, ctx) : null;
}

/**
 * One warning from `PolicyState.warnings`, with its `warning_details` entry when the gateway sent
 * one: worded by code, else by its text.
 */
export function policyWarningView(raw: string, ctx: PolicyWarningContext, coded?: CodedWarning): PolicyWarningView {
  if (coded?.code) {
    const words = byCode(coded, ctx);
    if (words !== null) return withRoute(routeOfCoded(coded, ctx), words, raw);
  }
  const head = /^the newest policy version v(\d+) no longer validates \(.*\); running v(\d+) until a valid version is saved$/s.exec(raw);
  if (head) {
    return { route: null, text: fillTemplate(WORDS.newest_version_invalid, { bad_version: head[1], active_version: head[2] }), detail: raw };
  }
  const prefixed = PREFIX.exec(raw);
  if (!prefixed?.groups) return { route: null, text: capitalize(raw), detail: raw };
  return fieldWarningView({ path: prefixed.groups.path, message: prefixed.groups.message }, ctx, raw);
}

function withRoute(route: string | null, words: string, detail: string): PolicyWarningView {
  return { route, text: route ? `${routeTitle(route)}: ${words}` : words, detail };
}

/** One warning from `ValidateResult.warnings` (a field error, coded by a current gateway). */
export function fieldWarningView(warning: CodedWarning, ctx: PolicyWarningContext, raw?: string): PolicyWarningView {
  const detail = raw ?? (warning.path ? `${warning.path}: ${warning.message}` : warning.message);
  const coded = warning.code ? byCode(warning, ctx) : null;
  if (coded !== null) return withRoute(routeOfCoded(warning, ctx), coded, detail);
  const route = warning.path ? routeOf(warning.path, ctx) : null;
  return withRoute(route, rephrase(warning.message, ctx) ?? capitalize(warning.message), detail);
}

/** The diverged-file warning repeats the diverged-file banner, which says it with its actions. */
export function isDivergedWarning(raw: string, coded?: CodedWarning): boolean {
  return coded?.code === 'file_diverged' || /changed after the newest dashboard edit and is not applied/.test(raw);
}
