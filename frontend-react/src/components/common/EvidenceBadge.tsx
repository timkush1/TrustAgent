/**
 * Where a number comes from (SPEC §7.4 "every number has an EvidenceBadge"; CLAUDE.md "honest
 * numbers"): Measured, Estimate, Third-party, Vendor or Simulated. It is achromatic (a value label
 * is not a verdict) and opens a popover with the glossary definition and, when the server sent
 * them, the basis of the number, its source link and date.
 */
import type { CSSProperties } from 'react';
import { Popover, PopoverContent, PopoverDescription, PopoverTitle, PopoverTrigger } from '@/components/ui/popover';
import { valueLabelView } from '@/domain/controlPresentation';
import { glossaryTerm } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { LabelledValue, ValueLabel } from '@/types/api';

export interface EvidenceBadgeProps {
  label: ValueLabel;
  /** How the number was produced ("tokens × the price table"). */
  basis?: string | null;
  sourceUrl?: string | null;
  asOf?: string | null;
  /** What the number is, for the popover title and the accessible name ("Your cost per 1,000 answers"). */
  subject?: string;
  className?: string;
  style?: CSSProperties;
}

export function EvidenceBadge({ label, basis, sourceUrl, asOf, subject, className, style }: EvidenceBadgeProps) {
  const view = valueLabelView(label);
  const Icon = view.icon;
  const definition = glossaryTerm(view.term)?.short;
  return (
    <Popover>
      <PopoverTrigger
        openOnHover
        delay={250}
        aria-label={subject ? `${view.label}: how “${subject}” was produced` : `${view.label}: how this number was produced`}
        data-value-label={label}
        style={style}
        className={cn(
          'inline-flex h-5 shrink-0 cursor-help items-center gap-1 rounded-full border border-border bg-surface-sunken px-1.5 align-middle text-2xs font-medium whitespace-nowrap text-fg-muted hover:text-fg',
          className,
        )}
      >
        <Icon aria-hidden="true" className="size-3" />
        {view.label}
      </PopoverTrigger>
      <PopoverContent side="top" className="w-72 space-y-1.5">
        <PopoverTitle className="text-sm font-semibold">
          {view.label}
          {subject ? <span className="font-normal text-fg-muted"> · {subject}</span> : null}
        </PopoverTitle>
        {definition ? <PopoverDescription className="text-sm text-fg-muted">{definition}</PopoverDescription> : null}
        {basis ? <p className="text-sm text-fg">{basis}</p> : null}
        {sourceUrl || asOf ? (
          <p className="text-xs text-fg-muted">
            {sourceUrl ? (
              <a href={sourceUrl} target="_blank" rel="noreferrer noopener" className="font-medium text-fg underline underline-offset-2">
                Source
              </a>
            ) : null}
            {sourceUrl && asOf ? ' · ' : null}
            {asOf ? `as of ${asOf}` : null}
          </p>
        ) : null}
      </PopoverContent>
    </Popover>
  );
}

/** The badge for a LabelledValue from the API. */
export function LabelledValueBadge({ value, subject, className }: { value: LabelledValue; subject?: string; className?: string }) {
  return (
    <EvidenceBadge
      label={value.label}
      basis={value.basis}
      sourceUrl={value.source_url}
      asOf={value.as_of}
      subject={subject}
      className={className}
    />
  );
}
