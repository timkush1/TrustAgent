/**
 * Button over Base UI (shadcn-style, written locally). Primary is the inverted fg fill; there is
 * no colored CTA (DESIGN.md: color means verdict). A disabled action must say why: wrap it in
 * <DisabledAction> (or render <DisabledReason> next to it) so there are no dead buttons.
 *
 * Buttons stay focusable while disabled, so keyboard and screen-reader users reach the reason.
 * `pending` shows a spinner, marks the button busy and ignores presses until the work settles.
 */
import { Button as BaseButton } from '@base-ui/react/button';
import { LoaderCircle } from 'lucide-react';
import type { ComponentProps } from 'react';
import { cn } from '@/lib/cn';
import { buttonVariants, type ButtonVariantProps } from './button-variants';

export type ButtonProps = Omit<ComponentProps<typeof BaseButton>, 'className'> &
  ButtonVariantProps & {
    className?: string;
    /** Work is in flight: show a spinner and ignore presses. */
    pending?: boolean;
  };

export function Button({ className, variant, size, pending = false, disabled, children, ...props }: ButtonProps) {
  return (
    <BaseButton
      focusableWhenDisabled
      disabled={disabled || pending}
      aria-busy={pending || undefined}
      data-pending={pending || undefined}
      className={cn(buttonVariants({ variant, size }), pending && 'data-disabled:cursor-progress', className)}
      {...props}
    >
      {pending ? <LoaderCircle aria-hidden="true" className="animate-spin" /> : null}
      {children}
    </BaseButton>
  );
}
