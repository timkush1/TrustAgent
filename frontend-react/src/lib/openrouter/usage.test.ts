import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { createKeyStore } from './keyStore';
import { createUsageStore, isBillable, isRejected, priceFromTable, REJECTED_PAUSE, USAGE_NAME, type UsageStore, type UsageStoreOptions } from './usage';
import { useUsage } from './useUsage';
import { MemoryStorage } from './testing';


const stores: UsageStore[] = [];
function make(opts: UsageStoreOptions = {}): UsageStore {
  const storage = new MemoryStorage();
  const s = createUsageStore({ maxCalls: 100, maxTokens: 8000, target: null, price: () => null, storage: () => storage, ...opts });
  stores.push(s);
  return s;
}
afterEach(() => {
  for (const s of stores.splice(0)) s.dispose();
});

const OK = { input_tokens: 400, cost: 0.0000168 };

describe('usage ledger: what counts (D101)', () => {
  it('classifies statuses', () => {
    expect(isBillable(200)).toBe(true);
    expect(isBillable(204)).toBe(true);
    expect(isBillable(500, { input_tokens: 1, cost: null })).toBe(true);
    for (const s of [0, 401, 402, 403, 404, 413, 429, 500]) expect(isBillable(s)).toBe(false);
    for (const s of [400, 401, 403, 404, 408, 413, 422, 429, 500, 502, 503]) expect(isRejected(s)).toBe(true);
    for (const s of [0, 200, 402]) expect(isRejected(s)).toBe(false);
    expect(isRejected(500, { input_tokens: 1, cost: null })).toBe(false);
  });

  it('allows the 100th billable call and refuses the 101st', () => {
    const s = make();
    for (let i = 0; i < 99; i++) {
      expect(s.reserve([500])).toEqual({ ok: true });
      s.settle(200, OK);
    }
    expect(s.getSnapshot().calls).toBe(99);
    expect(s.reserve([500])).toEqual({ ok: true });
    s.settle(200, OK);
    expect(s.getSnapshot().calls).toBe(100);
    expect(s.reserve([500])).toEqual({ ok: false, reason: 'cap_reached' });
  });

  it('reserves every slot of a multi-request check or none', () => {
    const s = make({ maxCalls: 3 });
    expect(s.reserve([100, 100, 100, 100])).toEqual({ ok: false, reason: 'cap_reached' });
    expect(s.getSnapshot().inflight).toBe(0);
    expect(s.reserve([100, 100])).toEqual({ ok: true });
    expect(s.getSnapshot().inflight).toBe(2);
    // Slots in flight count against the cap.
    expect(s.reserve([100, 100])).toEqual({ ok: false, reason: 'cap_reached' });
    s.settle(200, OK);
    s.settle(0);
    expect(s.getSnapshot()).toMatchObject({ calls: 1, inflight: 0 });
    expect(s.reserve([100, 100])).toEqual({ ok: true });
  });

  it('does not consume the cap on 402, 401 or no response', () => {
    const s = make({ maxCalls: 1 });
    for (const status of [402, 401, 0]) {
      expect(s.reserve([10])).toEqual({ ok: true });
      s.settle(status);
    }
    expect(s.getSnapshot()).toMatchObject({ calls: 0, inflight: 0, cost_usd: 0, cost_source: null });
    // 401 is a rejected response (it counts toward the pause), 402 and no response do not.
    expect(s.getSnapshot().rejected).toBe(1);
    expect(s.reserve([10])).toEqual({ ok: true });
  });

  it('counts a 200 whose body could not be read (bad_json)', () => {
    const s = make();
    s.reserve([10]);
    s.settle(200);
    expect(s.getSnapshot()).toMatchObject({ calls: 1, input_tokens: 0, cost_source: 'unknown' });
  });

  it('counts an error response that reported usage', () => {
    const s = make();
    s.reserve([10]);
    s.settle(500, { input_tokens: 30, cost: 0.00001 });
    expect(s.getSnapshot()).toMatchObject({ calls: 1, rejected: 0, input_tokens: 30, cost_source: 'vendor' });
  });

  it('pauses after 10 rejected responses in a row, until resume', () => {
    const s = make();
    for (let i = 0; i < REJECTED_PAUSE - 1; i++) {
      s.reserve([10]);
      s.settle(i % 2 === 0 ? 429 : 503);
    }
    expect(s.getSnapshot().paused).toBe(false);
    // A billable response ends the run.
    s.reserve([10]);
    s.settle(200, OK);
    expect(s.getSnapshot().rejected).toBe(0);

    for (let i = 0; i < REJECTED_PAUSE; i++) {
      expect(s.reserve([10])).toEqual({ ok: true });
      s.settle(400);
    }
    expect(s.getSnapshot()).toMatchObject({ rejected: REJECTED_PAUSE, paused: true, calls: 1 });
    expect(s.reserve([10])).toEqual({ ok: false, reason: 'rejected_pause' });

    s.resume();
    expect(s.getSnapshot().paused).toBe(false);
    expect(s.reserve([10])).toEqual({ ok: true });
  });

  it('refuses an estimate over the token cap before any fetch', () => {
    const s = make({ maxTokens: 8000 });
    expect(s.reserve([8000])).toEqual({ ok: true });
    s.settle(0);
    expect(s.reserve([1200, 8001])).toEqual({ ok: false, reason: 'token_cap' });
    expect(s.reserve([Number.NaN])).toEqual({ ok: false, reason: 'token_cap' });
    expect(s.getSnapshot().inflight).toBe(0);
  });

  it('an empty check reserves nothing', () => {
    const s = make({ maxCalls: 1 });
    s.reserve([1]);
    s.settle(200, OK);
    expect(s.reserve([])).toEqual({ ok: true });
  });
});

