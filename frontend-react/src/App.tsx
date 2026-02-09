import { useWebSocket } from './hooks/useWebSocket';
import { Header } from './components/layout/Header';
import { MetricsPanel } from './components/dashboard/MetricsPanel';
import { AuditFeed } from './components/audit/AuditFeed';

function App() {
  const { status } = useWebSocket();

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

        {/* Main - Audit Feed */}
        <main 
          className="flex-1"
          style={{ backgroundColor: 'var(--bg-primary)' }}
        >
          <AuditFeed />
        </main>
      </div>
    </div>
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
