/**
 * Where the landing's links go, decided once per build: the static GitHub Pages site has real
 * Studio and Playground pages (vite.site.ts STATIC_APP_PAGES) and runs live checks from the browser
 * (D99); the dashboard image's build links into its own app.
 */
import { LIVE_CHECKS } from '@/domain/liveScenarios';
import { APP_MODE, BASE } from './content';

/** The Studio: a real page of the static site (vite.site.ts STATIC_APP_PAGES), the dashboard's route otherwise. */
export const STUDIO_HREF = APP_MODE === 'static' ? `${BASE}app/evaluators/studio/` : `${BASE}app/evaluators/studio`;

/** Live checks run from this build: the static site, where the browser can call OpenRouter (D99). */
export const LIVE_ON_THIS_SITE = LIVE_CHECKS && APP_MODE === 'static';
