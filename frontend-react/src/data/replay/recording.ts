/**
 * Loads a recording (SPEC §2.13: manifest.json plus traces/<id>.json) from the static site, and
 * refuses one whose label could mislead: the same rules scripts/check_recording.py enforces before
 * publishing, checked again in the browser so a hand-edited manifest never reaches a reader.
 */
import type { Recording, TraceDetail } from '@/types/api';
import { ApiError } from '../errors';

export const RECORDING_DIR = 'demo/recorded/';
export const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';
const RECORDED_LABEL = /^Recorded .+ · replayed, not live$/;

/** VITE_DEMO_DATASET: which recording kind this build may present (`auto` takes either). */
export type DemoDataset = 'auto' | 'recorded' | 'mock';

export function parseDataset(value: string | undefined): DemoDataset {
  return value === 'recorded' || value === 'mock' ? value : 'auto';
}

function unavailable(message: string, status = 0, cause?: unknown): ApiError {
  return new ApiError({ kind: status ? 'http' : 'network', status, code: 'replay_unavailable', message, cause });
}

function invalid(message: string): ApiError {
  return new ApiError({ kind: 'invalid_response', status: 200, code: 'invalid_recording', message: `The demo recording is not valid: ${message}` });
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Structural and honesty checks of a manifest (the full schema check runs in check_recording.py). */
export function checkManifest(doc: unknown, dataset: DemoDataset = 'auto'): Recording {
  if (!isObj(doc)) throw invalid('manifest.json is not an object');
  if (doc.schema !== 'trustagent.recording/v1') throw invalid(`unknown schema ${JSON.stringify(doc.schema)}`);
  if (!Array.isArray(doc.traces) || !Array.isArray(doc.events)) throw invalid('traces and events must be lists');
  if (typeof doc.recorded_at !== 'string' || Number.isNaN(Date.parse(doc.recorded_at))) throw invalid('recorded_at is not a time');
  const backend = isObj(doc.decision_model) ? doc.decision_model.backend : undefined;
  const judge = isObj(doc.judge) ? doc.judge : {};
  if (doc.kind === 'mock') {
    if (doc.label !== MOCK_LABEL) throw invalid(`a mock recording must say "${MOCK_LABEL}"`);
  } else if (doc.kind === 'recorded') {
    if (backend === 'mock' || backend === 'off') throw invalid('a recorded manifest cannot come from the mock or no decision model');
    if (judge.fallback_active === true || String(judge.provider).toLowerCase() === 'mock') throw invalid('a recorded manifest needs a real judge');
    if ((doc.traces as Record<string, unknown>[]).some((t) => t.answer_source == null)) throw invalid('every recorded trace needs an answer source');
    if (typeof doc.label !== 'string' || !RECORDED_LABEL.test(doc.label)) throw invalid('the recorded label must end "replayed, not live"');
  } else {
    throw invalid(`unknown kind ${JSON.stringify(doc.kind)}`);
  }
  if (dataset !== 'auto' && doc.kind !== dataset) {
    throw new ApiError({
      kind: 'invalid_response',
      status: 200,
      code: 'wrong_dataset',
      message: `This build presents ${dataset === 'recorded' ? 'a real recording' : 'the simulated recording'} only, but the published manifest is ${doc.kind === 'mock' ? 'simulated' : 'recorded'}.`,
    });
  }
  return doc as unknown as Recording;
}

async function getJson(url: string, fetchImpl: typeof fetch, signal?: AbortSignal): Promise<unknown> {
  let res: Response;
  try {
    res = await fetchImpl(url, { headers: { Accept: 'application/json' }, credentials: 'same-origin', signal });
  } catch (err) {
    if (signal?.aborted) throw new ApiError({ kind: 'aborted', status: 0, code: 'aborted', message: 'Request cancelled', cause: err });
    throw unavailable(`The demo recording could not be loaded (${err instanceof Error ? err.message : String(err)}).`, 0, err);
  }
  if (!res.ok) throw unavailable(`The demo recording could not be loaded (HTTP ${res.status} for ${url}).`, res.status);
  const type = res.headers.get('content-type') ?? '';
  if (!type.includes('json')) throw unavailable(`The demo recording is missing (${url} is not JSON).`, 404);
  try {
    return await res.json();
  } catch (err) {
    throw invalid(`${url} is not JSON (${err instanceof Error ? err.message : String(err)})`);
  }
}

export interface RecordingFiles {
  manifest: Recording;
  /** GET traces/<id>.json (cached). */
  detail(id: string, signal?: AbortSignal): Promise<TraceDetail>;
}

/** Fetches manifest.json under `${basePath}demo/recorded/` and serves trace files on demand. */
export async function loadRecording(basePath: string, fetchImpl: typeof fetch, dataset: DemoDataset): Promise<RecordingFiles> {
  const dir = `${basePath.replace(/\/?$/, '/')}${RECORDING_DIR}`;
  const manifest = checkManifest(await getJson(`${dir}manifest.json`, fetchImpl), dataset);
  const ids = new Set(manifest.traces.map((t) => t.trace_id));
  const cache = new Map<string, Promise<TraceDetail>>();
  return {
    manifest,
    detail(id, signal) {
      if (!ids.has(id)) {
        return Promise.reject(new ApiError({ kind: 'http', status: 404, code: 'not_found', message: 'This recording has no request with that id.' }));
      }
      let p = cache.get(id);
      if (!p) {
        p = getJson(`${dir}traces/${encodeURIComponent(id)}.json`, fetchImpl, signal).then((doc) => {
          if (!isObj(doc) || doc.trace_id !== id) throw invalid(`traces/${id}.json does not hold that trace`);
          return doc as unknown as TraceDetail;
        });
        p.catch(() => cache.delete(id));
        cache.set(id, p);
      }
      return p;
    },
  };
}
