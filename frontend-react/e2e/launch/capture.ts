/**
 * Shared rules for the launch captures (D115; `npm run og`, `hero`, `screenshots`, or `make og`,
 * `hero-gif`, `screenshots`), which run against the launch build of the static site (`--mode
 * launch`: the Pages site plus the `/og` route) served by `vite preview`.
 *
 * Honest numbers decide where a capture may go:
 * - the social card and the hero come only from a `kind: recorded` tape (real fast check and judge).
 *   On the simulated tape (`kind: mock`) they refuse, unless OG_ALLOW_MOCK=1 / HERO_ALLOW_MOCK=1,
 *   which writes a capture that says Simulated, and only to frontend-react/test-results/launch/
 *   (scratch, never committed: Playwright's output directory);
 * - the dashboard screenshots may use the simulated tape: each page then shows the replay's
 *   Simulated label, and the README captions them "Simulated".
 *
 * Captures never change shared state: they run on the static build, whose pages hold nothing a
 * capture could save (CO-20), and every capture reaches no origin but the site's own.
 */
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';

/** The repository root (frontend-react/e2e/launch → ../../..). */
export const REPO_ROOT = resolve(fileURLToPath(new URL('../../..', import.meta.url)));
/** Scratch output for captures of the simulated tape (Playwright's output directory). */
export const SCRATCH_DIR = resolve(REPO_ROOT, 'frontend-react/test-results/launch');

/** The social card's committed copies (D115). */
export const OG_TARGETS = ['frontend-react/public/og.png', 'docs/launch/og.png'] as const;
export const OG_SIZE = { width: 1280, height: 640 } as const;
export const OG_MAX_BYTES = 1_000_000;

/** Where the dashboard screenshots go (the lead's I-5b block in README.md shows them). */
export const SCREENSHOT_DIR = 'docs/images/v2';

/** The hero recording: the video and its sidecar, read by scripts/make_hero_gif.py. */
export const HERO_DIR = resolve(SCRATCH_DIR, 'hero');
export const HERO_SIZE = { width: 1200, height: 675 } as const;

/** Local preferences the dashboard reads (src/lib/theme.ts, components/replay/guideState.ts). */
const THEME_STORAGE_KEY = 'trustagent.theme';
const GUIDE_STORAGE_KEY = 'ta.replay.guide';

export type Theme = 'light' | 'dark';
export type TapeKind = 'mock' | 'recorded';

export interface Tape {
  kind: TapeKind;
  /** The manifest's own label ("Simulated (mock checks + mock judge): not model output", ...). */
  label: string;
  traces: number;
}

/** The tape the built site ships (`demo/manifest.json`), read the way the replay reads it. */
export async function shippedTape(page: Page): Promise<Tape> {
  const res = await page.request.get('demo/manifest.json');
  if (!res.ok()) throw new Error(`the static build serves no demo/manifest.json (HTTP ${res.status()})`);
  const manifest = (await res.json()) as { kind?: unknown; label?: unknown; traces?: unknown };
  if (manifest.kind !== 'mock' && manifest.kind !== 'recorded') {
    throw new Error(`demo/manifest.json has an unknown kind: ${JSON.stringify(manifest.kind)}`);
  }
  if (typeof manifest.label !== 'string' || manifest.label.trim() === '') throw new Error('demo/manifest.json has no label');
  if (!Array.isArray(manifest.traces) || manifest.traces.length === 0) throw new Error('demo/manifest.json has no traces');
  return { kind: manifest.kind, label: manifest.label, traces: manifest.traces.length };
}

/** Whether a scratch capture of the simulated tape was asked for (`OG_ALLOW_MOCK=1`, ...). */
export function allowsMock(variable: string, env: NodeJS.ProcessEnv = process.env): boolean {
  return env[variable] === '1';
}

/**
 * Refuses the simulated tape for a capture that must be real. The message says how to get a real
 * tape and how to make a labelled scratch copy instead.
 */
