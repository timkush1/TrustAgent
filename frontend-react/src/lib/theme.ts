/**
 * Theme preference: light, dark or system (follows prefers-color-scheme).
 *
 * public/theme-init.js applies the stored preference before first paint; this module owns it
 * afterwards. Keep the storage key, values and attributes in sync with that script.
 */
import { useSyncExternalStore } from 'react';

export type ThemePreference = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

export const THEME_STORAGE_KEY = 'trustagent.theme';
const DARK_QUERY = '(prefers-color-scheme: dark)';

const listeners = new Set<() => void>();
let preference: ThemePreference = readStoredPreference();

function readStoredPreference(): ThemePreference {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
    if (stored === 'light' || stored === 'dark' || stored === 'system') return stored;
  } catch {
    // Storage blocked: fall through to the OS preference.
  }
  return 'system';
}

function systemPrefersDark(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia(DARK_QUERY).matches;
}

export function resolveTheme(pref: ThemePreference, prefersDark: boolean): ResolvedTheme {
  if (pref === 'system') return prefersDark ? 'dark' : 'light';
  return pref;
}

function apply(): void {
  const root = document.documentElement;
  root.setAttribute('data-theme', resolveTheme(preference, systemPrefersDark()));
  root.setAttribute('data-theme-pref', preference);
  listeners.forEach((l) => l());
}

export function getThemePreference(): ThemePreference {
  return preference;
}

export function setThemePreference(next: ThemePreference): void {
  preference = next;
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, next);
  } catch {
    // Not persisted; still applied for this page view.
  }
  apply();
}

/** Cycles light → dark → system → light (the top-bar toggle). */
export function nextThemePreference(pref: ThemePreference): ThemePreference {
  return pref === 'light' ? 'dark' : pref === 'dark' ? 'system' : 'light';
}

/**
 * Starts following OS changes while the preference is "system", and re-applies the stored
 * preference (theme-init.js may be missing in tests). Returns a cleanup function.
 */
export function initTheme(): () => void {
  preference = readStoredPreference();
  apply();
  if (typeof window.matchMedia !== 'function') return () => {};
  const mql = window.matchMedia(DARK_QUERY);
  const onChange = () => {
    if (preference === 'system') apply();
  };
  mql.addEventListener('change', onChange);
  return () => mql.removeEventListener('change', onChange);
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useThemePreference(): ThemePreference {
  return useSyncExternalStore(subscribe, getThemePreference, getThemePreference);
}
