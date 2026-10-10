/**
 * Where the glass-box panel sends the reader: the replay's trace of the refund example (the tape's
 * featured request) on the static site, when the tape shows it.
 */
import { digest, type TapeDigestTrace } from 'virtual:trustagent/tape-digest';
import { APP_MODE, BASE, DEMO_HREF, REFUND } from './content';

/**
 * The replay's trace of the refund example (the tape's featured request) on the static site, when
 * the tape shows it; otherwise the replay's Traffic page (or the dashboard, in the full build).
 */
export function refundTraceHref(mode: typeof APP_MODE = APP_MODE, examples: readonly TapeDigestTrace[] = digest.examples): { href: string; exact: boolean } {
  if (mode !== 'static') return { href: DEMO_HREF, exact: false };
  const t = examples.find((e) => {
    const preview = (e.question_preview ?? '').replace(/…$/, '').trim();
    return preview.length > 0 && REFUND.question.startsWith(preview) && (e.action === 'flag' || e.action === 'block' || e.action === 'fallback');
  });
  return t ? { href: `${BASE}app/traffic/${t.trace_id}`, exact: true } : { href: DEMO_HREF, exact: false };
}
