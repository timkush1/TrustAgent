/**
 * `import { digest } from 'virtual:trustagent/tape-digest'`: the landing's tape card, built at
 * build time from the chosen dataset's manifest (kind, label, date, snapshot, judge, outcome counts
 * and five examples), so the landing stops fetching the manifest (LD-5). Owned by WEB-LANDING; the
 * module type is declared in src/env.d.ts.
 *
 * Step 0b stub: the module id resolves, and loading it fails the build until LD-5 fills the body.
 */
import type { Plugin } from 'vite';

export const DIGEST_MODULE = 'virtual:trustagent/tape-digest';

export function digestPlugin(): Plugin {
  const resolved = `\0${DIGEST_MODULE}`;
  return {
    name: 'trustagent:tape-digest',
    resolveId(source) {
      return source === DIGEST_MODULE ? resolved : undefined;
    },
    load(id) {
      if (id !== resolved) return undefined;
      this.error(`${DIGEST_MODULE} is not built yet (LD-5 fills vite/plugins/digest.ts)`);
    },
  };
}
