/**
 * An icon-only button. The accessible name is required (`aria-label`), and by default the same
 * words appear as a tooltip for sighted pointer and keyboard users (DESIGN.md: a tooltip only
 * repeats an aria-label, it never carries anything new).
 */
import type { LucideIcon } from 'lucide-react';
import { Button, type ButtonProps } from './button';
import { Tooltip } from './tooltip';

export type IconButtonProps = Omit<ButtonProps, 'size' | 'children' | 'aria-label' | 'aria-labelledby'> & {
  /** What the button does, in words. Required: an icon alone has no name. */
  'aria-label': string;
  icon: LucideIcon;
  size?: 'sm' | 'md';
  /** Show the aria-label as a tooltip (default true). */
  tooltip?: boolean;
};

export function IconButton({ icon: Icon, size = 'md', tooltip = true, variant = 'ghost', ...props }: IconButtonProps) {
  const label = props['aria-label'];
  if (import.meta.env.DEV && !label.trim()) console.error('<IconButton> needs a non-empty aria-label');
  const button = (
    <Button variant={variant} size={size === 'sm' ? 'icon-sm' : 'icon'} {...props}>
      <Icon aria-hidden="true" />
    </Button>
  );
  return tooltip ? <Tooltip label={label}>{button}</Tooltip> : button;
}
