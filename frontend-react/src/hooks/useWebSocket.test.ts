import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useWebSocket } from './useWebSocket';

class MockWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  static instances: MockWebSocket[] = [];

  url: string;
  readyState: number = MockWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onerror: ((error: unknown) => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = MockWebSocket.CLOSED;
  });

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }
}

function lastSocket(): MockWebSocket {
  return MockWebSocket.instances[MockWebSocket.instances.length - 1];
}

beforeEach(() => {
  MockWebSocket.instances = [];
  vi.stubGlobal('WebSocket', MockWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('useWebSocket', () => {
  it('opens a connection on mount and reports connected on open', () => {
    const { result } = renderHook(() => useWebSocket());

    expect(MockWebSocket.instances).toHaveLength(1);
    expect(result.current.status.connected).toBe(false);

    act(() => {
      lastSocket().readyState = MockWebSocket.OPEN;
      lastSocket().onopen?.();
    });

    expect(result.current.status.connected).toBe(true);
  });

  it('parses incoming JSON messages into lastMessage', () => {
    const { result } = renderHook(() => useWebSocket());
    const message = { type: 'audit_result', timestamp: 'now', data: undefined };

    act(() => {
      lastSocket().onmessage?.({ data: JSON.stringify(message) });
    });

    expect(result.current.lastMessage).toEqual(message);
  });

  it('ignores malformed JSON without crashing', () => {
    const { result } = renderHook(() => useWebSocket());

    act(() => {
      lastSocket().onmessage?.({ data: 'not-json{{' });
    });

    expect(result.current.lastMessage).toBeNull();
  });

  it('reconnects after the socket closes', () => {
    vi.useFakeTimers();
    renderHook(() => useWebSocket());
    expect(MockWebSocket.instances).toHaveLength(1);

    act(() => {
      lastSocket().onclose?.({ code: 1006, reason: 'abnormal' });
    });
    act(() => {
      vi.advanceTimersByTime(3000);
    });

    expect(MockWebSocket.instances).toHaveLength(2);
  });

  it('marks the connection as down after close', () => {
    const { result } = renderHook(() => useWebSocket());

    act(() => {
      lastSocket().readyState = MockWebSocket.OPEN;
      lastSocket().onopen?.();
    });
    expect(result.current.status.connected).toBe(true);

    act(() => {
      lastSocket().onclose?.({ code: 1000, reason: 'normal' });
    });
    expect(result.current.status.connected).toBe(false);
  });

  it('send() serializes payloads only when the socket is open', () => {
    const { result } = renderHook(() => useWebSocket());
    const socket = lastSocket();

    act(() => {
      result.current.send({ hello: 'world' });
    });
    expect(socket.send).not.toHaveBeenCalled();

    socket.readyState = MockWebSocket.OPEN;
    act(() => {
      result.current.send({ hello: 'world' });
    });
    expect(socket.send).toHaveBeenCalledWith(JSON.stringify({ hello: 'world' }));
  });

  it('closes the socket on unmount', () => {
    const { unmount } = renderHook(() => useWebSocket());
    const socket = lastSocket();

    unmount();

    expect(socket.close).toHaveBeenCalled();
  });
});
