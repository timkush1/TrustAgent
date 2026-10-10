/**
 * presentation.ts re-exports every words module with `export *` (D86, D136): each export carries
 * its module's prefix, so two planes can never shadow each other's words.
 */
import { describe, expect, it } from 'vitest';
import * as costs from './costs';
import * as evaluators from './evaluators';
import * as live from './live';
import * as replay from './replay';
import * as security from './security';
import * as studio from './studio';

const modules: [string, Record<string, unknown>, RegExp][] = [
  ['security', security, /^(sec|SEC_)/],
  ['costs', costs, /^(cost|COST_)/],
  ['evaluators', evaluators, /^(eval|EVAL_)/],
  ['studio', studio, /^(studio|STUDIO_)/],
  ['live', live, /^(live|LIVE_)/],
  ['replay', replay, /^(replay|REPLAY_)/],
];

describe('words modules: every export carries its prefix (D86)', () => {
  it.each(modules)('%s', (_name, mod, prefix) => {
    expect(Object.keys(mod).filter((name) => !prefix.test(name))).toEqual([]);
  });
});
