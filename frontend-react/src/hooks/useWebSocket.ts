import { useState, useEffect, useRef, useCallback } from 'react';
import type { WSMessage, ConnectionStatus } from '../types/audit';
import { getWsUrl } from '../api/client';

const RECONNECT_DELAY = 3000;
const MAX_RECONNECT_ATTEMPTS = 10;

export function useWebSocket() {
  const [status, setStatus] = useState<ConnectionStatus>({ connected: false });
  const [lastMessage, setLastMessage] = useState<WSMessage | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectAttempts = useRef(0);
  const reconnectTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Indirection so the onclose handler can schedule a reconnect without
  // referencing `connect` before its declaration.
  const connectRef = useRef<(() => void) | null>(null);

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      return;
    }

    try {
      const ws = new WebSocket(getWsUrl());

      ws.onopen = () => {
        setStatus({ connected: true, lastConnected: new Date() });
        reconnectAttempts.current = 0;
      };

      ws.onclose = () => {
        setStatus((prev) => ({ ...prev, connected: false }));

        // Auto-reconnect
        if (reconnectAttempts.current < MAX_RECONNECT_ATTEMPTS) {
          reconnectAttempts.current++;
          reconnectTimeout.current = setTimeout(() => connectRef.current?.(), RECONNECT_DELAY);
        } else {
          setStatus((prev) => ({
            ...prev,
            error: 'Max reconnection attempts reached',
          }));
        }
      };

      ws.onerror = () => {
        setStatus((prev) => ({ ...prev, error: 'Connection error' }));
      };

      ws.onmessage = (event) => {
        try {
          const message: WSMessage = JSON.parse(event.data);
          setLastMessage(message);
        } catch {
          // Ignore malformed frames; a well-behaved server never sends them.
        }
      };

      wsRef.current = ws;
    } catch {
      // Construction only throws on an invalid URL (a config bug); connection
      // failures are reported asynchronously via onerror/onclose.
    }
  }, []);

  const disconnect = useCallback(() => {
    if (reconnectTimeout.current) {
      clearTimeout(reconnectTimeout.current);
    }
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }
  }, []);

  const send = useCallback((data: object) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(data));
    }
  }, []);

  useEffect(() => {
    connectRef.current = connect;
    connect();
    return () => disconnect();
  }, [connect, disconnect]);

  return { status, lastMessage, send, reconnect: connect };
}
