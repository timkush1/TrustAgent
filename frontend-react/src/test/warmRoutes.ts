/**
 * Loads the code-split route components a test file renders before its first test.
 *
 * The router plugin splits each route's component into its own module (autoCodeSplitting, as in
 * the production build), so the first navigation in a test file imports that page's whole module
 * graph, the app shell included, before anything renders. Under the full suite on two workers that
 * cold import can take longer than a test's findBy wait: the first test in a file then timed out
 * on an empty <div />, and the next one too while the import was still running. Loading the
 * components in a beforeAll, with a hook timeout sized for a busy machine, moves that one-off cost
 * out of the tests' waits. The tests still render through the router's lazy components.
 *
 * Call it at the top of a test file with the paths the file renders (`warmRoutes('/app/policies')`).
 */
import { createRouter, type AnyRoute } from '@tanstack/react-router';
import { beforeAll } from 'vitest';
import type { AppServices } from '@/app/services';
import { routeTree } from '@/routeTree.gen';

/**
 * A router over the app's route tree that only matches paths and loads route chunks: it never
 * renders or runs a loader, so it needs no services.
 */
function chunkRouter() {
  return createRouter({ routeTree, context: undefined as unknown as AppServices });
}

/** The routes on the way to each path (root, layouts and the page), without duplicates. */
export function routesFor(paths: readonly string[]): AnyRoute[] {
  const router = chunkRouter();
  return [...new Set(paths.flatMap((path) => router.getMatchedRoutes(path)[0]))];
}

/** Registers a beforeAll that loads the split components of the routes behind `paths`. */
export function warmRoutes(...paths: string[]): void {
  beforeAll(async () => {
    const router = chunkRouter();
    await Promise.all(routesFor(paths).map((route) => router.loadRouteChunk(route)));
  }, 120_000);
}
