// @vitest-environment node
/**
 * Drift check for types.gen.ts (SPEC §2.6: "committed, drift-checked in CI"): regenerating from
 * contracts/schema must reproduce the committed file byte for byte. On failure, run
 * `make gen-types` and commit the result.
 *
 * Also proves at compile time that api.ts re-exports the generated types and embeds them in the
 * §2.6 interfaces that the P1 types stream deferred.
 */
import { describe, expect, expectTypeOf, it } from 'vitest';
import type * as Api from '@/types/api';
import { COMMON_SCHEMA, generateTypesGen, TYPE_SCHEMAS } from './generate';
import committed from './types.gen.ts?raw';
import type * as Gen from './types.gen';

const schemaFiles = import.meta.glob<unknown>('@contracts/schema/*.schema.json', { eager: true, import: 'default' });
const byName = new Map(Object.entries(schemaFiles).map(([path, doc]) => [path.slice(path.lastIndexOf('/') + 1), doc]));

describe('types.gen.ts', () => {
  it('reads schema files that exist', () => {
    for (const file of [COMMON_SCHEMA, ...TYPE_SCHEMAS]) expect(byName.has(file), file).toBe(true);
  });

  it('matches a fresh generation from contracts/schema', async () => {
    const fresh = await generateTypesGen((file) => {
      if (!byName.has(file)) throw new Error(`unknown schema file ${file}`);
      return byName.get(file);
    });
    expect(fresh === committed, 'src/lib/contracts/types.gen.ts is stale: run `make gen-types` and commit it').toBe(true);
  }, 30_000);

  it('names the contract types after the schema titles', () => {
    for (const name of ['EvaluatorSpec', 'EvaluatorDraft', 'PolicyDoc', 'RouteSettings', 'RouteEvaluatorRef', 'CalibrationFile', 'CalibrationEntry', 'BenchmarkRun', 'DecideInput', 'GateDecisionView', 'RawAnswer']) {
      expect(committed).toMatch(new RegExp(`^export (type|interface) ${name}\\b`, 'm'));
    }
    // Every shared definition is emitted once; json-schema-to-typescript numbers duplicates.
    expect(committed).not.toMatch(/^export (type|interface) [A-Za-z]+[0-9]+\b/m);
  });

  it('is what api.ts re-exports and embeds (SPEC §2.6)', () => {
    expectTypeOf<Api.PolicyDoc>().toEqualTypeOf<Gen.PolicyDoc>();
    expectTypeOf<Api.RouteSettings>().toEqualTypeOf<Gen.RouteSettings>();
    expectTypeOf<Api.RouteEvaluatorRef>().toEqualTypeOf<Gen.RouteEvaluatorRef>();
    expectTypeOf<Api.EvaluatorSpec>().toEqualTypeOf<Gen.EvaluatorSpec>();
    expectTypeOf<Api.EvaluatorDraft>().toEqualTypeOf<Gen.EvaluatorDraft>();
    expectTypeOf<Api.CalibrationFile>().toEqualTypeOf<Gen.CalibrationFile>();
    expectTypeOf<Api.CalibrationEntry>().toEqualTypeOf<Gen.CalibrationEntry>();
    expectTypeOf<Api.BenchmarkRun>().toEqualTypeOf<Gen.BenchmarkRun>();

    expectTypeOf<Api.PolicyVersion['doc']>().toEqualTypeOf<Gen.PolicyDoc>();
    expectTypeOf<Api.PolicyState>().toExtend<Api.PolicyVersion>();
    expectTypeOf<Api.EffectiveRoute['settings']>().toEqualTypeOf<Gen.RouteSettings>();
    expectTypeOf<Api.EvaluatorVersion['spec']>().toEqualTypeOf<Gen.EvaluatorSpec>();
    expectTypeOf<Api.EvaluatorDetail['latest']>().toEqualTypeOf<Api.EvaluatorVersion>();
    expectTypeOf<Api.EvaluatorDetail['versions'][number]>().toEqualTypeOf<Omit<Api.EvaluatorVersion, 'spec'>>();
    expectTypeOf<Api.StudioDraftResponse['draft']>().toEqualTypeOf<Gen.EvaluatorDraft | null>();
    expectTypeOf<Api.StudioTestRequest['spec']>().toEqualTypeOf<Gen.EvaluatorSpec | undefined>();
    expectTypeOf<Api.StudioDryRunRequest['window']>().toEqualTypeOf<'24h' | '7d'>();
    expectTypeOf<Api.CalibrationRecord['entry']>().toEqualTypeOf<Gen.CalibrationEntry>();
    expectTypeOf<Api.CalibrationRecord>().toExtend<Api.CalibrationRecordMeta>();
  });
});
