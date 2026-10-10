/**
 * Frame-busting for the key surfaces (D106): the consent dialog, PasteKey, the live panel and the
 * callback render only "Open in its own tab" inside a frame, with no key input and no exchange. A
 * meta CSP cannot set `frame-ancestors`, and GitHub Pages sends no `X-Frame-Options`, so the page
 * checks for itself. Owned by WEB-LIVE (LV-5).
 */

/** True when this page runs inside a frame (or the check itself is refused, which only a frame does). */
export function isFramed(win: Pick<Window, 'top' | 'self'> | undefined = typeof window === 'undefined' ? undefined : window): boolean {
  if (win === undefined) return false;
  try {
    return win.top !== win.self;
  } catch {
    return true;
  }
}
