/*
 * Runs synchronously in <head> before first paint (no flash). A same-origin file rather than an
 * inline script, so the CSP can stay `script-src 'self'`.
 *
 * Keep in sync with src/lib/theme.ts: storage key, values and attributes.
 *   localStorage["trustagent.theme"] = "light" | "dark" | "system" (default "system")
 *   <html data-theme="light|dark" data-theme-pref="light|dark|system">
 */
(function () {
  var pref = 'system';
  try {
    var stored = window.localStorage.getItem('trustagent.theme');
    if (stored === 'light' || stored === 'dark' || stored === 'system') pref = stored;
  } catch (e) {
    /* Storage can be blocked (private mode, sandboxed frames); fall back to the OS. */
  }
  var dark =
    pref === 'dark' ||
    (pref === 'system' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-color-scheme: dark)').matches);
  var root = document.documentElement;
  root.setAttribute('data-theme', dark ? 'dark' : 'light');
  root.setAttribute('data-theme-pref', pref);
})();
