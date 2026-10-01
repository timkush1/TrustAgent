/**
 * The static build's top-bar status, in place of the Health pill: there is no gateway to be
 * healthy, only a recording being replayed. "Replaying · 12 of 18 requests", then "Replay finished",
 * with "Replay again" (a real action: it clears the list and plays the live part of the tape again).
 */
import { CircleAlert, CircleCheck, RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Spinner } from '@/components/ui/spinner';
import { useRefreshWhenFinished, useReplayInfo, useReplayRestart } from './useReplay';

export function ReplayStatus() {
  const info = useReplayInfo();
  const restart = useReplayRestart();
  useRefreshWhenFinished();

  let icon;
  let text: string;
  if (info.status === 'error') {
    icon = <CircleAlert aria-hidden="true" className="size-3.5 text-tone-warning" />;
    text = 'Recording unavailable';
  } else if (info.status !== 'ready') {
    icon = <Spinner size="sm" />;
    text = 'Loading the recording…';
  } else if (info.finished) {
    icon = <CircleCheck aria-hidden="true" className="size-3.5 text-fg-muted" />;
    text = `Replay finished · ${info.traces} requests`;
  } else {
    icon = <Spinner size="sm" />;
    text = `Replaying · ${info.shown} of ${info.traces} requests`;
  }

  return (
    <div className="flex items-center gap-1.5" data-replay-status={info.finished ? 'finished' : info.status}>
      <span
        className="inline-flex h-7 items-center gap-2 rounded-full border border-border bg-surface px-2.5 text-xs font-medium text-fg"
        title={info.error ?? undefined}
      >
        {icon}
        <span className="tabular max-sm:sr-only">{text}</span>
      </span>
      {/* Announced once per phase, not on every arriving request. */}
      <span className="sr-only" aria-live="polite">
        {info.status === 'ready' ? (info.finished ? 'Replay finished' : 'Replaying recorded traffic') : ''}
      </span>
      {restart ? (
        <Button variant="ghost" size="sm" onClick={restart} aria-label="Replay the recorded traffic again">
          <RotateCcw aria-hidden="true" />
          <span className="max-md:sr-only">Replay again</span>
        </Button>
      ) : null}
    </div>
  );
}
