/**
 * Test helpers for the live-mode stores (never imported by app code): a Map-backed Storage, so a
 * test controls sessionStorage without touching jsdom's shared one.
 */
export class MemoryStorage implements Storage {
  readonly map = new Map<string, string>();
  get length(): number {
    return this.map.size;
  }
  clear(): void {
    this.map.clear();
  }
  getItem(k: string): string | null {
    return this.map.get(k) ?? null;
  }
  key(i: number): string | null {
    return [...this.map.keys()][i] ?? null;
  }
  removeItem(k: string): void {
    this.map.delete(k);
  }
  setItem(k: string, v: string): void {
    this.map.set(k, String(v));
  }
}

/** A storage getter that always returns the same fresh MemoryStorage. */
export function memoryStorage(): () => Storage {
  const s = new MemoryStorage();
  return () => s;
}
