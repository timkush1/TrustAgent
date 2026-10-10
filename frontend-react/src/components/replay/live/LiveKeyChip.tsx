/**
 * The top bar's chip for the tab's OpenRouter key (static build only, D102): "Your key" with a
 * Forget key button, shown only while a key is in this tab. TopBar loads it lazily, so nothing of
 * the live feature is in the app entry (D112). Forget aborts in-flight calls and clears the key,
 * the verifier and the return path; the usage counter stays. Owned by WEB-LIVE (LV-6).
 */
import { KeyRound, X } from 'lucide-react';
import { copy } from '@/domain/presentation';
import { liveWords } from '@/domain/words/live';
import { useBrowserKey } from '@/lib/openrouter/useBrowserKey';
import { forgetKey } from './forget';

export default function LiveKeyChip() {
  const { key, state } = useBrowserKey();
  if (key === null) return null;
  const title = state === 'memory' ? liveWords.key_in_memory : liveWords.key_in_tab;
  return (
    <span
      className="inline-flex h-7 items-center gap-1 rounded-full border border-border bg-surface pr-0.5 pl-2 text-xs font-medium text-fg"
      title={title}
      data-testid="live-key-chip"
    >
      <KeyRound aria-hidden="true" className="size-3.5 text-fg-muted" />
      <span className="max-sm:sr-only">{liveWords.key_chip}</span>
      <span className="sr-only">: {title}</span>
      <button
        type="button"
        onClick={() => forgetKey()}
        aria-label={copy.playground_static.forget_key}
        className="inline-flex size-6 items-center justify-center rounded-full text-fg-muted outline-offset-2 hover:bg-surface-hover hover:text-fg"
      >
        <X aria-hidden="true" className="size-3.5" />
      </button>
    </span>
  );
}
