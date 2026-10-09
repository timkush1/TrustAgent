/**
 * Policy warnings in plain words. The server sends them as text: `PolicyState.warnings` as
 * "path (line L, col C): message" strings and `ValidateResult.warnings` as field errors (Go
 * `internal/policy/check.go`, `engine.go`). This module names the route and the checks a warning is
 * about and rephrases the warnings the gateway writes today; anything it does not recognise keeps
 * the server's own sentence. The raw text always stays available as the technical detail.
 *
 * The match is on the server's wording, so a reworded Go message falls back to the generic form
 * rather than breaking. (A warning `code` from the server would make this exact: requested from the
 * lead in the WEB-EV checkpoint report.)
 */
import { modeView } from '@/domain/controlPresentation';
import { copy, humanizeId } from '@/domain/presentation';
import type { PolicyMode } from '@/types/api';
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
function noCorroborator(ref: string, list: string, ctx: PolicyWarningContext): string {
  const ids = list.split(', ').filter((x) => x !== '');
  const names = ids.map((x) => studioCorroboratorName(x, ctx.names));
  const fix = ids.some((x) => x.startsWith('det:'))
    ? 'Give the route documents to check against so the instant checks can run.'
    : `Attach ${orList(names)} to the route so it can confirm a block.`;
  return `${evaluatorDisplayName(ref, ctx.names)} blocks only when another signal agrees (${orList(names)}), and none of them can run on this route, so it can flag answers but not block them. ${fix}`;
}

/** Rephrases one known message; null when it is not one the gateway writes today. */
function rephrase(message: string, ctx: PolicyWarningContext): string | null {
  const name = (ref: string) => evaluatorDisplayName(ref, ctx.names);
  let m = /^(\S+) blocks only when one of (.+) agrees, and none of them can run on this route$/.exec(message);
  if (m) return noCorroborator(m[1], m[2], ctx);
  // An older gateway warned for each corroborator a route did not attach, although any one that can
  // fire is enough: say what it is, without claiming the check cannot block.
  m = /^(\S+) is corroborated by (\S+), which this route does not attach$/.exec(message);
  if (m) return `${name(m[2])} is one of the signals that can confirm ${name(m[1])} before it blocks, and this route does not run it.`;
  m = /^(\S+) implies (\S+), which this route does not attach, so their disagreement is never checked$/.exec(message);
  if (m) return `${name(m[1])} is cross-checked against ${name(m[2])} to catch the two disagreeing, but this route does not run ${name(m[2])}, so that cross-check never happens.`;
  m = /^fail: closed only matters in block and fallback modes \(this route is (\w+)\)$/.exec(message);
  if (m) return `Blocking an answer when a check cannot finish (fail closed) only matters on Block and Fallback routes; this route is set to ${modeWord(m[1])}.`;
  m = /^sample_rate applies to observe routes only; this (\w+) route checks every request$/.exec(message);
  if (m) return `The sample rate only applies to Observe routes; this ${modeWord(m[1])} route checks every request.`;
  if (/^the \w+ route trusts user-message context/.test(message)) return copy.downgrades.trusts_user_context;
  m = /^client_rules lets end users rewrite rules on the enforcing route "([^"]+)"$/.exec(message);
  if (m) {
    return `Client rules are on, so each request can bring its own check rules, and ${routeTitle(m[1])} blocks answers: a caller could loosen the rules that decide what it blocks. Turn client rules off, or keep this route from blocking.`;
  }
  return null;
}

/** One warning from `PolicyState.warnings`. */
export function policyWarningView(raw: string, ctx: PolicyWarningContext): PolicyWarningView {
  const head = /^the newest policy version v(\d+) no longer validates \(.*\); running v(\d+) until a valid version is saved$/s.exec(raw);
  if (head) {
    return { route: null, text: `Policy version ${head[1]} no longer passes validation, so version ${head[2]} stays in effect until a valid version is saved.`, detail: raw };
  }
  const prefixed = PREFIX.exec(raw);
  if (!prefixed?.groups) return { route: null, text: capitalize(raw), detail: raw };
  return fieldWarningView({ path: prefixed.groups.path, message: prefixed.groups.message }, ctx, raw);
}

/** One warning from `ValidateResult.warnings` (a field error). */
export function fieldWarningView(warning: { path: string; message: string }, ctx: PolicyWarningContext, raw?: string): PolicyWarningView {
  const route = routeOf(warning.path, ctx);
  const detail = raw ?? (warning.path ? `${warning.path}: ${warning.message}` : warning.message);
  const text = rephrase(warning.message, ctx) ?? capitalize(warning.message);
  return { route, text: route ? `${routeTitle(route)}: ${text}` : text, detail };
}

/** The diverged-file warning repeats the diverged-file banner, which says it with its actions. */
export function isDivergedWarning(raw: string): boolean {
  return /changed after the newest dashboard edit and is not applied/.test(raw);
}
