/**
 * Dialog over Base UI: a modal task that needs the viewer's full attention (review a diff, edit a
 * key). Focus moves in and is trapped, Escape and the close button dismiss it, and focus returns
 * to the trigger. The title is the dialog's accessible name and the description its description.
 *
 * For a confirmation before an irreversible action use <ConfirmDialog>; for a side panel that
 * keeps the page in view use <Sheet>.
 */
import { Dialog as BaseDialog } from '@base-ui/react/dialog';
import { X } from 'lucide-react';
import type { ComponentProps, ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { Button } from './button';

export const Dialog = BaseDialog.Root;
export const DialogTrigger = BaseDialog.Trigger;
export const DialogClose = BaseDialog.Close;

/** The page behind a modal: a veil of the page color (elevation is lightness, not a dark scrim). */
export const backdropClass =
  'fixed inset-0 z-50 bg-bg/80 transition-opacity duration-150 data-ending-style:opacity-0 data-starting-style:opacity-0';

const sizeClass = { sm: 'w-[min(24rem,calc(100vw-2rem))]', md: 'w-[min(32rem,calc(100vw-2rem))]', lg: 'w-[min(44rem,calc(100vw-2rem))]' };

type PopupProps = Omit<ComponentProps<typeof BaseDialog.Popup>, 'className' | 'children' | 'title'>;

export interface DialogContentProps extends PopupProps {
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  /** Buttons, right-aligned under the body (use <DialogFooter> for custom layouts). */
  footer?: ReactNode;
  size?: keyof typeof sizeClass;
  /** Hide the corner close button (the footer must then offer a way out). */
  hideClose?: boolean;
  className?: string;
}

/** Title, description and the corner close button, shared by Dialog and Sheet. */
export function DialogHeading({ title, description, hideClose = false }: Pick<DialogContentProps, 'title' | 'description' | 'hideClose'>) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0 space-y-1">
        <BaseDialog.Title className="text-base font-semibold text-fg">{title}</BaseDialog.Title>
        {description ? <BaseDialog.Description className="text-sm text-fg-muted">{description}</BaseDialog.Description> : null}
      </div>
      {hideClose ? null : (
        <BaseDialog.Close
          render={
            <Button variant="ghost" size="icon-sm" aria-label="Close" className="-mt-1 -mr-1">
              <X aria-hidden="true" />
            </Button>
          }
        />
      )}
    </div>
  );
}

export function DialogFooter({ className, ...props }: ComponentProps<'div'>) {
  return <div className={cn('flex flex-wrap items-center justify-end gap-2 pt-2', className)} {...props} />;
}

export function DialogContent({ title, description, children, footer, size = 'md', hideClose, className, ...props }: DialogContentProps) {
  return (
    <BaseDialog.Portal>
      <BaseDialog.Backdrop className={backdropClass} />
      <BaseDialog.Popup
        className={cn(
          'fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100dvh-4rem)] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto',
          'rounded-lg border border-border bg-surface-raised p-5 text-sm text-fg shadow-popup outline-none',
          'transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0',
          sizeClass[size],
          className,
        )}
        {...props}
      >
        <DialogHeading title={title} description={description} hideClose={hideClose} />
        {children}
        {footer ? <DialogFooter>{footer}</DialogFooter> : null}
      </BaseDialog.Popup>
    </BaseDialog.Portal>
  );
}
