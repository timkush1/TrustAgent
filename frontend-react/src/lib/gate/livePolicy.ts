/**
 * What a check in the static site runs against (D104): the routes of config/policies.yaml (the
 * policy `make demo` runs and the recording was built with, parsed at build time by
 * vite/plugins/policy.ts, so no YAML parser ships to the browser) and the built-in evaluator
 * templates. Calibration is `{}` (raw scores): a live check is labelled uncalibrated. The live
 * Playground, the replay's glass box (D109) and the static Studio read their routes from here.
 * Owned by WEB-LIVE (LV-3).
 */
import { policy } from 'virtual:trustagent/policy';
import type { Spec } from '@/lib/decision/spec';
import { BUILTIN_TEMPLATES } from '@/lib/decision/templates';
import { mergeSettings, resolveRoute, specMap } from './route';
import type { PolicyInput, ResolvedRoute } from './types';

/** The build's policy document. */
export const LIVE_POLICY: PolicyInput = policy;

/** The route names of the build's policy, in policy order. */
export function liveRouteNames(): string[] {
  return (LIVE_POLICY.routes ?? []).map((r) => r.name);
}

/** A route of the build's policy, resolved over its defaults (RouteError when it has none). */
export function liveRoute(name: string): ResolvedRoute {
  return resolveRoute(LIVE_POLICY, name);
}

/** The text a block or fallback action delivers on `name` (its `block.message`), or null. */
export function liveBlockMessage(name: string): string | null {
  const route = LIVE_POLICY.routes?.find((r) => r.name === name);
  if (route === undefined) return null;
  const settings = mergeSettings(LIVE_POLICY.defaults ?? {}, route);
  const block = settings.block;
  return typeof block === 'object' && block !== null && typeof (block as { message?: unknown }).message === 'string' ? (block as { message: string }).message : null;
}

let specs: ReadonlyMap<string, Spec> | null = null;

/** Every built-in template, parsed, by ref (runGate's `specs`). */
export function builtinSpecs(): ReadonlyMap<string, Spec> {
  specs ??= specMap(Object.values(BUILTIN_TEMPLATES));
  return specs;
}
