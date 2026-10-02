/**
 * Contract copy with commands in it ("To use your own keys: `make demo-down && make up`"): the
 * commands from contracts/ui/commands.v1.json render as code, the rest as text.
 */
import { splitCommands } from '@/domain/presentation';

export function CommandText({ text }: { text: string }) {
  return (
    <>
      {splitCommands(text).map((part, i) =>
        part.command ? (
          <code key={i} className="rounded-sm bg-surface-sunken px-1 font-mono text-xs text-fg">
            {part.text}
          </code>
        ) : (
          <span key={i}>{part.text}</span>
        ),
      )}
    </>
  );
}
