/**
 * What Studio does only on your own stack (D107): watching a check in shadow, testing it on past
 * traffic, making it active and calibrating it for blocking all need a gateway that stores traffic
 * and labels. One card says so, with the command as code, instead of seven disabled buttons.
 */
import { Server } from 'lucide-react';
import { copy, fillTemplate, splitCommands } from '@/domain/presentation';

export function OwnStackCard() {
  const text = fillTemplate(copy.studio.static_needs_stack, {});
  return (
    <section aria-labelledby="studio-own-stack" className="space-y-2 rounded-lg border border-dashed border-border-strong bg-surface p-4 sm:p-5">
      <h2 id="studio-own-stack" className="flex items-center gap-2 text-base font-semibold text-fg">
        <Server aria-hidden="true" className="size-4 text-fg-muted" />
        On your own stack
      </h2>
      <p className="text-sm text-fg-muted">
        {splitCommands(text).map((part, i) =>
          part.command ? (
            <code key={i} className="rounded-sm bg-surface-sunken px-1 font-mono text-2xs text-fg">
              {part.text}
            </code>
          ) : (
            <span key={i}>{part.text}</span>
          ),
        )}
      </p>
    </section>
  );
}
