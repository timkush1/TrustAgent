/** Class strings shared by the landing page's components. */
export const container = 'mx-auto w-full max-w-6xl px-6 max-sm:px-4';

export const buttonClass = {
  primary:
    'inline-flex h-11 items-center justify-center gap-2 rounded-md bg-fg px-5 text-base font-medium text-fg-inverse transition-opacity hover:opacity-90 [&_svg]:size-4',
  secondary:
    'inline-flex h-11 items-center justify-center gap-2 rounded-md border border-border-strong bg-surface px-5 text-base font-medium text-fg transition-colors hover:bg-surface-hover [&_svg]:size-4',
  ghost:
    'inline-flex h-9 items-center justify-center gap-1.5 rounded-md px-3 text-sm font-medium text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg [&_svg]:size-4',
} as const;
