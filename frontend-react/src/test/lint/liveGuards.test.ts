// @vitest-environment node
/**
 * Proves the live-mode lint guards (D102, D138): in a live component every rule fires, the merged
 * lists keep the raw-enum rule working beside them, and the allow-listed modules may do exactly
 * what they are allowed to.
 */
import { fileURLToPath } from 'node:url';
import { ESLint } from 'eslint';
import { describe, expect, it } from 'vitest';

const root = fileURLToPath(new URL('../../..', import.meta.url));
const eslint = new ESLint({ cwd: root });
const LIVE = 'src/components/replay/live/Fixture.tsx';

async function rules(code: string, filePath = LIVE): Promise<string[]> {
  const [result] = await eslint.lintText(code, { filePath });
  return result.messages.map((m) => m.ruleId ?? 'fatal').filter((id) => id.startsWith('no-restricted') || id === 'fatal');
}

const header = `import type { TraceSummary } from '@/types/api';\n`;

describe('live-mode lint guards in components/replay/live (D102, D138)', () => {
  it.each([
    ['a raw enum', 'export function A({ t }: { t: TraceSummary }) { return <span>{t.action}</span>; }', 'no-restricted-syntax'],
    ['a ta.openrouter. literal', "export function A() { return <span>{sessionStorage.getItem('ta.openrouter.key')}</span>; }", 'no-restricted-syntax'],
    ['a ta.pkce. template', 'export function A({ n }: { n: string }) { return <span>{sessionStorage.getItem(`ta.pkce.${n}`)}</span>; }', 'no-restricted-syntax'],
    ['window.localStorage', "export function A() { return <span>{window.localStorage.getItem('x')}</span>; }", 'no-restricted-properties'],
    ['a bare localStorage', "export function A() { return <span>{localStorage.getItem('x')}</span>; }", 'no-restricted-globals'],
    ['document.cookie', 'export function A() { return <span>{document.cookie}</span>; }', 'no-restricted-properties'],
    ['a bare fetch', "export function A() { void fetch('https://openrouter.ai/api/v1/key'); return null; }", 'no-restricted-globals'],
    ['window.fetch', "export function A() { void window.fetch('/x'); return null; }", 'no-restricted-properties'],
    ['navigator.sendBeacon', "export function A() { navigator.sendBeacon('/x'); return null; }", 'no-restricted-properties'],
    ['a WebSocket', "export function A() { new WebSocket('wss://x'); return null; }", 'no-restricted-globals'],
    ['target=_blank without rel', 'export function A() { return <a href="https://openrouter.ai/settings/keys" target="_blank">keys</a>; }', 'no-restricted-syntax'],
    ['target=_blank with noopener only', 'export function A() { return <a href="https://openrouter.ai" target="_blank" rel="noopener">x</a>; }', 'no-restricted-syntax'],
    ['a nested target=_blank link once', 'export function A({ F }: { F: (p: { d: unknown }) => null }) { return <F d={<a href="https://openrouter.ai" rel="noreferrer" target="_blank">x</a>} />; }', 'no-restricted-syntax'],
  ])('flags %s', async (_name, body, rule) => {
    expect(await rules(header + body)).toEqual([rule]);
  }, 20_000);

  it('allows a new tab with rel="noopener noreferrer" and enum values through presentation', async () => {
    const code =
      header +
      "import { outcomeView } from '@/domain/presentation';\n" +
      'export function A() { return <a href="https://openrouter.ai" target="_blank" rel="noopener noreferrer">{outcomeView("passed").label}</a>; }';
    expect(await rules(code)).toEqual([]);
  }, 20_000);
});

describe('the allow-listed modules (D102, D138)', () => {
  it('lets the key store name its storage keys', async () => {
    expect(await rules("export const KEY = 'ta.openrouter.key';\n", 'src/lib/openrouter/keyStore.ts')).toEqual([]);
    expect(await rules("export const KEY = 'ta.openrouter.key';\n", 'src/lib/openrouter/usage.ts')).toEqual(['no-restricted-syntax']);
  }, 20_000);

  it('lets the OpenRouter transport fetch, but not store outside sessionStorage', async () => {
    expect(await rules("export const get = () => fetch('https://openrouter.ai/api/v1/key');\n", 'src/lib/openrouter/http.ts')).toEqual([]);
    expect(await rules("export const k = () => window.localStorage.getItem('x');\n", 'src/lib/openrouter/http.ts')).toEqual(['no-restricted-properties']);
  }, 20_000);

  it('lets the data layer fetch and nothing else', async () => {
    expect(await rules("export const get = () => fetch('/api/status');\n", 'src/data/http.ts')).toEqual([]);
    expect(await rules("export const get = () => fetch('/api/status');\n", 'src/data/queries.ts')).toEqual(['no-restricted-globals']);
    expect(await rules("export const get = () => globalThis.fetch('/api/status');\n", 'src/components/traffic/Fixture.tsx')).toEqual(['no-restricted-properties']);
  }, 20_000);

  it('keeps the storage keys out of every other module, and leaves tests alone', async () => {
    expect(await rules("export const KEY = 'ta.pkce.verifier';\n", 'src/landing/Fixture.ts')).toEqual(['no-restricted-syntax']);
    expect(await rules("export const KEY = 'ta.pkce.verifier';\nvoid fetch('/x');\n", 'src/lib/openrouter/keyStore.test.ts')).toEqual([]);
  }, 20_000);

  it('still lets the rest of the app use localStorage (the theme)', async () => {
    expect(await rules("export const t = () => window.localStorage.getItem('theme');\n", 'src/lib/theme.ts')).toEqual([]);
  }, 20_000);
});
