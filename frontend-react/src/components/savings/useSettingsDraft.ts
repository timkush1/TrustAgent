/**
 * A form over one part of SettingsView (the daily caps, the price overrides) that remembers which
 * version it was based on, so If-Match means what it says (SPEC §2.4 #53; P4-PLAN §13 SV-3):
 *
 * - a save sends the ETag of the version the draft was based on, never simply the latest one
 *   in the cache, so it cannot silently overwrite a change made elsewhere;
 * - a newer version that leaves this form's part alone, or changes only what the viewer has not
 *   edited, is taken in quietly (the draft follows it, and the next save uses its ETag);
 * - a newer version that changes what the viewer has edited is a conflict: the draft stays as
 *   typed with the old ETag, so the save meets the server's `stale_version`, and Reload starts
 *   over from the latest version.
 */
import { useState } from 'react';
import type { SettingsView } from '@/types/api';

export interface SettingsDraftOptions<Base, Draft> {
  /** This form's part of the settings. */
  read: (settings: SettingsView) => Base;
  /** A fresh, unedited draft of it. */
  toDraft: (base: Base) => Draft;
  /**
   * The draft carried over to a newer version `next` of what it was based on (`base`): the
   * viewer's edits kept and the rest following `next`, or null when `next` changed something
   * the viewer edited.
   */
  rebase: (draft: Draft, base: Base, next: Base) => Draft | null;
}

interface DraftState<Base, Draft> {
  /** The newest settings version this form has looked at. */
  seen: number;
  /** The If-Match of a save: the ETag of the version `base` comes from. */
  etag: string;
  base: Base;
  draft: Draft;
}

export interface SettingsDraft<Base, Draft> {
  draft: Draft;
  /** What the draft was based on (the saved values it is compared with). */
  base: Base;
  /** The If-Match a save sends. */
  etag: string;
  setDraft: (update: (draft: Draft) => Draft) => void;
  /** Starts over from these settings: a save's answer, Discard, or Reload. */
  reset: (settings: SettingsView) => void;
}

export function useSettingsDraft<Base, Draft>(settings: SettingsView, options: SettingsDraftOptions<Base, Draft>): SettingsDraft<Base, Draft> {
  const fresh = (s: SettingsView): DraftState<Base, Draft> => {
    const base = options.read(s);
    return { seen: s.version, etag: s.etag, base, draft: options.toDraft(base) };
  };
  const [state, setState] = useState(() => fresh(settings));
  let current = state;
  if (settings.version !== state.seen) {
    // A newer version arrived (another tab, a live update, the other form on this page saving).
    const draft = options.rebase(state.draft, state.base, options.read(settings));
    current =
      draft === null
        ? { ...state, seen: settings.version }
        : { seen: settings.version, etag: settings.etag, base: options.read(settings), draft };
    setState(current);
  }
  return {
    draft: current.draft,
    base: current.base,
    etag: current.etag,
    setDraft: (update) => setState((s) => ({ ...s, draft: update(s.draft) })),
    reset: (s) => setState(fresh(s)),
  };
}
