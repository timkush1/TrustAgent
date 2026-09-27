import { describe, expect, it } from 'vitest';
import { BACKOFF_CAP_MS, BACKOFF_FLOOR_MS, backoffDelay } from './backoff';

describe('backoffDelay', () => {
  it('draws from [0, min(30000, 500·2^n)) with a 250 ms floor', () => {
    expect(backoffDelay(0, () => 0)).toBe(BACKOFF_FLOOR_MS);
    expect(backoffDelay(0, () => 0.999)).toBe(499);
    expect(backoffDelay(3, () => 0.5)).toBe(2000);
    expect(backoffDelay(6, () => 0.999)).toBe(29_970);
  });

  it('stays capped for any attempt count', () => {
    for (const n of [7, 20, 1000, Number.MAX_SAFE_INTEGER]) {
      expect(backoffDelay(n, () => 0.999999)).toBeLessThanOrEqual(BACKOFF_CAP_MS);
    }
  });
});
