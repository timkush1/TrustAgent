import { createFileRoute } from '@tanstack/react-router';
import { lazy, Suspense, useEffect } from 'react';
import { NotFound } from '@/components/common/RouteStates';

/**
 * `/og` (D115): the social card page, outside the app shell and the nav. Only a build with
 * `VITE_LAUNCH_ROUTES=1` (`npm run og`) renders it; any other build answers with the not-found page,
 * and no build copies it into the published site (vite.site.ts STATIC_APP_PAGES).
 */
const LAUNCH_ROUTES = ['1', 'true'].includes(String(import.meta.env.VITE_LAUNCH_ROUTES ?? '').toLowerCase());
const OgCard = lazy(() => import('@/components/launch/OgCard'));

function OgRoute() {
  useEffect(() => {
    if (!LAUNCH_ROUTES) return undefined;
    const meta = document.createElement('meta');
    meta.name = 'robots';
    meta.content = 'noindex';
    document.head.append(meta);
    return () => meta.remove();
  }, []);
  if (!LAUNCH_ROUTES) return <NotFound />;
  return (
    <Suspense fallback={null}>
      <OgCard />
    </Suspense>
  );
}

export const Route = createFileRoute('/og')({
  component: OgRoute,
});
