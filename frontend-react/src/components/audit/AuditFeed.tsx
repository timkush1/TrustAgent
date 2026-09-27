import { useAuditStore } from '../../stores/auditStore';
import { useWebSocketContext } from '../../context/websocketContext';
import { AuditRow } from './AuditRow';
import { AuditDetail } from './AuditDetail';

export function AuditFeed() {
  const { audits, selectedAudit, selectAudit } = useAuditStore();
  // Connection status only; incoming audit_result messages are dispatched to
  // the store centrally by WebSocketProvider (single source of truth).
  const { status } = useWebSocketContext();

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
