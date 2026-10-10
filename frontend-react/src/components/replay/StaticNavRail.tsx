/**
 * The static build's navigation (SPEC §3.3 "Static build"): Overview (recorded), Traffic (replay),
 * Trace (the flagged refund answer, "caught": where the tour starts), Playground, Studio, and "Run
 * it yourself" at the bottom, which opens the landing page's self-host section. Policies, Settings
 * and Knowledge need a gateway and are not listed.
 */
import { Link, useRouterState } from '@tanstack/react-router';
import { FlaskConical, LayoutDashboard, PenLine, Rows3, ScanSearch, Terminal, type LucideIcon } from 'lucide-react';
import { useId, type ReactElement, type ReactNode } from 'react';
import { Tooltip } from '@/components/ui/tooltip';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import { useReplayStore } from '@/data/replay/replayStore';
import { useRunItYourselfHref } from './useReplay';

const itemClass =
  'flex h-9 items-center gap-2.5 rounded-md px-2.5 text-sm text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg max-lg:w-9 max-lg:justify-center max-lg:px-0';
const activeClass = 'bg-surface-hover font-medium text-fg';

function Item({
  label,
  icon: Icon,
  active,
  hint,
  description,
  children,
}: {
  label: string;
  icon: LucideIcon;
  active: boolean;
  hint?: string;
  /** A sentence about the item, in its tooltip and read after its name. */
  description?: string;
  children: (props: { className: string; 'aria-current'?: 'page'; 'aria-label': string; 'aria-describedby'?: string; content: ReactNode }) => ReactElement;
}) {
  const descriptionId = useId();
  const content = (
    <>
      <Icon aria-hidden="true" className="size-4 shrink-0" />
      <span className="max-lg:sr-only">{label}</span>
      {hint ? (
        <span aria-hidden={description ? true : undefined} className="ml-auto text-2xs font-normal text-fg-muted max-lg:hidden">
          {hint}
        </span>
      ) : null}
      {description ? (
        <span id={descriptionId} className="sr-only">
          {description}
        </span>
      ) : null}
    </>
  );
  return (
    <Tooltip label={description ? `${label}: ${description}` : label}>
      {children({
        className: cn(itemClass, active && activeClass),
        'aria-current': active ? 'page' : undefined,
        'aria-label': label,
        'aria-describedby': description ? descriptionId : undefined,
        content,
      })}
    </Tooltip>
  );
}

export function StaticNavRail() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  // Only the featured trace: the nav does not re-render as the tape plays.
  const featuredTraceId = useReplayStore((s) => s.featuredTraceId);
  const runHref = useRunItYourselfHref();
  const on = (suffix: string) => pathname.endsWith(suffix) || pathname.includes(`${suffix}/`);
  const onTrace = Boolean(featuredTraceId) && pathname.endsWith(`/app/traffic/${featuredTraceId}`);

  return (
    <nav
      aria-label="Main"
      className="sticky top-0 flex h-dvh flex-col gap-1 border-r border-border bg-surface-sunken px-2 py-3 max-lg:items-center lg:w-52"
    >
      <a href={runHref.replace(/#.*$/, '')} className="mb-3 flex h-9 items-center gap-2 rounded-md px-2.5 text-base font-semibold text-fg max-lg:px-0">
        <img src={`${import.meta.env.BASE_URL}favicon.svg`} alt="" className="size-5" />
        <span className="max-lg:sr-only">TrustAgent</span>
      </a>
      <Item label="Overview" hint="recorded" icon={LayoutDashboard} active={on('/app/overview')}>
        {({ content, ...p }) => (
          <Link to="/app/overview" {...p}>
            {content}
          </Link>
        )}
      </Item>
      <Item label="Traffic" hint="replay" icon={Rows3} active={on('/app/traffic') && !onTrace}>
        {({ content, ...p }) => (
          <Link to="/app/traffic" {...p}>
            {content}
          </Link>
        )}
      </Item>
      {/* The first stop of the tour (D117): the answer the gate caught. */}
      <Item label="Trace" hint="caught" description={copy.recording.nav_hint_caught} icon={ScanSearch} active={onTrace}>
        {({ content, ...p }) =>
          featuredTraceId ? (
            <Link to="/app/traffic/$traceId" params={{ traceId: featuredTraceId }} {...p}>
              {content}
            </Link>
          ) : (
            <Link to="/app/traffic" {...p}>
              {content}
            </Link>
          )
        }
      </Item>
      <Item label="Playground" icon={FlaskConical} active={on('/app/playground')}>
        {({ content, ...p }) => (
          <Link to="/app/playground" {...p}>
            {content}
          </Link>
        )}
      </Item>
      <Item label="Studio" icon={PenLine} active={on('/app/evaluators/studio')}>
        {({ content, ...p }) => (
          <Link to="/app/evaluators/studio" {...p}>
            {content}
          </Link>
        )}
      </Item>
      <div className="mt-auto">
        <Item label="Run it yourself" icon={Terminal} active={false}>
          {({ content, ...p }) => (
            <a href={runHref} {...p}>
              {content}
            </a>
          )}
        </Item>
      </div>
    </nav>
  );
}
