/**
 * Node module hooks that let a plain `node scripts/x.ts` import app modules the way Vite does:
 * the `@/` and `@contracts/` aliases (tsconfig.app.json paths), extensionless relative imports, and
 * JSON imports without an import attribute. Import this file first, then load app code with a
 * dynamic `import()` (static imports are resolved before any hook is registered).
 *
 * Needs Node >= 22.15 (module.registerHooks) and built-in TypeScript type stripping (>= 22.18).
 */
import { readFileSync, statSync } from 'node:fs';
import { registerHooks } from 'node:module';
import { fileURLToPath } from 'node:url';

const SRC = new URL('../../src/', import.meta.url);
const CONTRACTS = new URL('../../../contracts/', import.meta.url);

function aliased(specifier: string): string | null {
  if (specifier.startsWith('@/')) return new URL(specifier.slice(2), SRC).href;
  if (specifier.startsWith('@contracts/')) return new URL(specifier.slice('@contracts/'.length), CONTRACTS).href;
  return null;
}

function isFile(url: string): boolean {
  try {
    return statSync(fileURLToPath(url)).isFile();
  } catch {
    return false;
  }
}

registerHooks({
  resolve(specifier, context, nextResolve) {
    const target = aliased(specifier) ?? specifier;
    const local = target.startsWith('file:') || target.startsWith('./') || target.startsWith('../');
    if (local && context.parentURL) {
      const base = new URL(target, context.parentURL).href;
      for (const candidate of [base, `${base}.ts`, `${base}.tsx`, `${base}/index.ts`]) {
        if (isFile(candidate)) return { url: candidate, shortCircuit: true };
      }
    }
    return nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    if (url.startsWith('file:') && url.endsWith('.json')) {
      // A JSON document is an expression: serve it as an ES module with a default export.
      return { format: 'module', source: `export default ${readFileSync(fileURLToPath(url), 'utf8')};\n`, shortCircuit: true };
    }
    return nextLoad(url, context);
  },
});
