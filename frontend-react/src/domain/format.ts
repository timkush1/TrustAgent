/**
 * Number, money, duration and time formatting. Numbers render with `tabular` (index.css); these
 * helpers only decide the characters. Rounding follows SPEC §2.1: never Math.round() on display
 * values, r2(x) = floor(x·100 + 0.5) / 100.
 */

const EM_DASH = '—';

/** SPEC §2.1 two-decimal rounding. */
export function r2(x: number): number {
  return Math.floor(x * 100 + 0.5) / 100;
}

/** Risk in [0,1] as "0.98"; null (not assessed) as an em dash. */
export function formatRisk(risk: number | null | undefined): string {
  if (risk === null || risk === undefined || !Number.isFinite(risk)) return EM_DASH;
  return r2(risk).toFixed(2);
}

const integer = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 });

export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return EM_DASH;
  return integer.format(n);
}

/** Durations: "212 ms", "1.4 s", "2 min 5 s". */
export function formatDurationMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return EM_DASH;
  if (ms < 1000) return `${integer.format(Math.floor(ms + 0.5))} ms`;
  if (ms < 60_000) {
    const s = Math.floor(ms / 100 + 0.5) / 10;
    return `${s.toFixed(s < 10 ? 1 : 0)} s`;
  }
  const totalS = Math.floor(ms / 1000 + 0.5);
  const m = Math.floor(totalS / 60);
  const s = totalS % 60;
  return s === 0 ? `${m} min` : `${m} min ${s} s`;
}

const money = new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** Money in USD: cents from $0.01 up, two significant digits below that ($0.00001). */
export function formatUsd(usd: number | null | undefined): string {
  if (usd === null || usd === undefined || !Number.isFinite(usd)) return EM_DASH;
  if (usd === 0) return '$0';
  const sign = usd < 0 ? '-' : '';
  const abs = Math.abs(usd);
  if (abs >= 0.01) return `${sign}$${money.format(abs)}`;
  const digits = Math.min(10, 1 - Math.floor(Math.log10(abs)));
  return `${sign}$${abs.toFixed(digits).replace(/0+$/, '')}`;
}

const relative = new Intl.RelativeTimeFormat('en', { numeric: 'auto', style: 'short' });
const absolute = new Intl.DateTimeFormat('en-GB', {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  timeZoneName: 'short',
});
const sameDay = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const otherDay = new Intl.DateTimeFormat('en-GB', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });

/** Full timestamp for tooltips and details. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return EM_DASH;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? EM_DASH : absolute.format(t);
}

/**
 * "12 s ago" / "5 min ago" within the hour, the clock time within 24 h, the date after that.
 */
export function formatRelativeTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return EM_DASH;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return EM_DASH;
  const diffS = Math.floor((t - now) / 1000);
  const abs = Math.abs(diffS);
  if (abs < 5) return 'just now';
  if (abs < 60) return relative.format(diffS, 'second');
  if (abs < 3600) return relative.format(Math.trunc(diffS / 60), 'minute');
  if (abs < 86_400) return sameDay.format(t);
  return otherDay.format(t);
}
