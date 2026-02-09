import { useState } from 'react';
import type { AuditResult } from '../../types/audit';
import { TrustScoreGauge } from '../dashboard/TrustScoreGauge';
import { ClaimBreakdown } from './ClaimBreakdown';

interface AuditDetailProps {
  audit: AuditResult;
  onClose: () => void;
}

export function AuditDetail({ audit, onClose }: AuditDetailProps) {
  const [activeTab, setActiveTab] = useState<'claims' | 'response' | 'trace'>('claims');

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm">
      <div 
        className="w-full max-w-4xl max-h-[90vh] overflow-hidden rounded-xl border border-cyan-500/30"
        style={{ 
          backgroundColor: 'var(--bg-secondary)',
          boxShadow: '0 0 30px rgba(0, 255, 255, 0.1)',
        }}
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <div className="flex items-center gap-4">
            <TrustScoreGauge score={audit.overall_score} size="sm" showLabel={false} />
            <div>
              <h2 className="text-lg font-semibold text-cyan-400">
                Audit Detail
              </h2>
              <p className="text-xs text-gray-500 font-mono">
                {audit.audit_id}
              </p>
            </div>
          </div>
          
          <button
            onClick={onClose}
            className="w-8 h-8 flex items-center justify-center rounded-full bg-gray-700 hover:bg-gray-600 text-gray-300 hover:text-white transition-colors"
          >
            ✕
          </button>
        </div>

        {/* Query Section */}
        <div className="p-4 border-b border-gray-700 bg-gray-900/50">
          <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">
            User Query
          </h3>
          <p className="text-sm text-gray-200">
            {audit.user_query || 'No query captured'}
          </p>
        </div>

        {/* Tabs */}
        <div className="flex border-b border-gray-700">
          {(['claims', 'response', 'trace'] as const).map((tab) => (
            <button
              key={tab}
              onClick={() => setActiveTab(tab)}
              className={`px-4 py-2 text-sm font-medium transition-colors ${
                activeTab === tab
                  ? 'text-cyan-400 border-b-2 border-cyan-400'
                  : 'text-gray-500 hover:text-gray-300'
              }`}
            >
              {tab === 'claims' && `Claims (${audit.claims?.length || 0})`}
              {tab === 'response' && 'LLM Response'}
              {tab === 'trace' && 'Reasoning'}
            </button>
          ))}
        </div>

        {/* Tab Content */}
        <div className="p-4 overflow-y-auto" style={{ maxHeight: 'calc(90vh - 280px)' }}>
          {activeTab === 'claims' && (
            <ClaimBreakdown claims={audit.claims || []} />
          )}
          
          {activeTab === 'response' && (
            <div className="text-sm text-gray-300 whitespace-pre-wrap leading-relaxed p-4 bg-gray-900/50 rounded-lg">
              {audit.llm_response || 'No response captured'}
            </div>
          )}
          
          {activeTab === 'trace' && (
            <div className="text-sm text-gray-400 font-mono whitespace-pre-wrap p-4 bg-gray-900/50 rounded-lg">
              {audit.reasoning_trace || 'No reasoning trace available'}
            </div>
          )}
        </div>

        {/* Footer Stats */}
        <div className="flex items-center justify-between p-4 border-t border-gray-700 bg-gray-900/50 text-xs text-gray-500">
          <div className="flex gap-4">
            <span>Provider: <span className="text-gray-300">{audit.provider || 'unknown'}</span></span>
            <span>Model: <span className="text-gray-300">{audit.model || 'unknown'}</span></span>
          </div>
          <div className="flex gap-4">
            <span>Processing: <span className="text-cyan-400">{audit.processing_time_ms}ms</span></span>
            <span>Timestamp: <span className="text-gray-300">{new Date(audit.timestamp).toLocaleString()}</span></span>
          </div>
        </div>
      </div>
    </div>
  );
}
