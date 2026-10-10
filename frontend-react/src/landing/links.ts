/**
 * Where the landing's links go, decided once per build: the static GitHub Pages site has real
 * Studio and Playground pages (vite.site.ts STATIC_APP_PAGES) and runs live checks from the browser
 * (D99); the dashboard image's build links into its own app.
 */
import { LIVE_CHECKS } from '@/domain/liveScenarios';
import { APP_MODE, BASE } from './content';

/**
 * The words of a link to DEMO_HREF: the static site replays the demo's tape there; the dashboard
 * image's build opens its own dashboard, which shows that stack's traffic, not a replay.
 */
export function demoLinkText(mode: typeof APP_MODE = APP_MODE): string {
  return mode === 'static' ? 'Open the replay' : 'Open the dashboard';
}

/** The Studio: a real page of the static site (vite.site.ts STATIC_APP_PAGES), the dashboard's route otherwise. */
export const STUDIO_HREF = APP_MODE === 'static' ? `${BASE}app/evaluators/studio/` : `${BASE}app/evaluators/studio`;

/** Live checks run from this build: the static site, where the browser can call OpenRouter (D99). */
export const LIVE_ON_THIS_SITE = LIVE_CHECKS && APP_MODE === 'static';
