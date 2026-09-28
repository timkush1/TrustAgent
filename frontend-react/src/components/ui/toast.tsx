/**
 * The app's toaster: sonner, unstyled and redrawn with the design tokens so toasts match both
 * themes. Mount it once at the root (it replaces a bare sonner <Toaster>); raise toasts with
 * `notify` from src/components/common/notify.ts.
 *
 * Toasts confirm what the viewer just did ("Key removed") and report background failures. They
 * never carry the only copy of something important: errors stay until dismissed, and anything
 * the viewer must act on also appears on the page. Sonner announces each toast politely and
 * pauses the timer on hover and focus.
 */
import { CircleCheck, Info, LoaderCircle, TriangleAlert, X } from 'lucide-react';
import { Toaster as SonnerToaster } from 'sonner';
import { useThemePreference } from '@/lib/theme';
import { buttonVariants } from './button-variants';

export function Toaster() {
  const theme = useThemePreference();
  return (
    <SonnerToaster
      theme={theme}
      position="bottom-right"
      closeButton
      containerAriaLabel="Notifications"
      icons={{
        success: <CircleCheck aria-hidden="true" className="size-4 text-tone-success" />,
        error: <TriangleAlert aria-hidden="true" className="size-4 text-tone-danger" />,
        warning: <TriangleAlert aria-hidden="true" className="size-4 text-tone-warning" />,
        info: <Info aria-hidden="true" className="size-4 text-fg-muted" />,
        loading: <LoaderCircle aria-hidden="true" className="size-4 animate-spin text-fg-muted" />,
        close: <X aria-hidden="true" className="size-3.5" />,
      }}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast:
            'relative flex w-(--width) items-start gap-2 rounded-lg border border-border bg-surface-raised p-3 pr-9 text-sm text-fg shadow-popup',
          icon: 'mt-0.5 flex shrink-0',
          content: 'min-w-0 flex-1 space-y-0.5',
          title: 'font-medium text-fg',
          description: 'text-xs text-fg-muted',
          actionButton: buttonVariants({ variant: 'secondary', size: 'sm' }),
          cancelButton: buttonVariants({ variant: 'ghost', size: 'sm' }),
          closeButton:
            'absolute top-2 right-2 inline-flex size-6 items-center justify-center rounded-sm text-fg-muted hover:bg-surface-hover hover:text-fg',
        },
      }}
    />
  );
}
