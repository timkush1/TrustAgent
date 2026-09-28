/**
 * An honest page for a navigation item that is not built yet: what it will answer, what it will
 * show, and when it arrives. Only real links, never a dead button.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, Hourglass } from 'lucide-react';
import type { ReactNode } from 'react';

export interface PhasePlaceholderProps {
  /** The question the page answers (SPEC §3.3 IA). */
  question: string;
  /** Roadmap phase the page arrives in, e.g. "P3". */
  phase: string;
  phaseName: string;
  /** What the page will show, in plain words. */
  planned: readonly string[];
  /** Where to look meanwhile (optional). */
  meanwhile?: ReactNode;
}

export function PhasePlaceholder({ question, phase, phaseName, planned, meanwhile }: PhasePlaceholderProps) {
  return (
    <section aria-labelledby="placeholder-title" className="max-w-2xl space-y-5">
      <div className="space-y-2">
        <span className="inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted">
          <Hourglass aria-hidden="true" className="size-3.5" />
          Arrives in {phase} · {phaseName}
        </span>
        <h2 id="placeholder-title" className="text-xl font-semibold text-fg">
          {question}
        </h2>
        <p className="text-sm text-fg-muted">This page is not built yet. When it lands it will show:</p>
      </div>
      <ul className="space-y-1.5 text-sm text-fg">
        {planned.map((item) => (
          <li key={item} className="flex gap-2">
            <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-fg-subtle" />
            {item}
          </li>
        ))}
      </ul>
      {meanwhile ? <div className="rounded-lg border border-border bg-surface p-4 text-sm text-fg-muted">{meanwhile}</div> : null}
    </section>
  );
}

/** "Meanwhile, see what happened to each request in Traffic." */
export function SeeTraffic() {
  return (
    <p>
      Meanwhile, every checked request is listed in{' '}
      <Link to="/app/traffic" className="inline-flex items-center gap-0.5 font-medium text-fg underline underline-offset-2">
        Traffic
        <ArrowRight aria-hidden="true" className="size-3.5" />
      </Link>
    </p>
  );
}
