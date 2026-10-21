/**
 * A sentence that names an OpenRouter settings page ("Set one at openrouter.ai/settings/keys"),
 * with each page name a real link that opens in a new tab (D102), so the reader can act on the
 * advice in one click. The text stays the contract's own words. Owned by WEB-LIVE (LV-6).
 */
import { Fragment } from 'react';
import { OPENROUTER_PAGES } from '@/lib/openrouter/endpoints';
import { ExternalLink } from './ExternalLink';

const PAGE = /(openrouter\.ai\/settings\/(?:keys|credits|privacy))/;

function pageHref(name: string): string {
  const page = name.slice(name.lastIndexOf('/') + 1) as keyof typeof OPENROUTER_PAGES;
  return OPENROUTER_PAGES[page];
}

export function OpenRouterLinks({ text }: { text: string }) {
  const parts = text.split(PAGE);
  if (parts.length === 1) return <>{text}</>;
  return (
    <>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <ExternalLink key={i} href={pageHref(part)} className="font-normal">
            {part}
          </ExternalLink>
        ) : (
          <Fragment key={i}>{part}</Fragment>
        ),
      )}
    </>
  );
}
