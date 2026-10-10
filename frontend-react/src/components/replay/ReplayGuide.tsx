/**
 * The static demo's tour (D117): four steps that show what TrustAgent does in about two minutes,
 * each a link. (1) the answer it caught, the row Traffic marks `data-featured`; (2) "60 days" in
 * the answer against "30 days" in the policy the bot was given; (3) "Show exact Jev request", which
 * opens on arrival; (4) Savings.
 *
 * It sits in the page, above the list or the request, never over it: at 400 px it cannot cover
 * the featured row. It opens on a first visit and stays hidden once hidden (guideState.ts). The
 * words are copy.recording.guide.*.
 */
import { Link } from '@tanstack/react-router';
import { Compass, X } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { TRACE_ANCHORS } from '@/components/trace/traceView';
import { Button } from '@/components/ui/button';
import { useReplayStore } from '@/data/replay/replayStore';
import { copy } from '@/domain/presentation';
import { useGuideOpen, useGuideState } from './guideState';

const linkClass = 'font-medium text-fg underline underline-offset-2 hover:decoration-2';

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex items-start gap-2" data-guide-step={n}>
      <span aria-hidden="true" className="mt-px inline-flex size-5 shrink-0 items-center justify-center rounded-full border border-border-strong text-2xs font-medium text-fg-muted tabular">
        {n}
      </span>
      <span className="min-w-0">{children}</span>
    </li>
  );
}

export function ReplayGuide() {
  const open = useGuideOpen();
  const featured = useReplayStore((s) => s.featuredTraceId);
  const hide = useGuideState((s) => s.hide);
  const titleId = useId();
  if (!open || featured === null) return null;
  const trace = { to: '/app/traffic/$traceId', params: { traceId: featured } } as const;
  const g = copy.recording.guide;
  return (
    <section aria-labelledby={titleId} data-replay-guide className="space-y-2 rounded-lg border border-border bg-surface px-4 py-3">
      <div className="flex items-start justify-between gap-2">
        <h2 id={titleId} className="flex items-center gap-1.5 text-sm font-semibold text-fg">
          <Compass aria-hidden="true" className="size-4 text-fg-muted" />
          {g.title}
        </h2>
        <Button size="sm" variant="ghost" onClick={hide} className="-mt-1 -mr-2">
          <X aria-hidden="true" />
          {g.dismiss}
        </Button>
      </div>
      <ol className="grid gap-2 text-sm text-fg sm:grid-cols-2 xl:grid-cols-4">
        <Step n={1}>
          <Link {...trace} data-guide-anchor={featured} className={linkClass}>
            {g.step1}
          </Link>
        </Step>
        <Step n={2}>
          <Link {...trace} hash={TRACE_ANCHORS.answer} className={linkClass}>
            {g.step2}
          </Link>
        </Step>
        <Step n={3}>
          <Link {...trace} hash={TRACE_ANCHORS.jevRequest} className={linkClass}>
            {g.step3}
          </Link>
        </Step>
        <Step n={4}>
          <Link to="/app/overview" search={{ tab: 'savings' }} className={linkClass}>
            {g.step4}
          </Link>
        </Step>
      </ol>
    </section>
  );
}
