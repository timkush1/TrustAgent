import { useState, useEffect, useRef, useCallback } from 'react';
import type { WSMessage, ConnectionStatus } from '../types/audit';

const WS_URL = import.meta.env.VITE_WS_URL || 'ws://localhost:8081/ws';
const RECONNECT_DELAY = 3000;
const MAX_RECONNECT_ATTEMPTS = 10;

export function useWebSocket() {
  const [status, setStatus] = useState<ConnectionStatus>({ connected: false });
  const [lastMessage, setLastMessage] = useState<WSMessage | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectAttempts = useRef(0);
  const reconnectTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      return;
    }

    try {
      const ws = new WebSocket(WS_URL);

      ws.onopen = () => {
        console.log('[WS] Connected to', WS_URL);
        setStatus({ connected: true, lastConnected: new Date() });
        reconnectAttempts.current = 0;
      };

      ws.onclose = (event) => {
        console.log('[WS] Disconnected:', event.code, event.reason);
        setStatus((prev) => ({ ...prev, connected: false }));
        
        // Auto-reconnect
        if (reconnectAttempts.current < MAX_RECONNECT_ATTEMPTS) {
          reconnectAttempts.current++;
          console.log(`[WS] Reconnecting in ${RECONNECT_DELAY}ms (attempt ${reconnectAttempts.current})`);
          reconnectTimeout.current = setTimeout(connect, RECONNECT_DELAY);
        } else {
          setStatus((prev) => ({ 
            ...prev, 
            error: 'Max reconnection attempts reached' 
          }));
        }
      };

      ws.onerror = (error) => {
        console.error('[WS] Error:', error);
        setStatus((prev) => ({ ...prev, error: 'Connection error' }));
      };

      ws.onmessage = (event) => {
        try {
          const message: WSMessage = JSON.parse(event.data);
          console.log('[WS] Received message:', message.type, message);
          setLastMessage(message);
        } catch (err) {
          console.error('[WS] Failed to parse message:', err, event.data);
        }
      };

      wsRef.current = ws;
    } catch (err) {
      console.error('[WS] Failed to connect:', err);
      setStatus({ connected: false, error: 'Failed to connect' });
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
    connect();
    return () => disconnect();
  }, [connect, disconnect]);

  return { status, lastMessage, send, reconnect: connect };
}
