/**
 * Button classes, kept apart from the component so links styled as buttons can share them
 * (`<Link className={buttonVariants({ variant: 'secondary' })}>`).
 *
 * - `primary` is the inverted foreground fill: there is no colored call to action (DESIGN.md).
 * - `secondary` is the default: a surface with a hairline.
 * - `ghost` is for toolbars and icon buttons.
 * - `danger` is the danger-tone fill. It is used only on the final, confirming step of a
 *   destructive action (inside <ConfirmDialog>), never on the button that starts it.
 */
import { cva, type VariantProps } from 'class-variance-authority';

export const buttonVariants = cva(
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
        danger: 'bg-tone-danger text-fg-inverse hover:not-data-disabled:opacity-90',
      },
      size: {
        sm: 'h-7 px-2.5 text-xs',
        md: 'h-8 px-3 text-sm',
        icon: 'size-8 p-0',
        'icon-sm': 'size-7 p-0 [&_svg]:size-3.5',
      },
    },
    defaultVariants: { variant: 'secondary', size: 'md' },
  },
);

export type ButtonVariantProps = VariantProps<typeof buttonVariants>;
