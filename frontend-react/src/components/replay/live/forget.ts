/**
 * Forget key (D102), from the panel or the top bar's chip: aborts every request in flight, clears
 * the key, the verifier and the return path (usage stays), then says where to delete the key for
 * good. Owned by WEB-LIVE (LV-6).
 */
import { notify } from '@/components/common/notify';
import { copy } from '@/domain/presentation';
import { liveWords } from '@/domain/words/live';
import { browserKeyStore, type KeyStore } from '@/lib/openrouter/keyStore';

export function forgetKey(store: KeyStore = browserKeyStore()): void {
  store.forget();
  notify.success(liveWords.forgotten, { description: copy.playground_live.forget_note });
}
