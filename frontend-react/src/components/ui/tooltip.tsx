/**
 * Tooltip over Base UI. Only a visual label for sighted pointer and keyboard users: the trigger
 * must carry the same text as its aria-label. Anything a reader must not miss goes inline or in a
 * Popover (see <Term>).
 */
import { Tooltip as BaseTooltip } from '@base-ui/react/tooltip';
import type { ReactElement, ReactNode } from 'react';

export const TooltipProvider = BaseTooltip.Provider;

export function Tooltip({ label, children }: { label: ReactNode; children: ReactElement }) {
  return (
    <BaseTooltip.Root>
      <BaseTooltip.Trigger render={children} />
      <BaseTooltip.Portal>
        <BaseTooltip.Positioner sideOffset={6} className="z-50">
          <BaseTooltip.Popup className="rounded-sm bg-fg px-2 py-1 text-xs text-fg-inverse transition-opacity duration-150 data-ending-style:opacity-0 data-starting-style:opacity-0">
            {label}
          </BaseTooltip.Popup>
        </BaseTooltip.Positioner>
      </BaseTooltip.Portal>
    </BaseTooltip.Root>
  );
}
