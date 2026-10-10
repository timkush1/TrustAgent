/**
 * The static site's `/connect/callback/` (D105, D106): OpenRouter's PKCE redirect lands here with
 * `?code=`. The page takes the code out of the address bar before anything else, exchanges it with
 * this tab's verifier (lib/openrouter/connect.ts runCallback) and replaces itself with the
 * Playground, so Back never returns to a spent code. It never renders anything from the query
 * string: `error` and `error_description` map to the fixed `refused` state. Inside a frame it
 * exchanges nothing and offers only "Open in its own tab".
 *
 * main.tsx boots neither the replay nor the stream on this path, so every way out is a full page
 * load (a plain link), never a router navigation into an app that was not started. Owned by
 * WEB-LIVE (LV-5).
 */
import { CircleAlert, CircleCheck, KeyRound } from 'lucide-react';
import { useEffect, useState } from 'react';
import { buttonVariants } from '@/components/ui/button-variants';
import { Spinner } from '@/components/ui/spinner';
import { copy } from '@/domain/presentation';
import { liveText } from '@/components/replay/live/liveText';
import { DEFAULT_RETURN, runCallback, type CallbackState } from '@/lib/openrouter/connect';
import { isFramed } from '@/lib/openrouter/frame';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { pageNav } from '@/lib/openrouter/navigate';

const BASE = import.meta.env.BASE_URL;
const PLAYGROUND = `${BASE}${DEFAULT_RETURN}`;
/** The Playground with the consent dialog open on Paste (routes/app/playground.tsx `paste`). */
const PASTE = `${BASE}app/playground/?paste=1#live`;

type Shown = Exclude<CallbackState, 'done'>;

function message(state: Shown): string {
  return copy.playground_live.callback[state];
}

export default function StaticConnectCallback() {
  const [state, setState] = useState<CallbackState>('working');

  useEffect(() => {
    let mounted = true;
    void runCallback({
      store: browserKeyStore(),
      search: window.location.search,
      framed: isFramed(),
      scrubUrl: () => window.history.replaceState(window.history.state, '', window.location.pathname),
      leave: (url) => pageNav.replace(url),
      base: BASE,
    }).then((next) => {
      if (mounted) setState(next);
    });
    return () => {
      mounted = false;
    };
  }, []);

  const shown: Shown = state === 'done' ? 'working' : state;
  const busy = shown === 'working';
  const ok = shown === 'already_connected';
  const Icon = busy ? null : ok ? CircleCheck : shown === 'framed' ? KeyRound : CircleAlert;

  return (
    <main className="mx-auto flex min-h-dvh max-w-lg flex-col justify-center px-4 py-12 sm:px-6">
      <div className="space-y-5 rounded-lg border border-border bg-surface p-6">
        <h1 className="text-base font-semibold text-fg">{liveText.callback_title}</h1>
        <div role="status" aria-live="polite" aria-busy={busy} className="flex items-start gap-3">
          {busy ? <Spinner size="sm" className="mt-0.5" /> : Icon ? <Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" /> : null}
          <p className="text-sm text-fg" data-testid="callback-state" data-state={shown}>
            {message(shown)}
          </p>
        </div>
        {busy ? null : shown === 'framed' ? (
          <a href={PLAYGROUND} target="_blank" rel="noopener noreferrer" className={buttonVariants({ variant: 'primary' })}>
            {copy.playground_live.open_in_tab}
          </a>
        ) : (
          <div className="flex flex-wrap gap-2">
            {ok ? null : (
              <a href={PASTE} className={buttonVariants({ variant: 'primary' })}>
                {copy.playground_live.paste_key}
              </a>
            )}
            <a href={PLAYGROUND} className={buttonVariants({ variant: ok ? 'primary' : 'secondary' })}>
              {liveText.back_to_playground}
            </a>
          </div>
        )}
      </div>
    </main>
  );
}
