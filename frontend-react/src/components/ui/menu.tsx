/**
 * Menu over Base UI: a short list of actions behind one button (a row's "More actions", an
 * export choice). Arrow keys move, typing jumps to an item, Enter runs it, Escape closes and
 * returns focus. Actions that change data still go through <ConfirmDialog> when destructive.
 *
 * No dead items: a disabled item carries `disabledReason`, shown under its label.
 */
import { Menu as BaseMenu } from '@base-ui/react/menu';
import { Check } from 'lucide-react';
import type { ComponentProps, ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { optionClass, popupClass } from './control-styles';

export const Menu = BaseMenu.Root;
export const MenuTrigger = BaseMenu.Trigger;
export const MenuGroup = BaseMenu.Group;
export const MenuRadioGroup = BaseMenu.RadioGroup;

type PositionerProps = ComponentProps<typeof BaseMenu.Positioner>;

export interface MenuContentProps {
  children: ReactNode;
  side?: PositionerProps['side'];
  align?: PositionerProps['align'];
  sideOffset?: number;
  className?: string;
}

export function MenuContent({ children, side = 'bottom', align = 'end', sideOffset = 4, className }: MenuContentProps) {
  return (
    <BaseMenu.Portal>
      <BaseMenu.Positioner side={side} align={align} sideOffset={sideOffset} className="z-50 outline-none">
        <BaseMenu.Popup className={cn(popupClass, 'min-w-44', className)}>{children}</BaseMenu.Popup>
      </BaseMenu.Positioner>
    </BaseMenu.Portal>
  );
}

type ItemProps = Omit<ComponentProps<typeof BaseMenu.Item>, 'className' | 'children'>;

export interface MenuItemProps extends ItemProps {
  children: ReactNode;
  icon?: ReactNode;
  /** Why the item is unavailable; disables it and is shown under the label. */
  disabledReason?: string;
  /** A keyboard hint on the right (<Kbd>). */
  shortcut?: ReactNode;
  className?: string;
}

export function MenuItem({ children, icon, disabledReason, shortcut, disabled, className, ...props }: MenuItemProps) {
  return (
    <BaseMenu.Item disabled={disabled || disabledReason !== undefined} className={cn(optionClass, className)} {...props}>
      {icon ? <span className="flex w-4 shrink-0 justify-center text-fg-muted">{icon}</span> : null}
      <span className="min-w-0 flex-1">
        <span className="block truncate">{children}</span>
        {disabledReason ? <span className="block text-xs text-fg-muted">{disabledReason}</span> : null}
      </span>
      {shortcut ? <span className="ml-3 shrink-0">{shortcut}</span> : null}
    </BaseMenu.Item>
  );
}

export function MenuLinkItem({ className, ...props }: Omit<ComponentProps<typeof BaseMenu.LinkItem>, 'className'> & { className?: string }) {
  return <BaseMenu.LinkItem className={cn(optionClass, className)} {...props} />;
}

export function MenuCheckboxItem({
  children,
  className,
  ...props
}: Omit<ComponentProps<typeof BaseMenu.CheckboxItem>, 'className' | 'children'> & { children: ReactNode; className?: string }) {
  return (
    <BaseMenu.CheckboxItem className={cn(optionClass, className)} {...props}>
      <span className="flex w-4 shrink-0 justify-center">
        <BaseMenu.CheckboxItemIndicator>
          <Check aria-hidden="true" />
        </BaseMenu.CheckboxItemIndicator>
      </span>
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </BaseMenu.CheckboxItem>
  );
}

export function MenuRadioItem({
  children,
  className,
  ...props
}: Omit<ComponentProps<typeof BaseMenu.RadioItem>, 'className' | 'children'> & { children: ReactNode; className?: string }) {
  return (
    <BaseMenu.RadioItem className={cn(optionClass, className)} {...props}>
      <span className="flex w-4 shrink-0 justify-center">
        <BaseMenu.RadioItemIndicator>
          <span aria-hidden="true" className="block size-1.5 rounded-full bg-fg" />
        </BaseMenu.RadioItemIndicator>
      </span>
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </BaseMenu.RadioItem>
  );
}

export function MenuGroupLabel({ className, ...props }: Omit<ComponentProps<typeof BaseMenu.GroupLabel>, 'className'> & { className?: string }) {
  return <BaseMenu.GroupLabel className={cn('px-2 pt-1.5 pb-1 text-xs font-medium text-fg-muted', className)} {...props} />;
}

export function MenuSeparator({ className }: { className?: string }) {
  return <BaseMenu.Separator className={cn('mx-1 my-1 h-px bg-border', className)} />;
}
