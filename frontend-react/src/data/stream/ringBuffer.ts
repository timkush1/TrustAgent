/**
 * Fixed-capacity FIFO between the socket and React. When full, the oldest item is dropped and
 * counted, so a burst can never grow memory without bound; the flush reports the count.
 */
export class RingBuffer<T> {
  readonly capacity: number;
  private readonly items: (T | undefined)[];
  private head = 0;
  private size = 0;
  private dropped = 0;

  constructor(capacity: number) {
    if (!Number.isInteger(capacity) || capacity < 1) throw new RangeError('capacity must be a positive integer');
    this.capacity = capacity;
    this.items = new Array<T | undefined>(capacity);
  }

  get length(): number {
    return this.size;
  }

  push(item: T): void {
    const tail = (this.head + this.size) % this.capacity;
    this.items[tail] = item;
    if (this.size < this.capacity) {
      this.size += 1;
    } else {
      this.head = (this.head + 1) % this.capacity;
      this.dropped += 1;
    }
  }

  /** Removes and returns everything in arrival order, plus how many items overflowed since the last drain. */
  drain(): { items: T[]; dropped: number } {
    const out = new Array<T>(this.size);
    for (let i = 0; i < this.size; i += 1) {
      const idx = (this.head + i) % this.capacity;
      out[i] = this.items[idx] as T;
      this.items[idx] = undefined;
    }
    const dropped = this.dropped;
    this.head = 0;
    this.size = 0;
    this.dropped = 0;
    return { items: out, dropped };
  }
}