describe('usage ledger: cost (D101)', () => {
  it('sums the vendor cost and labels it vendor', () => {
    const s = make();
    s.reserve([10, 10]);
    s.settle(200, { input_tokens: 366, cost: 0.000015372 });
    s.settle(200, { input_tokens: 100, cost: 0.0000042 });
    expect(s.getSnapshot()).toMatchObject({ calls: 2, input_tokens: 466, cost_source: 'vendor' });
    expect(s.getSnapshot().cost_usd).toBeCloseTo(0.000019572, 12);
  });

  it('prices a call without a vendor cost with its model, and labels the total an estimate', () => {
    const seen: string[] = [];
    const s = make({
      price: (model, usage) => {
        seen.push(model);
        return (usage.input_tokens * 0.042) / 1e6;
      },
    });
    const jev = s.forModel('typesafe/jev-1.13');
    jev.reserve([1000]);
    jev.settle(200, { input_tokens: 1000, cost: null });
    s.reserve([10]);
    s.settle(200, { input_tokens: 10, cost: 0.000001 });
    expect(seen).toEqual(['typesafe/jev-1.13']);
    expect(s.getSnapshot().cost_source).toBe('estimate');
    expect(s.getSnapshot().cost_usd).toBeCloseTo(0.000042 + 0.000001, 12);
  });

  it('says unknown when a call can be neither read nor priced', () => {
    const s = make({ price: () => null });
    s.forModel('typesafe/jev-1.13').settle(200, { input_tokens: 1000, cost: null });
    expect(s.getSnapshot()).toMatchObject({ cost_usd: 0, cost_source: 'unknown' });
    // A later vendor figure does not make the total look exact again.
    s.settle(200, { input_tokens: 1, cost: 0.1 });
    expect(s.getSnapshot().cost_source).toBe('unknown');
  });

  it('ignores a negative or non-finite vendor cost', () => {
    const s = make({ price: () => 0.5 });
    s.forModel('m').settle(200, { input_tokens: 1, cost: -1 });
    expect(s.getSnapshot()).toMatchObject({ cost_usd: 0.5, cost_source: 'estimate' });
  });
});

