/**
 * The static site's `/connect/callback/` (D105, D106): OpenRouter's PKCE redirect lands here with
 * `?code=`; the page takes the code out of the address bar, exchanges it with this tab's verifier
 * and returns to the Playground. Owned by WEB-LIVE (LV-5).
 *
 * Step 0b stub: no sign-in can have started in this tab yet, so it says so and links back. It never
 * renders anything from the query string.
 */
import { copy } from '@/domain/presentation';

export default function StaticConnectCallback() {
  return (
    <main className="mx-auto flex min-h-dvh max-w-lg flex-col justify-center gap-4 px-6 py-12">
      <p className="text-sm text-fg">{copy.playground_live.callback.missing_verifier}</p>
      <p>
        <a href={`${import.meta.env.BASE_URL}app/playground/`} className="text-sm font-medium text-fg underline underline-offset-2">
          Back to the Playground
        </a>
      </p>
    </main>
  );
}
