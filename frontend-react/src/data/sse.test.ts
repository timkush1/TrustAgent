import { describe, expect, it } from 'vitest';
import { readSse, SseParser, type SseItem } from './sse';

function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c));
      controller.close();
    },
  });
}

async function collect(chunks: string[]): Promise<SseItem[]> {
  const out: SseItem[] = [];
  for await (const item of readSse(streamOf(chunks))) out.push(item);
  return out;
}

describe('SSE parser', () => {
  it('dispatches one message per blank-line-terminated block', () => {
    const p = new SseParser();
    expect(p.push('event: token\ndata: {"text":"Hi"}\n\n')).toEqual([
      { kind: 'message', message: { event: 'token', data: '{"text":"Hi"}' } },
    ]);
  });

  it('joins multi-line data and defaults the event name', () => {
    const p = new SseParser();
    expect(p.push('data: a\ndata: b\n\n')).toEqual([{ kind: 'message', message: { event: 'message', data: 'a\nb' } }]);
  });

  it('reports comments, including the gateway decision comment', () => {
    const p = new SseParser();
    expect(p.push(': trustagent {"action":"flag"}\n\n')).toEqual([{ kind: 'comment', text: 'trustagent {"action":"flag"}' }]);
  });

  it('handles CRLF and CR line endings, even split across chunks', () => {
    const p = new SseParser();
    expect(p.push('event: a\r')).toEqual([]);
    expect(p.push('\ndata: 1\r\n\r\nevent: b\rdata: 2\r\r')).toEqual([{ kind: 'message', message: { event: 'a', data: '1' } }]);
    // The last CR could still be the first half of a CRLF: it is settled by the next chunk or the end.
    expect(p.push('event: c\n')).toEqual([{ kind: 'message', message: { event: 'b', data: '2' } }]);
  });

  it('keeps an id and ignores retry and unknown fields', () => {
    const p = new SseParser();
    expect(p.push('id: 7\nretry: 100\nfoo: bar\ndata: x\n\n')).toEqual([{ kind: 'message', message: { event: 'message', data: 'x', id: '7' } }]);
  });

  it('dispatches a final block without its blank line at the end of the stream', async () => {
    expect(await collect(['event: done\ndata: {', '"ok":true}'])).toEqual([
      { kind: 'message', message: { event: 'done', data: '{"ok":true}' } },
    ]);
  });

  it('decodes UTF-8 split across chunks', async () => {
    const bytes = new TextEncoder().encode('data: café\n\n');
    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(bytes.slice(0, 9));
        c.enqueue(bytes.slice(9));
        c.close();
      },
    });
    const items: SseItem[] = [];
    for await (const item of readSse(stream)) items.push(item);
    expect(items).toEqual([{ kind: 'message', message: { event: 'message', data: 'café' } }]);
  });

  it('stops reading when the signal aborts', async () => {
    const controller = new AbortController();
    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new TextEncoder().encode('data: 1\n\n'));
      },
    });
    const items: SseItem[] = [];
    for await (const item of readSse(stream, controller.signal)) {
      items.push(item);
      controller.abort();
    }
    expect(items).toHaveLength(1);
  });
});
