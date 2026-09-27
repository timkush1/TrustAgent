/**
 * Glossary term with its definition (contracts/ui/glossary.v1.json) in a popover that opens on
 * hover, focus or tap, so touch and screen-reader users get it too (an infotip, not a tooltip).
 */
import type { ReactNode } from 'react';
import { Popover, PopoverContent, PopoverDescription, PopoverTitle, PopoverTrigger } from '@/components/ui/popover';
import { glossaryTerm } from '@/domain/presentation';
import { cn } from '@/lib/cn';

export function Term({ id, children, className }: { id: string; children?: ReactNode; className?: string }) {
  const term = glossaryTerm(id);
  if (!term) {
    if (import.meta.env.DEV) console.warn(`<Term id="${id}">: not in contracts/ui/glossary.v1.json`);
    return <>{children}</>;
  }
  return (
    <Popover>
      <PopoverTrigger
        openOnHover
        delay={250}
        className={cn(
          'cursor-help rounded-sm underline decoration-fg-subtle decoration-dotted underline-offset-3 hover:decoration-fg',
          className,
        )}
      >
        {children ?? term.term}
      </PopoverTrigger>
      <PopoverContent side="top" className="w-72 space-y-1">
        <PopoverTitle className="text-sm font-semibold">{term.term}</PopoverTitle>
        <PopoverDescription className="text-sm text-fg-muted">{term.short}</PopoverDescription>
      </PopoverContent>
    </Popover>
  );
}
