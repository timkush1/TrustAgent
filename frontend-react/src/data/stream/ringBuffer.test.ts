import { describe, expect, it } from 'vitest';
import { RingBuffer } from './ringBuffer';

describe('RingBuffer', () => {
  it('drains in arrival order', () => {
    const r = new RingBuffer<number>(4);
    [1, 2, 3].forEach((n) => r.push(n));
    expect(r.length).toBe(3);
    expect(r.drain()).toEqual({ items: [1, 2, 3], dropped: 0 });
    expect(r.length).toBe(0);
  });

  it('drops the oldest when full and counts it', () => {
    const r = new RingBuffer<number>(3);
    [1, 2, 3, 4, 5].forEach((n) => r.push(n));
    expect(r.drain()).toEqual({ items: [3, 4, 5], dropped: 2 });
    r.push(6);
    expect(r.drain()).toEqual({ items: [6], dropped: 0 });
  });

  it('wraps correctly across drains', () => {
    const r = new RingBuffer<number>(3);
    r.push(1);
    r.push(2);
    r.drain();
    [3, 4, 5].forEach((n) => r.push(n));
    expect(r.drain().items).toEqual([3, 4, 5]);
  });

  it('rejects a non-positive capacity', () => {
    expect(() => new RingBuffer(0)).toThrow(RangeError);
  });
});
