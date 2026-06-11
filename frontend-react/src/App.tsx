import { useState } from 'react';
import { useWebSocket } from './hooks/useWebSocket';
import { Header } from './components/layout/Header';
import { MetricsPanel } from './components/dashboard/MetricsPanel';
import { AuditFeed } from './components/audit/AuditFeed';
import { QueryInput } from './components/audit/QueryInput';
import { FileUpload } from './components/upload/FileUpload';
import { HistoryView } from './components/history/HistoryView';
import { KnowledgeBaseView } from './components/kb/KnowledgeBaseView';

type View = 'live' | 'history' | 'kb';

function App() {
  const { status } = useWebSocket();
  const [view, setView] = useState<View>('live');

  return (
    <div 
      className="min-h-screen flex flex-col"
      style={{ backgroundColor: 'var(--bg-primary)' }}
    >
      {/* Header */}
      <Header connectionStatus={status} />

      {/* Main Content */}
      <div className="flex-1 flex">
        {/* Left Panel - Metrics */}
        <aside 
          className="w-80 flex-shrink-0 border-r"
          style={{ 
            backgroundColor: 'var(--bg-secondary)',
            borderColor: 'var(--border-color)',
          }}
        >
          <QueryInput />
          <FileUpload />
          <MetricsPanel />

          {/* Quick Stats */}
          <div className="p-4">
            <h3 className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-3">
              System Status
            </h3>
            <div className="space-y-2 text-sm">
              <StatusRow label="Proxy" status="running" port="8080" />
              <StatusRow label="WebSocket" status={status.connected ? 'connected' : 'disconnected'} port="8081" />
              <StatusRow label="Python Engine" status="running" port="50051" />
            </div>
          </div>
        </aside>

        {/* Main - Live Feed / History */}
        <main
          className="flex-1"
          style={{ backgroundColor: 'var(--bg-primary)' }}
        >
          <div
            className="flex gap-1 px-6 pt-4"
            role="tablist"
            aria-label="Audit views"
          >
            <ViewTab label="Live Feed" active={view === 'live'} onClick={() => setView('live')} />
            <ViewTab label="History" active={view === 'history'} onClick={() => setView('history')} />
            <ViewTab label="Knowledge Base" active={view === 'kb'} onClick={() => setView('kb')} />
          </div>
          {view === 'live' && <AuditFeed />}
          {view === 'history' && <HistoryView />}
          {view === 'kb' && <KnowledgeBaseView />}
        </main>
      </div>
    </div>
  );
}

interface ViewTabProps {
  label: string;
  active: boolean;
  onClick: () => void;
}

function ViewTab({ label, active, onClick }: ViewTabProps) {
  return (
    <button
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={`px-4 py-2 text-sm rounded-t border-b-2 transition-colors ${
        active
          ? 'border-cyan-400 text-cyan-300'
          : 'border-transparent text-gray-500 hover:text-gray-300'
      }`}
    >
      {label}
    </button>
  );
}

interface StatusRowProps {
  label: string;
  status: string;
  port: string;
}

function StatusRow({ label, status, port }: StatusRowProps) {
  const isUp = status === 'running' || status === 'connected';
  
  return (
    <div className="flex items-center justify-between py-1">
      <span className="text-gray-400">{label}</span>
      <div className="flex items-center gap-2">
        <span className="text-gray-600 font-mono text-xs">:{port}</span>
        <span
          className={`px-2 py-0.5 text-xs rounded ${
            isUp 
              ? 'bg-green-500/20 text-green-400' 
              : 'bg-red-500/20 text-red-400'
          }`}
        >
          {status}
        </span>
      </div>
    </div>
  );
}

export default App;
