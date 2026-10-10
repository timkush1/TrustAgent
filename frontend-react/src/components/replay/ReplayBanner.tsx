/**
 * The static build's one banner (SPEC §3.3 "at most one banner"; §0 rows 4-5; D117): what the
 * reader is looking at, a replay, in the contract's words. A simulated tape says "simulated" once
 * (copy.recording.banner_simulated: the replayed traffic and examples use mock checks and a mock judge, not
 * model output); a recorded one says when and with which models it was recorded, and that it is
 * not live (copy.recording.banner_recorded). Both point to running it yourself.
 */
import { CircleAlert, History } from 'lucide-react';
import { commandText, copy, fillTemplate } from '@/domain/presentation';
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
  if (info.status !== 'ready' || !info.kind) return null;

  const words =
    info.kind === 'mock'
      ? copy.recording.banner_simulated
      : fillTemplate(copy.recording.banner_recorded, {
          date: recordingDate(info.recordedAt),
          snapshot: info.snapshot ?? 'an unnamed snapshot',
          judge: info.judge ?? 'an unnamed judge',
        });
  return (
    <div role="note" data-banner="replay" data-recording-kind={info.kind} className="flex items-start gap-2 border-b border-border bg-surface-sunken px-6 py-2 text-sm max-sm:px-4">
      <History aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
      <p className="min-w-0">
        <span className="font-medium" data-banner-words>
          {words}
        </span>{' '}
        <span className="text-fg-muted">
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
