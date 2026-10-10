/**
 * The landing's social and search meta (D114, D115, LD-7): canonical, og:url, og:site_name,
 * twitter:card and theme-color, with absolute URLs from VITE_SITE_URL (the published site; never a
 * request), and og:image only when public/og.png exists at build time (`npm run og` writes it; until
 * then the card is a text summary). The landing's title and og:title/og:description live in
 * landing.html. Only the landing page gets these; the app is not a share target. Owned by
 * WEB-LANDING.
 */
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import type { HtmlTagDescriptor, Plugin } from 'vite';

/** The registry default of VITE_SITE_URL (contracts/config/keys.v1.json; src/config/runtime.ts DEFAULT_SITE_URL). */
export const DEFAULT_SITE_URL = 'https://timkush1.github.io/TrustAgent/';

/** The page background of each theme (src/styles/tokens.css --bg), for the browser chrome. */
export const THEME_COLORS = { light: '#f9fafb', dark: '#0b0c0f' } as const;

/** The size `npm run og` captures the card at (D115). */
export const OG_IMAGE_SIZE = { width: 1280, height: 640 } as const;

const SITE_URL = /^https?:\/\/[^\s/?#]+(\/[^\s?#]*)?$/;

/** VITE_SITE_URL, absolute and ending in "/" (runtime.ts buildSiteUrl): the default when unset or malformed. */
export function siteUrlOf(value: string | undefined): string {
  const v = (value ?? '').trim();
  if (!SITE_URL.test(v)) return DEFAULT_SITE_URL;
  return v.endsWith('/') ? v : `${v}/`;
}

/** The tags the landing gets, for a site URL and whether public/og.png exists. */
export function ogTags(siteUrl: string, hasImage: boolean): HtmlTagDescriptor[] {
  const meta = (attrs: Record<string, string>): HtmlTagDescriptor => ({ tag: 'meta', attrs, injectTo: 'head' });
  const tags: HtmlTagDescriptor[] = [
    { tag: 'link', attrs: { rel: 'canonical', href: siteUrl }, injectTo: 'head' },
    meta({ property: 'og:url', content: siteUrl }),
    meta({ property: 'og:site_name', content: 'TrustAgent' }),
    meta({ name: 'twitter:card', content: hasImage ? 'summary_large_image' : 'summary' }),
    meta({ name: 'theme-color', content: THEME_COLORS.light, media: '(prefers-color-scheme: light)' }),
    meta({ name: 'theme-color', content: THEME_COLORS.dark, media: '(prefers-color-scheme: dark)' }),
  ];
  if (hasImage) {
    tags.push(
      meta({ property: 'og:image', content: new URL('og.png', siteUrl).href }),
      meta({ property: 'og:image:width', content: String(OG_IMAGE_SIZE.width) }),
      meta({ property: 'og:image:height', content: String(OG_IMAGE_SIZE.height) }),
      meta({ property: 'og:image:alt', content: 'TrustAgent flags an answer that says 60 days where the refund policy says 30.' }),
    );
  }
  return tags;
}

export function ogMetaPlugin(): Plugin {
  let siteUrl = DEFAULT_SITE_URL;
  let hasImage = false;
  return {
    name: 'trustagent:og-meta',
    apply: 'build',
    configResolved(config) {
      // config.env holds the VITE_* values from the environment and the .env files.
      siteUrl = siteUrlOf(config.env.VITE_SITE_URL as string | undefined);
      hasImage = typeof config.publicDir === 'string' && config.publicDir !== '' && existsSync(join(config.publicDir, 'og.png'));
    },
    transformIndexHtml(_html, ctx) {
      if (!ctx.filename.endsWith('landing.html')) return undefined;
      return ogTags(siteUrl, hasImage);
    },
  };
}
