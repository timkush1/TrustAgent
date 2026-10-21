// @vitest-environment node
/**
 * Proves the eslint `no-restricted-syntax` guard (SPEC §3.3): components may not render server
 * enum values raw; they go through src/domain/presentation.ts.
 */
import { fileURLToPath } from 'node:url';
import { ESLint } from 'eslint';
import { describe, expect, it } from 'vitest';

const root = fileURLToPath(new URL('../../..', import.meta.url));
const eslint = new ESLint({ cwd: root });

async function restricted(code: string, filePath = 'src/components/traffic/Fixture.tsx'): Promise<number> {
  const [result] = await eslint.lintText(code, { filePath });
  return result.messages.filter((m) => m.ruleId === 'no-restricted-syntax').length;
}

const header = `import type { TraceSummary } from '@/types/api';\n`;

describe('raw enum rule', () => {
  it.each([
    ['member expression', 'export function A({ t }: { t: TraceSummary }) { return <span>{t.action}</span>; }'],
    ['optional chain', 'export function A({ t }: { t?: TraceSummary }) { return <span>{t?.gate_status}</span>; }'],
    ['fallback', "export function A({ t }: { t: TraceSummary }) { return <span>{t.verdict ?? '—'}</span>; }"],
    ['template literal', 'export function A({ t }: { t: TraceSummary }) { return <span>{`${t.audit.status}`}</span>; }'],
    ['fragment child', 'export function A({ t }: { t: TraceSummary }) { return <>{t.would_action}</>; }'],
    ['upper-case verdict text', 'export function A() { return <span>CONTRADICTED</span>; }'],
    ['upper-case verdict literal', "export function A() { return <span>{'NOT_ASSESSED'}</span>; }"],
  ])('flags a %s', async (_name, body) => {
    expect(await restricted(header + body)).toBe(1);
  }, 20_000);

  it('allows enum values in attributes and presentation calls', async () => {
    const code =
      header +
      "import { outcomeView } from '@/domain/presentation';\n" +
      'export function A({ t }: { t: TraceSummary }) { return <span data-action={t.action}>{outcomeView("passed").label}</span>; }';
    expect(await restricted(code)).toBe(0);
  }, 20_000);

  it('does not apply outside components and routes', async () => {
    const code = header + 'export function a(t: TraceSummary) { return <span>{t.action}</span>; }';
    expect(await restricted(code, 'src/domain/Fixture.tsx')).toBe(0);
  }, 20_000);
});
