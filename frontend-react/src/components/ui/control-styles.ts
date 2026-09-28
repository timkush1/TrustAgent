/**
 * Shared classes for form controls. A control's boundary is how it is recognised, so it must reach
 * 3:1 against the surface in both themes (WCAG 1.4.11): controls use `fg-subtle` at 80% (≈3.2:1),
 * not the decorative `border-strong` hairline (≈1.8:1). charts/tokenContrast.test.ts checks it.
 */
export const CONTROL_BORDER = 'border-fg-subtle/80';

/** Text inputs, textareas, select and combobox triggers. */
export const fieldControlClass = [
  'w-full min-w-0 rounded-md border bg-surface px-2.5 text-sm text-fg',
  CONTROL_BORDER,
  'placeholder:text-fg-subtle',
  'data-disabled:cursor-not-allowed data-disabled:opacity-50',
  'data-invalid:border-tone-danger',
  // Base UI marks read-only controls; the :read-only pseudo-class would also match buttons.
  'data-readonly:bg-surface-sunken',
].join(' ');

/** Floating lists (select, combobox, menu). */
export const popupClass = [
  'origin-(--transform-origin) rounded-lg border border-border bg-surface-raised p-1 text-sm text-fg shadow-popup outline-none',
  'transition-[scale,opacity] duration-150',
  'data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0',
].join(' ');

/** One option in a floating list. */
export const optionClass = [
  'flex min-h-8 cursor-default items-center gap-2 rounded-sm px-2 py-1 text-sm text-fg outline-none select-none',
  'data-highlighted:bg-surface-hover data-disabled:cursor-not-allowed data-disabled:text-fg-subtle',
  '[&_svg]:size-4 [&_svg]:shrink-0',
].join(' ');
