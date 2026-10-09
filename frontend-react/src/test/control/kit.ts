/**
 * The pieces every control-plane msw handler file shares: the reply type (a value, an error, or
 * `undefined` for 404), the JSON reply, request recording, and body parsing. handlers.ts builds the
 * P3 handlers with them and passes a HandlerKit to each plane's handler file
 * ({security,cost,evaluator,studio}Handlers.ts). Lead-owned like handlers.ts.
 */
import { HttpResponse } from 'msw';

export interface ErrorReply {
  error: { status: number; code: string; message: string; retry_after_ms?: number; details?: Record<string, unknown> };
}

/** What a state entry answers: the value, an error, or `undefined` (404, an older proxy). */
export type Reply<T> = T | ErrorReply | undefined;

export function errorReply(
  status: number,
  code: string,
  message: string,
  retryAfterMs?: number,
  details?: Record<string, unknown>,
): ErrorReply {
  return { error: { status, code, message, retry_after_ms: retryAfterMs, details } };
}

export function isError(v: unknown): v is ErrorReply {
  return typeof v === 'object' && v !== null && 'error' in v && typeof (v as ErrorReply).error?.status === 'number';
}

/** The HTTP response for a state entry (`headers` only on success). */
export function reply<T>(value: Reply<T>, headers?: Record<string, string>): Response {
  if (value === undefined) return HttpResponse.json({ error: { code: 'not_found', message: 'Not served by this proxy.' } }, { status: 404 });
  if (isError(value)) {
    const { status, code, message, retry_after_ms, details } = value.error;
    return HttpResponse.json({ error: { code, message, retry_after_ms: retry_after_ms ?? null, details } }, { status });
  }
  return HttpResponse.json(value as object, headers ? { headers } : undefined);
}

/** A request's body: JSON when it parses, the text otherwise, and `{file}` for a multipart upload. */
export async function readBody(request: Request): Promise<unknown> {
  const type = request.headers.get('content-type') ?? '';
  if (type.includes('multipart/form-data')) {
    const form = await request.formData();
    const file = form.get('file');
    // jsdom's File crosses into Node's fetch here, so check the shape rather than the class.
    if (file && typeof file === 'object' && 'text' in file) {
      const blob = file as Blob & { name?: string };
      return { file: { name: blob.name ?? null, text: await blob.text() } };
    }
    return { file: null };
  }
  const text = await request.text();
  if (!text) return undefined;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

/** What createControlApi hands each plane's handler factory. */
export interface HandlerKit {
  /** Records the call (method, path, query, body, headers), waits `latencyMs`, and returns the URL and parsed body. */
  record(request: Request): Promise<{ url: URL; body: unknown }>;
}
