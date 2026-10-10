/**
 * The 402 screen (D100, D101): the account is out of credits. It links to openrouter.ai's credits
 * page (a new tab, D102) and offers "Check again", which asks the free key check (never counted)
 * whether the key has credit to spend now. A 402 is never counted against the caps. An in-flight
 * 402 (the key's own budget, with Retry-After) is not this screen: the panel says to wait and retry.
 * Owned by WEB-LIVE (LV-6).
 */
import { CreditCard } from 'lucide-react';
import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { copy } from '@/domain/presentation';
import { liveText } from './liveText';
import { OPENROUTER_PAGES } from '@/lib/openrouter/endpoints';
import { getKeyInfo } from '@/lib/openrouter/keyInfo';
import { errorKind } from '@/lib/openrouter/errors';
import type { KeyStore } from '@/lib/openrouter/keyStore';
import { ExternalLink } from './ExternalLink';

export interface NoCreditsScreenProps {
  store: KeyStore;
  /** The key may spend again (or was forgotten): back to the panel. */
  onRecovered: () => void;
}

export function NoCreditsScreen({ store, onRecovered }: NoCreditsScreenProps) {
  const [checking, setChecking] = useState(false);
  const [still, setStill] = useState(false);

  async function checkAgain() {
    const key = store.currentKey();
    if (key === null) {
      onRecovered();
      return;
    }
    setChecking(true);
    setStill(false);
    try {
      const info = await getKeyInfo(key, { track: store.track });
      if (typeof info.limit_remaining === 'number' && info.limit_remaining <= 0) setStill(true);
      else onRecovered();
    } catch (err) {
      // A key OpenRouter refuses is forgotten (D100); the panel then asks for a new one.
      if (errorKind(err) === 'auth') {
        store.forget();
        onRecovered();
      } else setStill(true);
    } finally {
      setChecking(false);
    }
  }

  return (
    <div role="alert" className="space-y-3 rounded-lg border border-border-strong bg-surface p-4" data-testid="live-no-credits">
      <p className="flex items-center gap-2 text-sm font-semibold text-fg">
        <CreditCard aria-hidden="true" className="size-4" />
        {liveText.no_credits_title}
      </p>
      <p className="text-sm text-fg">{copy.playground_live.errors.no_credits}</p>
      <div className="flex flex-wrap items-center gap-3">
        <ExternalLink href={OPENROUTER_PAGES.credits} className="text-sm">
          {copy.playground_live.actions.open_credits}
        </ExternalLink>
        <Button type="button" variant="secondary" size="sm" onClick={() => void checkAgain()} pending={checking}>
          {copy.playground_live.check_again}
        </Button>
      </div>
      <p aria-live="polite" className="text-xs text-fg-muted">
        {checking ? liveText.no_credits_checking : still ? liveText.no_credits_still : ''}
      </p>
    </div>
  );
}
