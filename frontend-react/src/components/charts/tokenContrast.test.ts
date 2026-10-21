/**
 * Computes the contrast of every token pair the UI kit and the charts put together, in both
 * themes, straight from src/styles/tokens.css, which src/index.css and the landing page import
 * (jsdom cannot compute colors, so axe's color-contrast rule is off in component tests). Text
 * needs 4.5:1 (WCAG 1.4.3); control boundaries, focus rings and chart marks need 3:1 (WCAG 1.4.11).
 *
 * Colors are converted OKLCH → OKLab → linear sRGB (Björn Ottosson's matrices), clamped to the
 * sRGB gamut. `color-mix(in oklab, a p%, b)` mixes in OKLab; a color at partial alpha
 * (`color-mix(… X p%, transparent)`, Tailwind's `/80`) is composited over its background in
 * gamma-encoded sRGB, as browsers do.
 */
import { describe, expect, it } from 'vitest';
import { LIGHT_SHADE_PERCENT } from './palette';

// Vitest hands CSS imports (even ?raw) to tests as empty strings, so read the stylesheet from
// disk. The specifiers are variables because the app's tsconfig has no Node types.
const [fsSpecifier, urlSpecifier] = ['node:fs', 'node:url'];
const { readFileSync } = (await import(/* @vite-ignore */ fsSpecifier)) as {
  readFileSync: (path: string, encoding: 'utf8') => string;
};
const { fileURLToPath } = (await import(/* @vite-ignore */ urlSpecifier)) as { fileURLToPath: (url: string) => string };
// Through a variable: Vite rewrites `new URL('…', import.meta.url)` into an asset URL.
// fileURLToPath, not `.pathname`: on Windows the pathname is "/C:/…", which is not a file path.
const here = import.meta.url;
const css = readFileSync(fileURLToPath(new URL('../../styles/tokens.css', here).href), 'utf8');

type Lab = [number, number, number];
type Rgb = [number, number, number]; // gamma-encoded sRGB, 0..1
type Theme = 'light' | 'dark';

function oklchToLab(l: number, c: number, h: number): Lab {
  const rad = (h * Math.PI) / 180;
  return [l, c * Math.cos(rad), c * Math.sin(rad)];
}

function labToRgb([L, a, b]: Lab): Rgb {
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const linear = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  return linear.map((x) => {
    const v = Math.min(1, Math.max(0, x));
    return v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055;
  }) as Rgb;
}

function luminance(rgb: Rgb): number {
  const [r, g, b] = rgb.map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: Rgb, b: Rgb): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// ------------------------------------------------------------------------------ tokens from CSS

const rootBlock = css.slice(css.indexOf(':root {'), css.indexOf('}', css.indexOf(':root {')));
const tokens = new Map<string, Record<Theme, Lab>>();
for (const m of rootBlock.matchAll(/--([a-z-]+):\s*light-dark\(\s*oklch\(([\d.]+) ([\d.]+) ([\d.]+)\),\s*oklch\(([\d.]+) ([\d.]+) ([\d.]+)\)\s*\)/g)) {
  const n = m.slice(2).map(Number);
  tokens.set(m[1], { light: oklchToLab(n[0], n[1], n[2]), dark: oklchToLab(n[3], n[4], n[5]) });
}
for (const m of rootBlock.matchAll(/--([a-z-]+):\s*var\(--([a-z-]+)\);/g)) {
  const target = tokens.get(m[2]);
  if (target) tokens.set(m[1], target);
}
const tint: Record<Theme, number> = { light: 0.1, dark: 0.16 };

function lab(name: string, theme: Theme): Lab {
  const t = tokens.get(name);
  if (!t) throw new Error(`tokens.css has no token --${name}`);
  return t[theme];
}
const rgb = (name: string, theme: Theme) => labToRgb(lab(name, theme));

/** color-mix(in oklab, a p, b). */
function mix(a: string, p: number, b: string, theme: Theme): Rgb {
  const [x, y] = [lab(a, theme), lab(b, theme)];
  return labToRgb([0, 1, 2].map((i) => x[i] * p + y[i] * (1 - p)) as Lab);
}

/** `a` at alpha p composited over `b`. */
function over(a: string, p: number, b: string, theme: Theme): Rgb {
  const [x, y] = [rgb(a, theme), rgb(b, theme)];
  return [0, 1, 2].map((i) => x[i] * p + y[i] * (1 - p)) as Rgb;
}

// ------------------------------------------------------------------------------ the pairs

const TONES = ['tone-success', 'tone-danger', 'tone-warning', 'tone-info', 'tone-neutral'];
const VERDICTS = ['v-supported', 'v-contradicted', 'v-unsupported', 'v-partial', 'v-unknown', 'v-not-checked'];
const SURFACES = ['bg', 'surface', 'surface-raised'];

