/**
 * Small building blocks of the landing page. No Base UI, router or chart code here: the landing
 * entry has a 110 KB gzip budget (npm run size), so these are plain elements over the shared tokens.
 */
import clsx from 'clsx';
import { Check, Copy, Monitor, Moon, Sun } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { copyText } from '@/components/common/clipboard';
import { nextThemePreference, setThemePreference, useThemePreference, type ThemePreference } from '@/lib/theme';
import { sourceAnchor, type ClaimView } from './claims';
import { buttonClass, container } from './styles';

/** Where a number comes from: an achromatic chip that links to its entry under "Sources". */
export function LabelChip({ claim, className }: { claim: Pick<ClaimView, 'id' | 'label' | 'labelText'>; className?: string }) {
  return (
    <a
      href={`#${sourceAnchor(claim.id)}`}
      data-value-label={claim.label}
      aria-label={`${claim.labelText}: where this number comes from`}
      className={clsx(
        'inline-flex h-5 shrink-0 items-center rounded-full border border-border-strong px-1.5 align-middle text-2xs font-medium text-fg-muted no-underline transition-colors hover:border-fg-subtle hover:text-fg',
        claim.label === 'simulated' && 'border-dashed',
        className,
      )}
    >
      {claim.labelText}
    </a>
  );
}

/** A number from value-claims with its label chip ("~$0.21 / 1,000 answers [Estimate]"). */
export function ClaimValue({ claim, className, strong = true }: { claim: ClaimView; className?: string; strong?: boolean }) {
  if (!claim.hasValue) return <span className={clsx('text-fg-muted', className)}>{claim.display}</span>;
  return (
    <span data-chip-scope className={clsx('inline-flex flex-wrap items-baseline gap-x-1.5', className)}>
      <span className={clsx('tabular', strong && 'font-medium text-fg')}>{claim.display}</span>
      <LabelChip claim={claim} className="self-center" />
    </span>
  );
}

/**
 * A claim's caveat, placed next to its chip (D128, DESIGN §12b): inside the claim's chip scope or
 * the small block marked data-caveat-scope that holds it. Nothing for a claim without one.
 */
export function ClaimCaveat({ claim, className }: { claim: Pick<ClaimView, 'id' | 'caveat'>; className?: string }) {
  if (!claim.caveat) return null;
  return (
    <p data-caveat-for={claim.id} className={clsx('text-xs text-fg-subtle', className)}>
      {/* A figure stays on one line with its unit ("0.3 s", not "0.3" and "s"). */}
      {claim.caveat.replace(/(\d) (s|ms|%)(?=\W|$)/g, '$1\u00a0$2')}
    </p>
  );
}

export function Section({ id, eyebrow, title, lead, children, className }: { id: string; eyebrow: string; title: string; lead?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section id={id} aria-labelledby={`${id}-title`} className={clsx('border-t border-border py-20 max-sm:py-14', className)}>
      <div className={container}>
        <p className="mb-3 text-sm font-medium text-fg-muted">{eyebrow}</p>
        <h2 id={`${id}-title`} className="max-w-3xl text-4xl font-semibold tracking-tight text-fg max-sm:text-3xl">
          {title}
        </h2>
        {lead ? <p className="mt-4 max-w-2xl text-lead text-fg-muted">{lead}</p> : null}
        <div className="mt-10 max-sm:mt-8">{children}</div>
      </div>
    </section>
  );
}

/** Contract text marks field names with backticks: show them as code. */
export function Ticked({ text }: { text: string }) {
  return (
    <>
      {text.split('`').map((part, i) =>
        i % 2 === 1 ? (
          <code key={i} className="font-mono text-[0.85em] text-fg">
            {part}
          </code>
        ) : (
          <span key={i}>{part}</span>
        ),
      )}
    </>
  );
}

/** A shell command with a copy button: the command is the text, so nothing hides in the button. */
/**
 * A command with a copy button. A short one stays on one line; `wrap` lets a long one break over
 * lines instead of being cut off, so the reader can see what they copy.
 */
/**
 * Code that must keep its lines (a wrapped YAML or shell line reads as broken): it never wraps, and
 * on a narrow screen it scrolls sideways inside a named, focusable region (DESIGN.md: wide content
 * scrolls inside a named, focusable region, never the page).
 */
export function CodeScroll({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return (
    // A keyboard has to reach it to scroll it.
    // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
    <div role="region" aria-label={`${label} (scrolls sideways)`} tabIndex={0} className={clsx('min-w-0 overflow-x-auto', className)} data-code-scroll>
      {children}
    </div>
  );
}

/**
 * A shell command with a copy button. A short one is cut off with an ellipsis if it must be; a long
 * one (`scroll`) keeps its line and scrolls sideways instead, so no command is shown half.
 */
export function Command({ command, note, className, scroll = false }: { command: string; note?: string; className?: string; scroll?: boolean }) {
  const [copied, setCopied] = useState<boolean | null>(null);
  const code = (
    <code className={clsx('min-w-0 font-mono text-sm text-fg', scroll ? 'block w-max whitespace-pre' : 'truncate')}>
      <span aria-hidden="true" className="mr-2 text-fg-muted select-none">
        $
      </span>
      {command}
    </code>
  );
  return (
    <div className={clsx('inline-flex max-w-full items-center gap-1 rounded-md border border-border bg-surface-sunken py-1 pr-1 pl-3', className)}>
      {scroll ? (
        <CodeScroll label="Command" className="py-1">
          {code}
        </CodeScroll>
      ) : (
        code
      )}
      {note ? <span className="ml-1 text-xs whitespace-nowrap text-fg-muted max-sm:hidden">{note}</span> : null}
      <button
        type="button"
        onClick={() => void copyText(command).then(setCopied)}
        aria-label={`Copy ${command}`}
        className="ml-1 inline-flex size-8 shrink-0 items-center justify-center rounded-sm text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg"
      >
        {copied ? <Check aria-hidden="true" className="size-4" /> : <Copy aria-hidden="true" className="size-4" />}
      </button>
      <span className="sr-only" aria-live="polite">
        {copied === true ? 'Copied' : copied === false ? 'Copy failed: select the command and copy it by hand' : ''}
      </span>
    </div>
  );
}

const themeMeta: Record<ThemePreference, { label: string; icon: typeof Sun }> = {
  light: { label: 'Light', icon: Sun },
  dark: { label: 'Dark', icon: Moon },
  system: { label: 'System', icon: Monitor },
};

export function ThemeButton() {
  const pref = useThemePreference();
  const next = nextThemePreference(pref);
  const { icon: Icon, label } = themeMeta[pref];
  const text = `Theme: ${label}. Switch to ${themeMeta[next].label}`;
  return (
    <button type="button" onClick={() => setThemePreference(next)} aria-label={text} title={text} className={clsx(buttonClass.ghost, 'w-9 px-0')}>
      <Icon aria-hidden="true" />
    </button>
  );
}

/** The GitHub mark (lucide dropped brand icons), drawn in the text color. */
export function GitHubMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" className={className} fill="currentColor">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  );
}

/** The product mark (public/favicon.svg), inline so it follows the theme. */
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" aria-hidden="true" className={className} fill="currentColor">
      <path d="M16 2 4 6.5v8.2c0 7.3 5 13.4 12 15.3 7-1.9 12-8 12-15.3V6.5L16 2Zm-1.6 19.4-5.3-5.3 2.1-2.1 3.2 3.2 6.9-6.9 2.1 2.1-9 9Z" />
    </svg>
  );
}
