import { createFileRoute } from '@tanstack/react-router';
import { PlaygroundPage } from '@/components/playground/PlaygroundPage';
import { StaticPlayground } from '@/components/replay/StaticPlayground';
import { useIsStatic } from '@/components/replay/useReplay';
import { isLiveScenario, type LiveScenarioId } from '@/domain/liveScenarios';

/**
 * The static Playground's query (D99, D105): `?scenario=` preselects a live scenario and is honoured
 * only for an id in LIVE_SCENARIOS; `?paste=1` opens the consent dialog (the callback's "Paste a key
 * instead"). Anything else is dropped. The full build's Playground reads neither.
 */
interface PlaygroundSearch {
  scenario?: LiveScenarioId;
  paste?: true;
}

function validatePlaygroundSearch(search: Record<string, unknown>): PlaygroundSearch {
  // Both keys are always set: the router merges a route's result over the raw query, so a key left
  // out would keep whatever the address bar said.
  return {
    scenario: isLiveScenario(search.scenario) ? search.scenario : undefined,
    paste: search.paste === 1 || search.paste === '1' || search.paste === true ? true : undefined,
  };
}

/** The static build has no gateway: the recorded examples first, then the live check (D99). */
function PlaygroundRoute() {
  const search = Route.useSearch();
  return useIsStatic() ? <StaticPlayground scenario={search.scenario} paste={search.paste === true} /> : <PlaygroundPage />;
}

export const Route = createFileRoute('/app/playground')({
  staticData: { title: 'Playground' },
  validateSearch: validatePlaygroundSearch,
  component: PlaygroundRoute,
});
