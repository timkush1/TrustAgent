import { useEffect, useRef, useState } from 'react';

/**
 * Page contract loading timing (SPEC §3.3): show a loading state only after `delayMs` (200 ms),
 * and once shown keep it for at least `minMs` (400 ms), so fast loads never flash.
 */
export function useDelayedLoading(loading: boolean, { delayMs = 200, minMs = 400 } = {}): boolean {
  const [visible, setVisible] = useState(false);
  const shownAt = useRef(0);

  useEffect(() => {
    if (loading && !visible) {
      const t = setTimeout(() => {
        shownAt.current = Date.now();
        setVisible(true);
      }, delayMs);
      return () => clearTimeout(t);
    }
    if (!loading && visible) {
      const t = setTimeout(() => setVisible(false), Math.max(0, minMs - (Date.now() - shownAt.current)));
      return () => clearTimeout(t);
    }
    return undefined;
  }, [loading, visible, delayMs, minMs]);

  return visible;
}
