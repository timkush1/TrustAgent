import { beforeEach, describe, expect, it } from 'vitest';
import { initTheme, nextThemePreference, resolveTheme, setThemePreference, THEME_STORAGE_KEY } from './theme';

describe('theme', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });

  it('resolves system against the OS preference', () => {
    expect(resolveTheme('system', true)).toBe('dark');
    expect(resolveTheme('system', false)).toBe('light');
    expect(resolveTheme('light', true)).toBe('light');
  });

  it('cycles light → dark → system', () => {
    expect(nextThemePreference('light')).toBe('dark');
    expect(nextThemePreference('dark')).toBe('system');
    expect(nextThemePreference('system')).toBe('light');
  });

  it('persists the choice and stamps data-theme like theme-init.js', () => {
    setThemePreference('dark');
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(document.documentElement.dataset.themePref).toBe('dark');
  });

  it('re-applies the stored choice on init', () => {
    localStorage.setItem(THEME_STORAGE_KEY, 'light');
    const stop = initTheme();
    expect(document.documentElement.dataset.theme).toBe('light');
    stop();
  });
});