describe('usage ledger: pricing from contracts/pricing (the TS cost core)', () => {
  it('prices the live models from the table, and nothing it does not list', () => {
    expect(priceFromTable('typesafe/jev-1.13', { input_tokens: 1_000_000, cost: null })).toBeCloseTo(0.042, 12);
    expect(priceFromTable('anthropic/claude-haiku-4.5', { input_tokens: 1_000_000, output_tokens: 1_000_000, cost: null })).toBeCloseTo(6, 12);
    expect(priceFromTable('someone/else', { input_tokens: 1000, cost: null })).toBeNull();
  });

  it('rounds a fractional estimate up instead of failing (D150)', () => {
    expect(priceFromTable('typesafe/jev-1.13', { input_tokens: 999_999.2, cost: null })).toBeCloseTo(0.042, 12);
  });

  it('the default ledger labels a call priced from the table an estimate', () => {
    const s = createUsageStore({ maxCalls: 100, maxTokens: 8000, storage: () => new MemoryStorage(), target: null });
    stores.push(s);
    s.forModel('typesafe/jev-1.13').settle(200, { input_tokens: 400, cost: null });
    expect(s.getSnapshot()).toMatchObject({ calls: 1, cost_source: 'estimate' });
    expect(s.getSnapshot().cost_usd).toBeCloseTo((400 * 0.042) / 1e6, 12);
  });
});

describe('usage ledger: storage', () => {
  it('survives a reload of the tab', () => {
    const storage = new MemoryStorage();
    const a = make({ storage: () => storage });
    a.reserve([10]);
    a.settle(200, OK);
    a.reserve([10]);
    a.settle(429);
    const b = make({ storage: () => storage });
    expect(b.getSnapshot()).toMatchObject({ calls: 1, rejected: 1, input_tokens: 400, cost_source: 'vendor' });
    expect(JSON.parse(storage.getItem(USAGE_NAME) ?? '')).toEqual({ calls: 1, rejected: 1, input_tokens: 400, cost_usd: 0.0000168, cost_source: 'vendor' });
  });

  it('Forget key keeps usage', () => {
    const storage = new MemoryStorage();
    const usage = make({ storage: () => storage });
    usage.reserve([10]);
    usage.settle(200, OK);
    const keys = createKeyStore({ storage: () => storage, target: null });
    keys.set('sk-or-v1-e2e-0123456789abcdef0123456789abcdef');
    keys.forget();
    keys.dispose();
    expect(make({ storage: () => storage }).getSnapshot().calls).toBe(1);
  });

  it('starts from zero on a tampered entry', () => {
    const storage = new MemoryStorage();
    for (const raw of ['{', '[]', JSON.stringify({ calls: -5, rejected: 0, input_tokens: 0, cost_usd: 0, cost_source: null }), JSON.stringify({ calls: 1.5 })]) {
      storage.setItem(USAGE_NAME, raw);
      expect(make({ storage: () => storage }).getSnapshot()).toMatchObject({ calls: 0, rejected: 0, cost_source: null });
    }
  });

  it('keeps counting in memory when storage is blocked', () => {
    const s = make({
      storage: () => {
        throw new DOMException('blocked', 'SecurityError');
      },
    });
    s.reserve([10]);
    s.settle(200, OK);
    expect(s.getSnapshot().calls).toBe(1);
  });

  it('re-reads on a bfcache restore', () => {
    const storage = new MemoryStorage();
    const target = new EventTarget() as unknown as Window;
    const page = make({ storage: () => storage, target });
    const other = make({ storage: () => storage });
    other.reserve([10]);
    other.settle(200, OK);
    expect(page.getSnapshot().calls).toBe(0);
    target.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }));
    expect(page.getSnapshot().calls).toBe(1);
  });

  it('useUsage re-renders on every change', () => {
    const s = make();
    const { result } = renderHook(() => useUsage(s));
    expect(result.current).toMatchObject({ calls: 0, maxCalls: 100, inflight: 0 });
    act(() => {
      s.reserve([10]);
    });
    expect(result.current.inflight).toBe(1);
    act(() => {
      s.settle(200, OK);
    });
    expect(result.current).toMatchObject({ calls: 1, inflight: 0 });
  });
});
