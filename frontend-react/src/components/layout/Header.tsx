import type { ConnectionStatus } from '../../types/audit';

interface HeaderProps {
  connectionStatus: ConnectionStatus;
}

export function Header({ connectionStatus }: HeaderProps) {
  return (
    <header 
      className="flex items-center justify-between px-6 py-4 border-b"
      style={{ 
        backgroundColor: 'var(--bg-secondary)',
        borderColor: 'var(--border-color)',
      }}
    >
      {/* Logo */}
      <div className="flex items-center gap-3">
        <div 
          className="w-10 h-10 flex items-center justify-center rounded-lg text-xl"
          style={{
            background: 'linear-gradient(135deg, #00ffff20, #ff00ff20)',
            border: '1px solid #00ffff40',
          }}
        >
          🔍
        </div>
        <div>
          <h1 
            className="text-xl font-bold tracking-tight"
            style={{
              background: 'linear-gradient(90deg, #00ffff, #ff00ff)',
              WebkitBackgroundClip: 'text',
              WebkitTextFillColor: 'transparent',
            }}
          >
            TruthTable
          </h1>
          <p className="text-xs text-gray-500">AI Hallucination Control Plane</p>
        </div>
      </div>

      {/* Connection Status */}
      <div className="flex items-center gap-4">
        <div className="flex items-center gap-2">
          <span
            className={`w-3 h-3 rounded-full ${
              connectionStatus.connected 
                ? 'bg-green-500 glow-green' 
                : 'bg-red-500 glow-red'
            }`}
          />
          <span className="text-sm text-gray-400">
            {connectionStatus.connected ? 'Live' : 'Disconnected'}
          </span>
        </div>

        {connectionStatus.error && (
          <span className="text-xs text-red-400">
            {connectionStatus.error}
          </span>
        )}

        <div className="text-xs text-gray-600 font-mono">
          ws://localhost:8081
        </div>
      </div>
    </header>
  );
}
