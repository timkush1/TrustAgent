/**
 * `import { policy } from 'virtual:trustagent/policy'`: config/policies.yaml (the policy `make demo`
 * runs and the recording was built with), parsed at build time so no YAML parser ships to the
 * browser (D104). The live Playground, the replay's glass box (D109) and the static Studio read
 * their routes from it. Owned by WEB-LIVE (LV-3).
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Plugin } from 'vite';
import { parse } from 'yaml';

export const POLICY_MODULE = 'virtual:trustagent/policy';

/** The policy document, checked for the fields the browser reads (version and routes). */
export function readPolicy(file: string): Record<string, unknown> {
  const doc: unknown = parse(readFileSync(file, 'utf8'));
  if (typeof doc !== 'object' || doc === null || Array.isArray(doc)) throw new Error(`${file} is not a policy document`);
  const policy = doc as Record<string, unknown>;
  if (typeof policy.version !== 'number') throw new Error(`${file} has no numeric version`);
  if (!Array.isArray(policy.routes) || policy.routes.length === 0) throw new Error(`${file} has no routes`);
  return policy;
}

export function policyPlugin(repoDir: string): Plugin {
  const file = join(repoDir, 'config/policies.yaml');
  const resolved = `\0${POLICY_MODULE}`;
  return {
    name: 'trustagent:policy',
    resolveId(source) {
      return source === POLICY_MODULE ? resolved : undefined;
    },
    load(id) {
      if (id !== resolved) return undefined;
      this.addWatchFile(file);
      let policy: Record<string, unknown>;
      try {
        policy = readPolicy(file);
      } catch (err) {
        this.error(err instanceof Error ? err.message : String(err));
      }
      return `export const policy = ${JSON.stringify(policy)};\n`;
    },
  };
}
