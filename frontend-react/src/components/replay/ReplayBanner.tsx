/**
 * The static build's one banner (SPEC §3.3 "at most one banner"; §0 rows 4-5): what the reader is
 * looking at, a replay. A mock recording says it is simulated in the contract's own words
 * (copy.recording.mock) and when it was recorded; a real one shows its "Recorded … · replayed, not
 * live" label. Both point to running it yourself.
 */
import { CircleAlert, History } from 'lucide-react';
import { commandText } from '@/domain/presentation';
import { recordingDate, useReplayInfo, useRunItYourselfHref } from './useReplay';

export function ReplayBanner() {
  const info = useReplayInfo();
  const href = useRunItYourselfHref();
  const demo = commandText('demo');

  if (info.status === 'error') {
    return (
      <div role="alert" data-banner="replay" className="flex items-start gap-2 border-b border-border bg-surface-sunken px-6 py-2 text-sm max-sm:px-4">
        <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
        <p className="min-w-0">
          <span className="font-medium">The demo recording could not be loaded.</span>{' '}
          <span className="text-fg-muted">{info.error}</span>
        </p>
      </div>
    );
  }
  if (info.status !== 'ready' || !info.label) return null;

  const simulated = info.kind === 'mock';
  return (
    <div role="note" data-banner="replay" className="flex items-start gap-2 border-b border-border bg-surface-sunken px-6 py-2 text-sm max-sm:px-4">
      <History aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
      <p className="min-w-0">
        <span className="font-medium">Replay of recorded traffic.</span>{' '}
        <span className="text-fg-muted">
          <span data-recording-label>{info.label}</span>
          {simulated ? ` · recorded ${recordingDate(info.recordedAt)} on the zero-key demo stack` : ''}.{' '}
          <a href={href} className="font-medium text-fg underline underline-offset-2">
            Run it yourself
          </a>
          {demo ? (
            <>
              {' '}
              with <code className="rounded-sm bg-surface px-1 py-0.5 font-mono text-xs whitespace-nowrap text-fg">{demo}</code>
            </>
          ) : null}
          .
        </span>
      </p>
    </div>
  );
}
