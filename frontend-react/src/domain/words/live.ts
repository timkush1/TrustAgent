/**
 * Words for the static site's live mode ("Try Jev live": browser key, caps, OpenRouter errors,
 * PKCE callback; D99-D105, D136). Owned by WEB-LIVE. presentation.ts re-exports this module with
 * `export *`, so every export here starts with `live` (D86) and can never collide with another
 * plane's words. The words themselves come from contracts/ui copy.playground_live.*.
 */
export {};
