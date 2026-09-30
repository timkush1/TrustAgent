/**
 * The committed demo recording (public/demo/recorded) served by a fake fetch, as the static site
 * serves it, plus a manual clock for the replay player.
 */
import type { ReplayClock } from '@/data/replay/player';
import type { Recording } from '@/types/api';

const files = import.meta.glob<string>('../../public/demo/recorded/**/*.json', { query: '?raw', import: 'default', eager: true });

/** "manifest.json" and "traces/<id>.json" → file text. */
export const RECORDING_FILES: Readonly<Record<string, string>> = Object.fromEntries(
  Object.entries(files).map(([path, text]) => [path.slice(path.indexOf('/recorded/') + '/recorded/'.length), text]),
);

export function committedManifest(): Recording {
  return JSON.parse(RECORDING_FILES['manifest.json']) as Recording;
}

export interface RecordingFetch {
  fetch: typeof fetch;
  requests: string[];
}

/**
 * Serves `${base}demo/recorded/<file>` from the committed files (or `overrides`, raw text), 404
 * for anything else. `manifest` replaces manifest.json with a document.
 */
export function recordingFetch(base = '/TrustAgent/', opts: { manifest?: unknown; fail?: boolean } = {}): RecordingFetch {
  const requests: string[] = [];
  const prefix = `${base}demo/recorded/`;
  const impl = async (input: RequestInfo | URL): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    requests.push(url);
    if (opts.fail) throw new TypeError('Failed to fetch');
    const name = url.startsWith(prefix) ? url.slice(prefix.length) : null;
    const text = name === 'manifest.json' && opts.manifest !== undefined ? JSON.stringify(opts.manifest) : name ? RECORDING_FILES[name] : undefined;
    if (text === undefined) return new Response('<!doctype html><title>404</title>', { status: 404, headers: { 'Content-Type': 'text/html' } });
    return new Response(text, { status: 200, headers: { 'Content-Type': 'application/json; charset=utf-8' } });
  };
  return { fetch: impl as typeof fetch, requests };
}

/** A clock the test advances by hand: `advance(ms)` runs every timer due by then, in order. */
export class ManualClock implements ReplayClock {
  now = 0;
  private timers: { at: number; fn: () => void; id: number }[] = [];
  private nextId = 1;
  /** Real delays the player asked for, in order. */
  readonly delays: number[] = [];

  setTimeout(fn: () => void, ms: number): unknown {
    const id = this.nextId++;
    this.delays.push(ms);
    this.timers.push({ at: this.now + ms, fn, id });
    return id;
  }

  clearTimeout(handle: unknown): void {
    this.timers = this.timers.filter((t) => t.id !== handle);
  }

  get pending(): number {
    return this.timers.length;
  }

  advance(ms: number): void {
    const until = this.now + ms;
    for (;;) {
      const due = this.timers.filter((t) => t.at <= until).sort((a, b) => a.at - b.at || a.id - b.id)[0];
      if (!due) break;
      this.timers = this.timers.filter((t) => t !== due);
      this.now = due.at;
      due.fn();
    }
    this.now = until;
  }

  /** Runs timers until none is left (bounded). */
  runAll(limit = 10_000): void {
    for (let i = 0; i < limit && this.timers.length > 0; i += 1) {
      const next = [...this.timers].sort((a, b) => a.at - b.at || a.id - b.id)[0];
      this.advance(next.at - this.now);
    }
  }
}
