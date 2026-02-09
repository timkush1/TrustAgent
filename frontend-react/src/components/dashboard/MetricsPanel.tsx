import { useAuditStore } from '../../stores/auditStore';
import { TrustScoreGauge } from './TrustScoreGauge';

export function MetricsPanel() {
  const getStats = useAuditStore((state) => state.getStats);
  const stats = getStats();

  return (
    <div className="p-4 border-b border-gray-700">
      <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-4">
        Session Metrics
      </h2>
      
      <div className="flex items-center justify-around gap-4">
        {/* Average Trust Score */}
        <div className="flex flex-col items-center">
          <TrustScoreGauge 
            score={stats.avgScore || 0} 
            size="md"
            showLabel={true}
          />
          <span className="text-xs text-gray-500 mt-2">Average Score</span>
        </div>

        {/* Stats Cards */}
        <div className="grid grid-cols-2 gap-3">
          <StatCard
            label="Total Audits"
            value={stats.total}
            color="#00ffff"
          />
          <StatCard
            label="Hallucinations"
            value={stats.hallucinationCount}
            color="#ff3366"
          />
          <StatCard
            label="Hall. Rate"
            value={`${Math.round(stats.hallucinationRate * 100)}%`}
            color={stats.hallucinationRate > 0.2 ? '#ff3366' : '#00ff88'}
          />
          <StatCard
            label="Clean"
            value={stats.total - stats.hallucinationCount}
            color="#00ff88"
          />
        </div>
      </div>
    </div>
  );
}

interface StatCardProps {
  label: string;
  value: number | string;
  color: string;
}

function StatCard({ label, value, color }: StatCardProps) {
  return (
    <div
      className="p-3 rounded-lg border"
      style={{
        backgroundColor: `${color}08`,
        borderColor: `${color}30`,
      }}
    >
      <div
        className="text-2xl font-bold"
        style={{ color, textShadow: `0 0 10px ${color}40` }}
      >
        {value}
      </div>
      <div className="text-xs text-gray-500 mt-1">{label}</div>
    </div>
  );
}
