import { z } from 'zod/mini';
import { OUTCOME_IDS } from '@/domain/contracts';

/** An optional search param; an invalid value is dropped instead of failing the route. */
const lenient = <T extends z.core.SomeType>(schema: T) => z.catch(z.optional(schema), undefined);
const text = z.string().check(z.minLength(1), z.maxLength(200));

/** Traffic search params: `?live=1` plus the filters of SPEC §3.3; bad values drop out. */
export const TrafficSearch = z.object({
  live: lenient(z.union([z.literal(0), z.literal(1)])),
  outcome: lenient(z.enum(OUTCOME_IDS)),
  route: lenient(text),
  model: lenient(text),
  reason: lenient(text),
});

export type TrafficSearchParams = z.infer<typeof TrafficSearch>;