export function refuseMock(tape: Tape, variable: string, what: string, env: NodeJS.ProcessEnv = process.env): void {
  if (tape.kind !== 'mock' || allowsMock(variable, env)) return;
  throw new Error(
    `${what} refuses the simulated tape (kind: mock): a capture of it would show mock output as the product's. ` +
      'Record the demo first (make record-demo CONFIRM=1) and build with it, or set ' +
      `${variable}=1 for a capture labelled Simulated, written only to frontend-react/test-results/launch/.`,
  );
}

/** Whether a path is inside the scratch directory. */
export function inScratch(path: string): boolean {
  const abs = resolve(REPO_ROOT, path);
  return abs === SCRATCH_DIR || abs.startsWith(SCRATCH_DIR + sep);
}

/** Where the social card goes: its committed copies, or the scratch directory for the simulated tape. */
export function ogTargets(kind: TapeKind): string[] {
  return kind === 'recorded' ? OG_TARGETS.map((p) => resolve(REPO_ROOT, p)) : [resolve(SCRATCH_DIR, 'og.png')];
}

/** Writes a file, creating its directory, and returns its path relative to the repository. */
export function writeCapture(path: string, data: Buffer | string): string {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, data);
  return relative(REPO_ROOT, path);
}

/** The width and height of a PNG, from its IHDR chunk; throws when the bytes are not a PNG. */
export function pngSize(png: Buffer): { width: number; height: number } {
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  if (png.length < 24 || !png.subarray(0, 8).equals(signature) || png.toString('latin1', 12, 16) !== 'IHDR') {
    throw new Error('not a PNG image');
  }
  return { width: png.readUInt32BE(16), height: png.readUInt32BE(20) };
}

/** The browser-context options of a capture (valid for `test.use` and `browser.newContext`). */
export interface CaptureContext {
  viewport: { width: number; height: number };
  deviceScaleFactor: number;
  colorScheme: Theme;
  reducedMotion: 'reduce';
  locale: string;
  timezoneId: string;
}

/**
 * Browser-context options for a capture: a fixed theme (emulated for prefers-color-scheme; the
 * stored preference is presetPreferences'), no motion, and a fixed locale and time zone, so two
 * runs of the same tape give the same pictures.
 */
export function captureContext(theme: Theme, size: { width: number; height: number }): CaptureContext {
  return {
    viewport: size,
    deviceScaleFactor: 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
    locale: 'en-US',
    timezoneId: 'UTC',
  };
}

/** Sets the dashboard's theme preference and, unless the tour should show, hides the replay's tour. */
export async function presetPreferences(page: Page, theme: Theme, opts: { showGuide?: boolean } = {}): Promise<void> {
  await page.addInitScript(
    ({ themeKey, guideKey, theme: t, showGuide }) => {
      try {
        window.localStorage.setItem(themeKey, t);
        if (!showGuide) window.localStorage.setItem(guideKey, 'hidden');
      } catch {
        // Storage blocked: the page follows prefers-color-scheme, which the context emulates.
      }
    },
    { themeKey: THEME_STORAGE_KEY, guideKey: GUIDE_STORAGE_KEY, theme, showGuide: opts.showGuide ?? false },
  );
}

/**
 * The questions the glass box lists more than once within one request (CO-20: a capture of a stack
 * that kept test checks showed "Competitor mentions" twice). Empty when every question is listed once.
 */
export async function duplicatedQuestions(page: Page): Promise<string[]> {
  return page.getByTestId('jev-request').evaluate((panel) => {
    const repeated: string[] = [];
    for (const list of Array.from(panel.querySelectorAll('h4 + ul'))) {
      const heading = list.previousElementSibling?.textContent ?? '';
      if (!heading.endsWith('what it was asked')) continue;
      const seen = new Set<string>();
      for (const item of Array.from(list.querySelectorAll(':scope > li > span:first-child'))) {
        const name = (item.textContent ?? '').trim();
        if (seen.has(name)) repeated.push(name);
        seen.add(name);
      }
    }
    return repeated;
  });
}
