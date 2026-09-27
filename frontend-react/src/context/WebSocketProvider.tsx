import { useEffect, type ReactNode } from 'react';
import { useWebSocket } from '../hooks/useWebSocket';
import { useAuditStore } from '../stores/auditStore';
import type { AuditResult } from '../types/audit';
import { WebSocketContext } from './websocketContext';

/**
 * Owns the SINGLE WebSocket connection for the whole app and is the one place
 * incoming audit_result messages are written into the store. Previously both
 * App and AuditFeed called useWebSocket(), opening two sockets and two
 * reconnect loops; consumers now read the context (useWebSocketContext).
 */
export function WebSocketProvider({ children }: { children: ReactNode }) {
  const { status, lastMessage, send } = useWebSocket();
  const addAudit = useAuditStore((s) => s.addAudit);

  useEffect(() => {
    if (lastMessage?.type === 'audit_result' && lastMessage.data) {
      addAudit(lastMessage.data as AuditResult);
    }
  }, [lastMessage, addAudit]);

  return (
    <WebSocketContext.Provider value={{ status, lastMessage, send }}>
      {children}
    </WebSocketContext.Provider>
  );
}
