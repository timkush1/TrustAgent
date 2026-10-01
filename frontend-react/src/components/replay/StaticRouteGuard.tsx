/**
 * The static build hides the pages that need a running gateway (SPEC §3.3 "Static build": Policies,
 * Settings and Knowledge; also the evaluator library and the setup wizard, which read the gateway's
 * control plane). A deep link to one of them lands here instead of an error with a Retry that could
 * never work: what the page is for, why this site cannot show it, and where to go instead.
 */
import { Link, useRouterState } from '@tanstack/react-router';
import { ArrowRight, Terminal } from 'lucide-react';
import type { ReactNode } from 'react';
import { CodeBlock } from '@/components/common/CodeBlock';
import { commandText } from '@/domain/presentation';
import { hiddenStaticPage, type HiddenPage } from './staticPages';
import { useIsStatic, useRunItYourselfHref } from './useReplay';

function StaticOnlyNotice({ page }: { page: HiddenPage }) {
  const runHref = useRunItYourselfHref();
  const demo = commandText('demo') ?? 'make demo';
  return (
    <section aria-labelledby="static-hidden-title" className="max-w-2xl space-y-5">
      <div className="space-y-2">
        <span className="inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted">
          <Terminal aria-hidden="true" className="size-3.5" />
          Runs on your own stack
        </span>
        <h2 id="static-hidden-title" className="text-xl font-semibold text-fg">
          {page.name} is not part of the static demo
        </h2>
        <p className="text-sm text-fg-muted">
          On a running stack, {page.name} is where you {page.does}. This site replays a recording in your browser, so there is
          no gateway here to read or change.
        </p>
      </div>
      <div className="space-y-2 rounded-lg border border-border bg-surface p-4">
        <p className="text-sm text-fg">Run the whole dashboard locally. Docker only, no keys, $0:</p>
        <CodeBlock code={demo} label="Start the demo stack" />
        <a href={runHref} className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
          More ways to run it
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </a>
      </div>
      <p className="text-sm text-fg-muted">
        Meanwhile, the recorded requests are in{' '}
        <Link to="/app/traffic" className="inline-flex items-center gap-0.5 font-medium text-fg underline underline-offset-2">
          Traffic
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      </p>
    </section>
  );
}

/** Renders `children` unless this is the static build and the path is a hidden page. */
export function StaticRouteGuard({ children }: { children: ReactNode }) {
  const isStatic = useIsStatic();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const page = isStatic ? hiddenStaticPage(pathname) : null;
  return page ? <StaticOnlyNotice page={page} /> : <>{children}</>;
}
