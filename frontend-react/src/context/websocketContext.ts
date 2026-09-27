import { createContext, useContext } from 'react';
import type { ConnectionStatus, WSMessage } from '../types/audit';

export interface WebSocketContextValue {
  status: ConnectionStatus;
  lastMessage: WSMessage | null;
  send: (data: object) => void;
}

export const WebSocketContext = createContext<WebSocketContextValue | null>(null);

export function useWebSocketContext(): WebSocketContextValue {
  const ctx = useContext(WebSocketContext);
  if (!ctx) {
    throw new Error('useWebSocketContext must be used within <WebSocketProvider>');
  }
  return ctx;
}
