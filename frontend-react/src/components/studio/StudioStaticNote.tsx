/**
 * The static build's note on Studio (contracts/ui copy.studio.static_soon, D72): run
 * TrustAgent yourself to use it, with the command shown as code.
 */
import { copy, fillTemplate, splitCommands } from '@/domain/presentation';

export function StudioStaticNote() {
  const text = fillTemplate(copy.studio.static_soon, {});
  return (
    <p>
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
  );
}
