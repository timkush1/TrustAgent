import { createFileRoute, useRouterState } from '@tanstack/react-router';
import { ConnectCallback } from '@/components/setup/ConnectCallback';

/**
 * Connect OpenRouter returns here with `?code=` (SPEC §7.5; P4-PLAN D38, ON-7e). Full build only
 * (D72). The code is read from the raw query string (the router's JSON search parsing would turn a
 * numeric code into a number), and the page takes it out of the address bar at once.
 */
function ConnectCallbackRoute() {
  const searchStr = useRouterState({ select: (s) => s.location.searchStr });
  const params = new URLSearchParams(searchStr);
  return <ConnectCallback code={params.get('code') ?? undefined} error={params.get('error') ?? undefined} />;
}

export const Route = createFileRoute('/connect/callback')({
  component: ConnectCallbackRoute,
});
