import { useEffect } from 'react';
import { useAuditStore } from '../../stores/auditStore';
import { useWebSocket } from '../../hooks/useWebSocket';
import { AuditRow } from './AuditRow';
import { AuditDetail } from './AuditDetail';
import type { AuditResult } from '../../types/audit';

export function AuditFeed() {
  const { audits, selectedAudit, addAudit, selectAudit } = useAuditStore();
  const { status, lastMessage } = useWebSocket();

  // Process incoming WebSocket messages
  useEffect(() => {
    console.log('[AuditFeed] lastMessage:', lastMessage);
    if (lastMessage?.type === 'audit_result' && lastMessage.data) {
      console.log('[AuditFeed] Adding audit:', lastMessage.data);
      addAudit(lastMessage.data as AuditResult);
    }
  }, [lastMessage, addAudit]);

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <div className="flex items-center justify-between p-4 border-b border-gray-700">
        <div className="flex items-center gap-3">
          <h2 className="text-lg font-semibold text-cyan-400">
            Live Audit Feed
          </h2>
          <div className="flex items-center gap-2">
            <span
              className={`w-2 h-2 rounded-full ${
                status.connected ? 'bg-green-500 animate-pulse' : 'bg-red-500'
              }`}
            />
            <span className="text-xs text-gray-500">
              {status.connected ? 'Connected' : 'Disconnected'}
            </span>
          </div>
        </div>
        
        <span className="text-sm text-gray-500">
          {audits.length} audits
        </span>
      </div>

      {/* Feed List */}
      <div className="flex-1 overflow-y-auto p-4 space-y-2">
        {audits.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-gray-500">
            <div className="text-4xl mb-4">📡</div>
            <p className="text-sm">Waiting for audit events...</p>
            <p className="text-xs mt-2 text-gray-600">
              Make requests through the proxy to see audits appear here
            </p>
          </div>
        ) : (
          audits.map((audit) => (
            <AuditRow
              key={audit.audit_id}
              audit={audit}
              onClick={() => selectAudit(audit.audit_id)}
            />
          ))
        )}
      </div>

      {/* Detail Modal */}
      {selectedAudit && (
        <AuditDetail
          audit={selectedAudit}
          onClose={() => selectAudit(null)}
        />
      )}
    </div>
  );
}
