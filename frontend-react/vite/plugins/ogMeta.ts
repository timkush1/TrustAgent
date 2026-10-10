/**
 * The landing's social meta (D115, LD-6): og:*, twitter:card, canonical and theme-color from
 * VITE_SITE_URL, and og:image only when public/og.png exists at build time (in P5 it does not).
 * Owned by WEB-LANDING.
 *
 * Step 0b stub: registered, and adds nothing.
 */
import type { Plugin } from 'vite';

export function ogMetaPlugin(): Plugin {
  return {
    name: 'trustagent:og-meta',
    apply: 'build',
    transformIndexHtml() {
      return undefined;
    },
  };
}
