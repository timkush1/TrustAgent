/**
 * The fix beside a static Studio action that needs the tab's OpenRouter key (D107, D108): a link-
 * styled button that opens the consent dialog (four lines, the cost chip, Connect or Paste a key),
 * which returns to Studio after a PKCE sign-in. The dialog loads only when asked for, so nothing of
 * the live feature joins Studio's chunk until the viewer wants it.
 */
import { lazy, Suspense, useState } from 'react';
import { copy } from '@/domain/presentation';

const ConsentDialog = lazy(() => import('@/components/replay/live/ConsentDialog').then((m) => ({ default: m.ConsentDialog })));

/** Where the PKCE callback brings the viewer back to (router-relative, D105). */
export const STUDIO_RETURN_TO = 'app/evaluators/studio/';

export function ConnectKeyFix() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className="font-medium text-fg underline underline-offset-2" onClick={() => setOpen(true)}>
        {copy.playground_live.connect}
      </button>
      {open ? (
        <Suspense fallback={null}>
          <ConsentDialog open onClose={() => setOpen(false)} returnTo={STUDIO_RETURN_TO} />
        </Suspense>
      ) : null}
    </>
  );
}
