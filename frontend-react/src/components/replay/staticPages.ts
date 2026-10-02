/**
 * The pages the static build hides (SPEC §3.3 "Static build"): they need a running gateway.
 * StaticRouteGuard shows a notice in their place.
 */
export interface HiddenPage {
  prefix: string;
  name: string;
  /** What the page does on a running stack. */
  does: string;
}

/**
 * Checked first: /app/evaluators/studio ("Coming soon" in the static build) and
 * /app/evaluators/benchmarks (the committed runs, bundled at build time; D72) stay visible, the rest
 * of /app/evaluators does not.
 */
const VISIBLE = ['/app/evaluators/studio', '/app/evaluators/benchmarks'];
const HIDDEN: readonly HiddenPage[] = [
  { prefix: '/app/policies', name: 'Policies', does: 'decide what each route does with a risky answer: observe, flag, block or fall back' },
  { prefix: '/app/settings', name: 'Settings', does: 'connect OpenRouter, pick the fast-check and deep-audit models, and set budgets and keys' },
  { prefix: '/app/knowledge', name: 'Knowledge', does: 'upload documents to the optional knowledge base and review what it holds' },
  { prefix: '/app/evaluators', name: 'Evaluators', does: 'see every check, the routes it runs on, and how well it agrees with your labels' },
  { prefix: '/app/setup', name: 'Setup', does: 'secure the dashboard and connect your OpenRouter key' },
];

/** The hidden page a path belongs to, or null (with or without the site's base path, /TrustAgent/). */
export function hiddenStaticPage(pathname: string): HiddenPage | null {
  const at = pathname.search(/\/app(\/|$)/);
  const path = at >= 0 ? pathname.slice(at) : pathname;
  const within = (prefix: string) => path === prefix || path.startsWith(`${prefix}/`);
  if (VISIBLE.some(within)) return null;
  return HIDDEN.find((p) => within(p.prefix)) ?? null;
}
