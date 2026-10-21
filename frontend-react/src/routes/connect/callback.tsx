import { createFileRoute, useRouterState } from '@tanstack/react-router';
import { lazy, Suspense } from 'react';
import { ConnectCallback } from '@/components/setup/ConnectCallback';
import { useIsStatic } from '@/components/replay/useReplay';

/** The static site's own callback (D105): loaded only by the static build. */
const StaticConnectCallback = lazy(() => import('@/components/setup/StaticConnectCallback'));

/**
 * Connect OpenRouter returns here with `?code=` (SPEC §7.5; D38, ON-7e). The full build finishes
 * the gateway's Connect flow; the static build runs the browser PKCE exchange (D105). The code is
 * read from the raw query string (the router's JSON search parsing would turn a numeric code into a
 * number), and the page takes it out of the address bar at once.
 */
function ConnectCallbackRoute() {
  const searchStr = useRouterState({ select: (s) => s.location.searchStr });
  const isStatic = useIsStatic();
  if (isStatic) {
    return (
      <Suspense fallback={null}>
        <StaticConnectCallback />
      </Suspense>
    );
  }
  const params = new URLSearchParams(searchStr);
  return <ConnectCallback code={params.get('code') ?? undefined} error={params.get('error') ?? undefined} />;
}

export const Route = createFileRoute('/connect/callback')({
  component: ConnectCallbackRoute,
});
