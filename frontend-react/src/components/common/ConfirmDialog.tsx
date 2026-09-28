/**
 * The danger-confirm pattern (DESIGN.md "Buttons"). A destructive or risky action starts from an
 * ordinary button and is carried out only from this alert dialog:
 *
 * - The title asks the question ("Remove the OpenRouter key?"); the body says what will happen.
 * - Cancel gets focus first, so Enter never destroys by accident; a click outside does not close.
 * - The confirm button says the action ("Remove key"). With `tone="danger"` it is the danger fill,
 *   the only place that fill appears.
 * - `confirmationText` adds a typed confirmation for actions that are hard to undo or that change
 *   what users receive ("Allow uncalibrated blocking"): the button works once the text matches,
 *   and the instruction next to it says so.
 * - `onConfirm` may return a promise: the button shows progress, the dialog stays open, and a
 *   failure shows the server's message in place so the viewer can try again or cancel.
 */
import { AlertDialog } from '@base-ui/react/alert-dialog';
import { CircleAlert } from 'lucide-react';
import { useId, useRef, useState, type ReactElement, type ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { backdropClass } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/cn';
import { describeError } from './describeError';

export interface ConfirmDialogProps {
  /** The button that opens the dialog. Omit it and control `open` to open it from elsewhere. */
  trigger?: ReactElement;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  title: ReactNode;
  /** What will happen, in plain words. */
  description?: ReactNode;
  /** More detail under the description (a list of consequences, a diff). */
  children?: ReactNode;
  /** The action, as a verb ("Remove key", "Allow blocking"). */
  confirmLabel: string;
  cancelLabel?: string;
  onConfirm: () => void | Promise<unknown>;
  /** `danger` for destructive or risky actions (danger fill); `neutral` for a plain confirmation. */
  tone?: 'danger' | 'neutral';
  /** Text the viewer must type before the action is available (a route or key name). */
  confirmationText?: string;
}

export function ConfirmDialog({
  trigger,
  open: openProp,
  onOpenChange,
  title,
  description,
  children,
  confirmLabel,
  cancelLabel = 'Cancel',
  onConfirm,
  tone = 'danger',
  confirmationText,
}: ConfirmDialogProps) {
  const [openState, setOpenState] = useState(false);
  const open = openProp ?? openState;
  const [typed, setTyped] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const id = useId();
  const instructionId = `${id}-instruction`;
  const inputId = `${id}-input`;

  // Every opening starts clean, however it was opened (the trigger, or a parent setting `open`):
  // no text typed for an earlier attempt, no error from it.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setTyped('');
      setError(null);
    }
  }

  const matches = confirmationText === undefined || typed.trim() === confirmationText;

  const setOpen = (next: boolean) => {
    if (!next && pending) return; // Stay open until the action settles.
    setOpenState(next);
    onOpenChange?.(next);
  };

  const confirm = async () => {
    if (!matches || pending) return;
    setError(null);
    setPending(true);
    try {
      await onConfirm();
      setPending(false);
      setOpenState(false);
      onOpenChange?.(false);
    } catch (err) {
      setPending(false);
      setError(err);
    }
  };

  const failure = error === null ? null : describeError(error);

  return (
    <AlertDialog.Root open={open} onOpenChange={setOpen}>
      {trigger ? <AlertDialog.Trigger render={trigger} /> : null}
      <AlertDialog.Portal>
        <AlertDialog.Backdrop className={backdropClass} />
        <AlertDialog.Popup
          initialFocus={confirmationText === undefined ? cancelRef : inputRef}
          className={cn(
            'fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100dvh-4rem)] w-[min(28rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto',
            'rounded-lg border border-border bg-surface-raised p-5 text-sm text-fg shadow-popup outline-none',
            'transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0',
          )}
        >
          <div className="space-y-1">
            <AlertDialog.Title className="text-base font-semibold text-fg">{title}</AlertDialog.Title>
            {description ? <AlertDialog.Description className="text-sm text-fg-muted">{description}</AlertDialog.Description> : null}
          </div>
          {children}
          {confirmationText !== undefined ? (
            <div className="space-y-1">
              <label id={instructionId} htmlFor={inputId} className="block text-sm text-fg">
                To confirm, type <code className="rounded-sm bg-surface-sunken px-1 font-mono text-xs">{confirmationText}</code>
              </label>
              <Input
                ref={inputRef}
                id={inputId}
                value={typed}
                onValueChange={setTyped}
                autoComplete="off"
                spellCheck={false}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    void confirm();
                  }
                }}
              />
            </div>
          ) : null}
          {failure ? (
            <div role="alert" className="flex items-start gap-2 rounded-md border border-border bg-surface px-3 py-2">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
              <div className="min-w-0 space-y-0.5">
                <p className="text-sm text-fg">{failure.message}</p>
                {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
              </div>
            </div>
          ) : null}
          <div className="flex flex-wrap items-center justify-end gap-2">
            <AlertDialog.Close
              ref={cancelRef}
              disabled={pending}
              render={<Button variant="secondary">{cancelLabel}</Button>}
            />
            <Button
              variant={tone === 'danger' ? 'danger' : 'primary'}
              disabled={!matches}
              pending={pending}
              aria-describedby={confirmationText !== undefined && !matches ? instructionId : undefined}
              onClick={() => void confirm()}
            >
              {confirmLabel}
            </Button>
          </div>
        </AlertDialog.Popup>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
