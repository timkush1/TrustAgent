/**
 * A contract sentence whose figure carries a value tag in the text ("… about {cost} [estimate]"):
 * the tag becomes the EvidenceBadge, so every number on the live surfaces says where it comes from
 * (Rule 10). An OpenRouter settings page it names is a link. Owned by WEB-LIVE (LV-6).
 */
import { Fragment, type ReactNode } from 'react';
import { OpenRouterLinks } from './OpenRouterLinks';

const TAG = /\s*\[(?:measured|estimate|third-party|vendor|simulated)\]/i;

/** `text` with each value tag replaced by `badge`; the text without a tag is returned as it is. */
export function Tagged({ text, badge }: { text: string; badge: ReactNode }) {
  const parts = text.split(TAG);
  return (
    <>
      {parts.map((part, i) => (
        <Fragment key={i}>
          <OpenRouterLinks text={part} />
          {i < parts.length - 1 ? <> {badge}</> : null}
        </Fragment>
      ))}
    </>
  );
}
