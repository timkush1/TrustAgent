/** The note above a Studio run's results when the mock fast check answered it. */
import { FlaskConical } from 'lucide-react';

export function SimulatedRunNote({ children }: { children: string }) {
  return (
    <p className="flex items-start gap-1.5 rounded-md border border-border bg-surface-sunken px-3 py-2 text-sm text-fg">
      <FlaskConical aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
      <span className="min-w-0">{children}</span>
    </p>
  );
}
