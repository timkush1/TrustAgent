import { Eye, FlaskConical } from 'lucide-react';
import { useAppMode } from '@/data/useSystem';
import { copy } from '@/domain/presentation';

/** "Demo" and "Read-only" markers in the top bar (the banner explains them). */
export function ModeBadge() {
  const { demo, readonly } = useAppMode();
  if (!demo && !readonly) return null;
  return (
    <span className="flex items-center gap-1.5">
      {demo ? (
        <span className="inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted">
          <FlaskConical aria-hidden="true" className="size-3.5" />
          Demo
          <span className="sr-only">: {copy.banners.demo.body}</span>
        </span>
      ) : null}
      {readonly ? (
        <span className="inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted">
          <Eye aria-hidden="true" className="size-3.5" />
          Read-only
        </span>
      ) : null}
    </span>
  );
}
