/**
 * Tabs over Base UI: switch between views of the same subject (Overview's Summary and Savings,
 * the "Connect your app" snippets). Arrow keys move between tabs and Enter or Space opens one
 * (manual activation, so a screen-reader user can browse the tab names first). An underline marks
 * the active tab, whose label also turns from muted to full ink.
 */
import { Tabs as BaseTabs } from '@base-ui/react/tabs';
import type { ComponentProps } from 'react';
import { cn } from '@/lib/cn';

export const Tabs = BaseTabs.Root;

export type TabsListProps = Omit<ComponentProps<typeof BaseTabs.List>, 'className'> & { className?: string };

export function TabsList({ className, children, ...props }: TabsListProps) {
  return (
    <BaseTabs.List className={cn('relative flex items-center gap-1 border-b border-border', className)} {...props}>
      {children}
      <BaseTabs.Indicator className="absolute bottom-[-1px] left-0 h-0.5 w-(--active-tab-width) translate-x-(--active-tab-left) rounded-full bg-fg transition-[translate,width] duration-150" />
    </BaseTabs.List>
  );
}

export type TabProps = Omit<ComponentProps<typeof BaseTabs.Tab>, 'className'> & { className?: string };

export function Tab({ className, ...props }: TabProps) {
  return (
    <BaseTabs.Tab
      className={cn(
        'inline-flex h-9 items-center gap-1.5 rounded-sm px-2 text-sm font-medium whitespace-nowrap text-fg-muted select-none',
        'hover:not-data-disabled:text-fg data-active:text-fg data-disabled:cursor-not-allowed data-disabled:opacity-50',
        '[&_svg]:size-4 [&_svg]:shrink-0',
        className,
      )}
      {...props}
    />
  );
}

export type TabPanelProps = Omit<ComponentProps<typeof BaseTabs.Panel>, 'className'> & { className?: string };

export function TabPanel({ className, ...props }: TabPanelProps) {
  return <BaseTabs.Panel className={cn('rounded-sm pt-4', className)} {...props} />;
}
