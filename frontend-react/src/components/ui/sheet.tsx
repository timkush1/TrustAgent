/**
 * Sheet: a modal panel that slides in from the right edge, for a closer look that keeps the page
 * in view (a label queue item, a chart's data, an evaluator's versions). It is a Base UI Dialog,
 * so it has the same focus trap, Escape, close button and focus return. The header and footer
 * stay put while the body scrolls.
 *
 * Base UI's Drawer adds swipe gestures and snap points for touch bottom sheets; the dashboard does
 * not need them, so the sheet is a dialog positioned at the edge.
 */
import { Dialog as BaseDialog } from '@base-ui/react/dialog';
import type { ComponentProps, ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { backdropClass, DialogHeading } from './dialog';

export const Sheet = BaseDialog.Root;
export const SheetTrigger = BaseDialog.Trigger;
export const SheetClose = BaseDialog.Close;

type PopupProps = Omit<ComponentProps<typeof BaseDialog.Popup>, 'className' | 'children' | 'title'>;

export interface SheetContentProps extends PopupProps {
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  /** Pinned under the scrolling body. */
  footer?: ReactNode;
  size?: 'md' | 'lg';
  className?: string;
}

export function SheetContent({ title, description, children, footer, size = 'md', className, ...props }: SheetContentProps) {
  return (
    <BaseDialog.Portal>
      <BaseDialog.Backdrop className={backdropClass} />
      <BaseDialog.Popup
        className={cn(
          'fixed inset-y-0 right-0 z-50 flex max-w-full flex-col border-l border-border bg-surface-raised text-sm text-fg shadow-popup outline-none',
          'transition-[translate,opacity] duration-200 data-ending-style:translate-x-8 data-ending-style:opacity-0 data-starting-style:translate-x-8 data-starting-style:opacity-0',
          size === 'lg' ? 'w-[min(44rem,100vw)]' : 'w-[min(28rem,100vw)]',
          className,
        )}
        {...props}
      >
        <div className="border-b border-border px-5 py-4">
          <DialogHeading title={title} description={description} />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer ? <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-5 py-3">{footer}</div> : null}
      </BaseDialog.Popup>
    </BaseDialog.Portal>
  );
}