interface Pair {
  what: string;
  min: number;
  fg: (t: Theme) => Rgb;
  bg: (t: Theme) => Rgb;
}

const pairs: Pair[] = [];
const add = (what: string, min: number, fg: (t: Theme) => Rgb, bg: (t: Theme) => Rgb) => pairs.push({ what, min, fg, bg });

// Text.
for (const surface of [...SURFACES, 'surface-sunken', 'surface-hover']) {
  for (const text of ['fg', 'fg-muted']) add(`${text} text on ${surface}`, 4.5, (t) => rgb(text, t), (t) => rgb(surface, t));
}
for (const surface of SURFACES) {
  add(`fg-subtle text (placeholders, hints, missing values) on ${surface}`, 4.5, (t) => rgb('fg-subtle', t), (t) => rgb(surface, t));
}
add('fg-inverse on fg (primary button, tooltip, checked controls)', 4.5, (t) => rgb('fg-inverse', t), (t) => rgb('fg', t));
for (const tone of TONES) {
  add(`fg-inverse on ${tone} (solid badge, danger button)`, 4.5, (t) => rgb('fg-inverse', t), (t) => rgb(tone, t));
}
for (const hue of [...TONES, ...VERDICTS]) {
  for (const surface of SURFACES) add(`${hue} text on ${surface}`, 4.5, (t) => rgb(hue, t), (t) => rgb(surface, t));
}
// Tint badges: the tone's text on its tint; the neutral tint carries fg-muted text (badge.tsx).
for (const tone of TONES.filter((x) => x !== 'tone-neutral')) {
  for (const surface of ['surface', 'surface-raised']) {
    add(`${tone} text on its tint over ${surface} (tint badge)`, 4.5, (t) => rgb(tone, t), (t) => over(tone, tint[t], surface, t));
  }
}
add('fg-muted text on the neutral tint (neutral tint badge)', 4.5, (t) => rgb('fg-muted', t), (t) => over('tone-neutral', tint[t], 'surface', t));
// Answer marks: the answer text (fg) on each verdict's tint.
for (const v of VERDICTS) {
  add(`fg answer text on the ${v} mark tint`, 4.5, (t) => rgb('fg', t), (t) => over(v, tint[t], 'surface', t));
}

// Control boundaries (CONTROL_BORDER = fg-subtle at 80%) and the focus ring.
for (const surface of SURFACES) {
  add(`control border on ${surface}`, 3, (t) => over('fg-subtle', 0.8, surface, t), (t) => rgb(surface, t));
  add(`focus ring (fg) on ${surface}`, 3, (t) => rgb('fg', t), (t) => rgb(surface, t));
}
add('switch thumb (fg-subtle) on its track (surface-sunken)', 3, (t) => rgb('fg-subtle', t), (t) => rgb('surface-sunken', t));

// Chart marks on the surface: tones solid and light, verdicts, and the neutral inks.
const shade = LIGHT_SHADE_PERCENT / 100;
for (const tone of TONES) {
  add(`${tone} mark on surface`, 3, (t) => rgb(tone, t), (t) => rgb('surface', t));
  add(`${tone} light-shade mark on surface`, 3, (t) => mix(tone, shade, 'surface', t), (t) => rgb('surface', t));
}
for (const v of VERDICTS) add(`${v} mark on surface`, 3, (t) => rgb(v, t), (t) => rgb('surface', t));
add('v-unknown light-shade mark (Not checked) on surface', 3, (t) => mix('v-unknown', shade, 'surface', t), (t) => rgb('surface', t));
for (const ink of ['fg-muted', 'fg-subtle']) add(`${ink} ink mark on surface`, 3, (t) => rgb(ink, t), (t) => rgb('surface', t));

describe('token contrast', () => {
  it('reads every color token from tokens.css', () => {
    for (const name of ['bg', 'surface', 'fg', 'fg-muted', 'fg-subtle', 'fg-inverse', 'border-strong', ...TONES, ...VERDICTS]) {
      expect(tokens.has(name), name).toBe(true);
    }
  });

  it('matches the contrast DESIGN.md states for fg on surface', () => {
    expect(contrast(rgb('fg', 'light'), rgb('surface', 'light'))).toBeCloseTo(17.3, 0);
    expect(contrast(rgb('fg', 'dark'), rgb('surface', 'dark'))).toBeCloseTo(15.0, 0);
  });

  for (const theme of ['light', 'dark'] as const) {
    it.each(pairs.map((p) => [p.what, p] as const))(`${theme}: %s`, (_what, pair) => {
      const ratio = contrast(pair.fg(theme), pair.bg(theme));
      expect(ratio, `${pair.what} (${theme}) is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(pair.min);
    });
  }
});
