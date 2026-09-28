/** Popover over Base UI, styled with the design tokens. */
import { Popover as BasePopover } from '@base-ui/react/popover';
import type { ComponentProps } from 'react';
import { cn } from '@/lib/cn';

export const Popover = BasePopover.Root;
export const PopoverTrigger = BasePopover.Trigger;
export const PopoverTitle = BasePopover.Title;
export const PopoverDescription = BasePopover.Description;
export const PopoverClose = BasePopover.Close;

type PopupProps = Omit<ComponentProps<typeof BasePopover.Popup>, 'className'> & {
  className?: string;
  side?: ComponentProps<typeof BasePopover.Positioner>['side'];
  align?: ComponentProps<typeof BasePopover.Positioner>['align'];
  sideOffset?: number;
};

export function PopoverContent({ className, side = 'bottom', align = 'center', sideOffset = 8, ...props }: PopupProps) {
  return (
    <BasePopover.Portal>
      <BasePopover.Positioner side={side} align={align} sideOffset={sideOffset} className="z-50">
        <BasePopover.Popup
          className={cn(
            'max-w-[min(24rem,calc(100vw-2rem))] origin-(--transform-origin) rounded-lg border border-border bg-surface-raised p-3 text-sm text-fg shadow-popup outline-none',
            'transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0',
            className,
          )}
          {...props}
        />
      </BasePopover.Positioner>
    </BasePopover.Portal>
  );
}
