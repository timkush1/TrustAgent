import { describe, expect, it } from 'vitest';
import outcomesSchema from '@contracts/schema/ui-outcomes.v1.schema.json';
import { OUTCOME_IDS, outcomesContract } from './contracts';

describe('UI contract typing', () => {
  it('OUTCOME_IDS matches the ui-outcomes schema enum and the outcomes file', () => {
    const schemaIds = outcomesSchema.properties.outcomes.items.properties.id.enum;
    expect([...OUTCOME_IDS].sort()).toEqual([...schemaIds].sort());
    expect(outcomesContract.outcomes.map((o) => o.id).sort()).toEqual([...OUTCOME_IDS].sort());
  });

  it('ends with a catch-all rule', () => {
    expect(outcomesContract.rules.at(-1)?.when).toEqual({});
  });
});
