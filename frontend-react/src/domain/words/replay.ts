/**
 * Words for the static replay (the simulated or recorded tape, the guide, the glass box on
 * replayed traces, the first visit; D109, D110, D117). Owned by WEB-REPLAY. presentation.ts
 * re-exports this module with `export *`, so every export here starts with `replay` (D86) and can
 * never collide with another plane's words. The words come from contracts/ui copy.recording.*.
 */
import type { DecisionRequestView, Recording } from '@/types/api';
import { copy } from '../contracts';

/**
 * The glass box's badge on a replayed trace (D109). On the simulated tape a match proves nothing
 * (the tape was built by the same compile()), so it says only that it was rebuilt; a recorded
 * tape says whether the rebuild matches the request that was sent.
 */
export function replayGlassboxBadge(kind: Recording['kind'], matches: boolean): string {
  if (kind === 'mock') return copy.recording.glassbox.rebuilt_simulated;
  return matches ? copy.recording.glassbox.matches : copy.recording.glassbox.differs;
}

/** Why a rebuilt request differs from the recorded one; null when the body itself differs (scrubbed). */
export function replayMismatchText(reason: DecisionRequestView['mismatch_reason']): string | null {
  if (reason === null) return null;
  const words: Record<string, string> = copy.recording.glassbox.mismatch;
  return Object.hasOwn(words, reason) ? words[reason] : null;
}
