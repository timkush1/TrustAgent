/**
 * Recharts styling in the design tokens. Axes and grid are recessive: solid 1px hairlines one step
 * off the surface, tick text in the muted text token at 11px. Marks follow the dataviz specs: bars
 * at most 24px thick, a 4px rounded data end and a square baseline, 2px surface gaps between
 * touching fills.
 */

export const axisTick = { fill: 'var(--fg-muted)', fontSize: 11 } as const;
export const gridStroke = 'var(--border)';

/** Surface gap between stacked segments, in px. */
export const SEGMENT_GAP = 2;
/** Radius of a bar's data end, in px. */
export const BAR_RADIUS = 4;

export interface SegmentGeometry {
  x?: number;
  y?: number;
  width?: number;
  height?: number;
  payload?: unknown;
}

function roundedTop(x: number, y: number, w: number, h: number, r: number): string {
  return [
    `M${x},${y + h}`,
    `L${x},${y + r}`,
    `Q${x},${y} ${x + r},${y}`,
    `L${x + w - r},${y}`,
    `Q${x + w},${y} ${x + w},${y + r}`,
    `L${x + w},${y + h}`,
    'Z',
  ].join(' ');
}

function plain(x: number, y: number, w: number, h: number): string {
  return `M${x},${y} H${x + w} V${y + h} H${x} Z`;
}

/**
 * The path of one segment of a stacked column. The topmost non-empty segment of the column gets
 * the rounded data end; every segment with another one above it gives up its top 2px to the
 * surface gap (a sliver keeps at least 1px so a small count is still visible).
 */
export function stackedSegmentPath(geom: SegmentGeometry, seriesKey: string, stackOrder: readonly string[]): string {
  const { x = 0, y = 0, width = 0, height = 0 } = geom;
  if (!(width > 0) || !(height > 0)) return '';
  const values = (geom.payload ?? {}) as Record<string, unknown>;
  const above = stackOrder.slice(stackOrder.indexOf(seriesKey) + 1).some((k) => Number(values[k] ?? 0) > 0);
  const h = above ? Math.max(height - SEGMENT_GAP, Math.min(height, 1)) : height;
  const top = y + (height - h);
  if (above) return plain(x, top, width, h);
  return roundedTop(x, top, width, h, Math.min(BAR_RADIUS, h, width / 2));
}
