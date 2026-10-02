import { Link } from '@tanstack/react-router';
import { ArrowRight, CircleAlert, ExternalLink, Info } from 'lucide-react';
import type { CSSProperties } from 'react';
import { useRuntimeConfig } from '@/app/services';
import { ReplayBanner } from '@/components/replay/ReplayBanner';
import { useBanner } from '@/data/useSystem';
import type { BannerFix } from '@/domain/banner';
import { splitCommands, toneVar } from '@/domain/presentation';
import { cn } from '@/lib/cn';

/** The single full-width banner (SPEC §3.3; copy from ui/copy.v1.json). */
export function Banner() {
  return useRuntimeConfig().appMode === 'static' ? <ReplayBanner /> : <GatewayBanner />;
}

function GatewayBanner() {
  const banner = useBanner();
  if (!banner) return null;
  const Icon = banner.tone === 'neutral' ? Info : CircleAlert;
  return (
    <div
      role={banner.tone === 'neutral' ? 'note' : 'alert'}
      data-banner={banner.id}
      style={{ '--tone': `var(${toneVar(banner.tone)})` } as CSSProperties}
      className={cn(
        'flex items-start gap-2 border-b px-6 py-2 text-sm',
        banner.tone === 'neutral'
          ? 'border-border bg-surface-sunken text-fg'
          : 'border-[color-mix(in_oklab,var(--tone)_40%,transparent)] bg-[color-mix(in_oklab,var(--tone)_var(--tint),var(--surface))] text-fg',
      )}
    >
      <Icon aria-hidden="true" className={cn('mt-0.5 size-4 shrink-0', banner.tone === 'neutral' ? 'text-fg-muted' : 'text-(--tone)')} />
      <p className="min-w-0">
        <span className="font-medium">{banner.title}.</span> <span className="text-fg-muted">{banner.body}</span>
        {banner.action ? (
          <span className="text-fg-muted">
            {' '}
            {splitCommands(banner.action).map((part, i) =>
              part.command ? (
                <code key={i} className="rounded-sm bg-surface px-1 py-0.5 font-mono text-xs text-fg">
                  {part.text}
                </code>
              ) : (
                <span key={i}>{part.text}</span>
              ),
            )}
          </span>
        ) : null}
        {banner.fix ? (
          <>
            {' '}
            <FixLink fix={banner.fix} />
          </>
        ) : null}
      </p>
    </div>
  );
}

const fixClass = 'inline-flex items-center gap-0.5 font-medium whitespace-nowrap text-fg underline underline-offset-2';

/** Where the banner's problem is fixed: a Settings section, or OpenRouter's credits page. */
function FixLink({ fix }: { fix: BannerFix }) {
  if (fix.kind === 'external') {
    return (
      <a href={fix.href} target="_blank" rel="noopener noreferrer" className={fixClass}>
        {fix.label}
        <ExternalLink aria-hidden="true" className="size-3.5" />
        <span className="sr-only"> (opens in a new tab)</span>
      </a>
    );
  }
  return (
    <Link to="/app/settings/$section" params={{ section: fix.section }} className={fixClass}>
      {fix.label}
      <ArrowRight aria-hidden="true" className="size-3.5" />
    </Link>
  );
}
