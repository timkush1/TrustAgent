/**
 * Hands the viewer a file made in the page: a Blob behind a temporary object URL, clicked through an
 * `<a download>` that never joins the visible page, then released. Nothing leaves the browser (no
 * request is made), so it works on the static site under its CSP. Resolves to whether the download
 * was started; the caller tells the viewer either way, as with copyText (clipboard.ts).
 */
export function downloadText(fileName: string, text: string, type = 'application/json'): boolean {
  if (typeof document === 'undefined' || typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function') return false;
  let url: string | null = null;
  const link = document.createElement('a');
  try {
    url = URL.createObjectURL(new Blob([text], { type: `${type};charset=utf-8` }));
    link.href = url;
    link.download = fileName;
    link.rel = 'noopener';
    link.style.display = 'none';
    document.body.appendChild(link);
    link.click();
    return true;
  } catch {
    return false;
  } finally {
    link.remove();
    // Firefox needs the URL a moment after the click; nothing else holds it.
    if (url !== null) {
      const done = url;
      setTimeout(() => URL.revokeObjectURL(done), 0);
    }
  }
}

