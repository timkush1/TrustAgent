/**
 * A controllable stand-in for the browser WebSocket (implements SocketLike). Tests drive the
 * lifecycle explicitly: open(), message(), serverClose().
 */
import type { SocketLike } from '@/data/live/streamClient';

export class FakeWebSocket implements SocketLike {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readyState = FakeWebSocket.CONNECTING;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  closedWith: { code?: number; reason?: string } | null = null;
  readonly url: URL;

  constructor(url: string) {
    this.url = new URL(url);
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.({});
  }

  /** Sends one text frame; objects are JSON-encoded. */
  message(data: unknown): void {
    this.onmessage?.({ data: typeof data === 'string' ? data : JSON.stringify(data) });
  }

  /** The server (or network) closes the connection. */
  serverClose(code = 1006): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.onerror?.({});
    this.onclose?.({ code });
  }

  close(code?: number, reason?: string): void {
    this.closedWith = { code, reason };
    this.readyState = FakeWebSocket.CLOSED;
  }
}

/** Records every socket the client creates. */
export function socketFactory() {
  const sockets: FakeWebSocket[] = [];
  return {
    sockets,
    create: (url: string) => {
      const ws = new FakeWebSocket(url);
      sockets.push(ws);
      return ws;
    },
    get last(): FakeWebSocket {
      const ws = sockets.at(-1);
      if (!ws) throw new Error('no socket created yet');
      return ws;
    },
  };
}

/** Manual lifecycle events (online / tab visible). */
export function fakeLifecycle() {
  const online = new Set<() => void>();
  const visible = new Set<() => void>();
  return {
    source: {
      addOnline: (fn: () => void) => {
        online.add(fn);
        return () => online.delete(fn);
      },
      addVisible: (fn: () => void) => {
        visible.add(fn);
        return () => visible.delete(fn);
      },
    },
    goOnline: () => online.forEach((fn) => fn()),
    becomeVisible: () => visible.forEach((fn) => fn()),
    listeners: () => online.size + visible.size,
  };
}
