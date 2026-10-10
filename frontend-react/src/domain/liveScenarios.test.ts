import { describe, expect, it } from 'vitest';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import { ALLOWED_REQUEST_HEADERS, DECISIONS_PATH, isLiveScenario, LIVE_CHECKS, LIVE_SCENARIOS, liveHref, STATIC_CONNECT_PRIMARY } from './liveScenarios';

describe('live scenarios and capability constants (D99, D100)', () => {
  it('offers only scenarios of the demo pack', () => {
    const ids = new Set(scenarioPack.scenarios.map((s) => s.id));
    for (const id of LIVE_SCENARIOS) expect(ids.has(id)).toBe(true);
  });

  it('builds the live link relative to the base', () => {
    expect(liveHref('/TrustAgent/', 'refund-contradiction-flag')).toBe('/TrustAgent/app/playground/?scenario=refund-contradiction-flag#live');
    expect(liveHref('/TrustAgent', 'grounded-pass')).toBe('/TrustAgent/app/playground/?scenario=grounded-pass#live');
  });

  it('honours only listed ids', () => {
    expect(isLiveScenario('pii-leak-flag')).toBe(true);
    for (const bad of ['javascript:alert(1)', '../x', 'refund', 1, null]) expect(isLiveScenario(bad)).toBe(false);
  });

  it('records the Step 0a results (scope L)', () => {
    expect(LIVE_CHECKS).toBe(true);
    expect(DECISIONS_PATH).toBe('alpha/decisions');
    expect(STATIC_CONNECT_PRIMARY).toBe('connect');
    expect([...ALLOWED_REQUEST_HEADERS].sort()).toEqual(['accept', 'authorization', 'content-type', 'http-referer', 'x-openrouter-title']);
    expect(ALLOWED_REQUEST_HEADERS).not.toContain('x-title');
  });
});
