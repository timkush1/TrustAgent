/**
 * Button over Base UI (shadcn-style, written locally). Primary is the inverted fg fill; there is
 * no colored CTA (DESIGN.md: color means verdict). A disabled action must say why: pass
 * `disabledReason` and render it next to the button with <DisabledReason> (no dead buttons).
 */
import { Button as BaseButton } from '@base-ui/react/button';
import { cva, type VariantProps } from 'class-variance-authority';
import type { ComponentProps } from 'react';
import { cn } from '@/lib/cn';

const buttonVariants = cva(
  [
    'inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-md font-medium select-none',
    'transition-[background-color,border-color,opacity] outline-offset-2',
    'data-disabled:cursor-not-allowed data-disabled:opacity-50',
    '[&_svg]:size-4 [&_svg]:shrink-0',
  ],
  {
    variants: {
      variant: {
        primary: 'bg-fg text-fg-inverse hover:not-data-disabled:opacity-90',
        secondary:
          'border border-border-strong bg-surface text-fg hover:not-data-disabled:bg-surface-hover',
        ghost: 'text-fg-muted hover:not-data-disabled:bg-surface-hover hover:not-data-disabled:text-fg',
      },
      size: {
        sm: 'h-7 px-2.5 text-xs',
        md: 'h-8 px-3 text-sm',
        icon: 'size-8 p-0',
      },
    },
    defaultVariants: { variant: 'secondary', size: 'md' },
  },
);

export type ButtonProps = Omit<ComponentProps<typeof BaseButton>, 'className'> &
  VariantProps<typeof buttonVariants> & { className?: string };

export function Button({ className, variant, size, ...props }: ButtonProps) {
  return (
    <BaseButton
      focusableWhenDisabled
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  );
}
