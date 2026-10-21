/**
 * The committed simulated recording (demo-data/simulated, served at demo/; D110) served by a fake
 * fetch, as the static site serves it, the traces of each demo scenario by scenario id (never by
 * position: the tape grows with the scenario pack), and a manual clock for the replay player.
 */
import type { ReplayClock } from '@/data/replay/player';
import { DEFAULT_BUILD_OPTIONS, traceSeed, uuidv7 } from '@/data/replay/recorder';
import type { Recording, TraceDetail, TraceSummary } from '@/types/api';

const files = import.meta.glob<string>('../../demo-data/simulated/**/*.json', { query: '?raw', import: 'default', eager: true });
// A moved or emptied directory must fail loudly, not serve an empty recording (D110).
if (Object.keys(files).length === 0) throw new Error('src/test/recording.ts found no files under demo-data/simulated');

/** "manifest.json" and "traces/<id>.json" → file text. */
export const RECORDING_FILES: Readonly<Record<string, string>> = Object.fromEntries(
  Object.entries(files).map(([path, text]) => [path.slice(path.indexOf('/simulated/') + '/simulated/'.length), text]),
);

export function committedManifest(): Recording {
  return JSON.parse(RECORDING_FILES['manifest.json']) as Recording;
}

/**
 * The committed traces of a demo scenario (contracts/demo/scenarios.v1.json id), oldest first: one
 * per round. A trace's id carries its scenario's seed (the recorder's traceSeed) in its random
 * bits, so the lookup needs no position and no text match. Empty for a scenario the tape skips.
 */
export function scenarioTraces(scenarioId: string, manifest: Recording = committedManifest()): TraceSummary[] {
  const tails = new Set<string>();
  for (let round = 0; round < DEFAULT_BUILD_OPTIONS.rounds; round += 1) tails.add(uuidv7(0, traceSeed(scenarioId, round)).slice(13));
  return manifest.traces.filter((t) => tails.has(t.trace_id.slice(13)));
}

/** A scenario's trace in one round (default the first); throws when the tape does not have it. */
export function scenarioTrace(scenarioId: string, round = 0, manifest: Recording = committedManifest()): TraceSummary {
  const tail = uuidv7(0, traceSeed(scenarioId, round)).slice(13);
  const found = manifest.traces.find((t) => t.trace_id.slice(13) === tail);
  if (!found) throw new Error(`the committed recording has no round-${round} trace of scenario ${scenarioId}`);
  return found;
}

/** A committed trace file (traces/<id>.json). */
export function committedDetail(traceId: string): TraceDetail {
  const text = RECORDING_FILES[`traces/${traceId}.json`];
  if (text === undefined) throw new Error(`the committed recording has no traces/${traceId}.json`);
  return JSON.parse(text) as TraceDetail;
}

export interface RecordingFetch {
  fetch: typeof fetch;
  requests: string[];
}

/**
 * Serves `${base}demo/<file>` from the committed files (or `overrides`, raw text), 404
 * for anything else. `manifest` replaces manifest.json with a document.
 */
export function recordingFetch(base = '/TrustAgent/', opts: { manifest?: unknown; fail?: boolean } = {}): RecordingFetch {
  const requests: string[] = [];
  const prefix = `${base}demo/`;
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
