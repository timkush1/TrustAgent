/**
 * The two page navigations of the Connect flow, behind one object so tests can watch them (jsdom
 * cannot navigate, and `location`'s methods cannot be spied on): Connect leaves for openrouter.ai
 * with `assign` (Back returns to the Playground), and the callback leaves with `replace` (Back never
 * returns to the spent code; D105). Owned by WEB-LIVE (LV-5).
 */
export const pageNav = {
  assign(url: string): void {
    window.location.assign(url);
  },
  replace(url: string): void {
    window.location.replace(url);
  },
};
