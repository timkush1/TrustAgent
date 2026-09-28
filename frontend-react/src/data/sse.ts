/**
 * A server-sent events reader (WHATWG "text/event-stream" parsing) for REST endpoints that answer
 * with a stream: the Playground's `stream: true` run (SPEC §2.4 #61) and, in P4, the Studio dry run
 * (#38). It yields one message per blank-line-terminated block; comment lines (": …") are reported
 * separately because the gateway writes its decision summary as a `: trustagent {...}` comment.
 */

export interface SseMessage {
  /** The `event:` field, or "message" when the block has none. */
  event: string;
  /** Every `data:` line of the block, joined with "\n". */
  data: string;
  id?: string;
}

export type SseItem = { kind: 'message'; message: SseMessage } | { kind: 'comment'; text: string };

/** Incremental parser: feed decoded text chunks, collect complete items. */
export class SseParser {
  private buffer = '';
  private event = '';
  private data: string[] = [];
  private id: string | undefined;

  /** Parses as much of `chunk` as forms complete lines; the rest waits for the next chunk. */
  push(chunk: string): SseItem[] {
    this.buffer += chunk;
    const items: SseItem[] = [];
    let start = 0;
    for (;;) {
      const lf = this.buffer.indexOf('\n', start);
      const cr = this.buffer.indexOf('\r', start);
      let end: number;
      let next: number;
      if (cr !== -1 && (lf === -1 || cr < lf)) {
        // A lone CR at the very end may be the first half of CRLF: wait for more input.
        if (cr === this.buffer.length - 1) break;
        end = cr;
        next = this.buffer[cr + 1] === '\n' ? cr + 2 : cr + 1;
      } else if (lf !== -1) {
        end = lf;
        next = lf + 1;
      } else {
        break;
      }
      this.line(this.buffer.slice(start, end), items);
      start = next;
    }
    this.buffer = this.buffer.slice(start);
    return items;
  }

  /** Ends the stream: a final block without its blank line is still dispatched. */
  end(): SseItem[] {
    const items: SseItem[] = [];
    if (this.buffer) this.line(this.buffer, items);
    this.buffer = '';
    this.dispatch(items);
    return items;
  }

  private line(line: string, items: SseItem[]): void {
    if (line === '') {
      this.dispatch(items);
      return;
    }
    if (line.startsWith(':')) {
      items.push({ kind: 'comment', text: line.slice(1).replace(/^ /, '') });
      return;
    }
    const colon = line.indexOf(':');
    const field = colon === -1 ? line : line.slice(0, colon);
    const value = colon === -1 ? '' : line.slice(colon + 1).replace(/^ /, '');
    switch (field) {
      case 'event':
        this.event = value;
        break;
      case 'data':
        this.data.push(value);
        break;
      case 'id':
        if (!value.includes('\0')) this.id = value;
        break;
      default:
        // `retry` and unknown fields are ignored: these streams are never resumed.
        break;
    }
  }

  private dispatch(items: SseItem[]): void {
    if (this.data.length > 0) {
      const message: SseMessage = { event: this.event || 'message', data: this.data.join('\n') };
      if (this.id !== undefined) message.id = this.id;
      items.push({ kind: 'message', message });
    }
    this.event = '';
    this.data = [];
  }
}

/** Reads a response body as SSE items until it ends or `signal` aborts. */
export async function* readSse(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<SseItem> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  const parser = new SseParser();
  const onAbort = () => void reader.cancel().catch(() => {});
  signal?.addEventListener('abort', onAbort, { once: true });
  try {
    for (;;) {
      if (signal?.aborted) return;
      const { done, value } = await reader.read();
      if (done) break;
      yield* parser.push(decoder.decode(value, { stream: true }));
    }
    yield* parser.push(decoder.decode());
    yield* parser.end();
  } finally {
    signal?.removeEventListener('abort', onAbort);
    reader.releaseLock();
  }
}
