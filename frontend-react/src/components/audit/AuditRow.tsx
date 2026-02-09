import type { AuditResult } from '../../types/audit';

interface AuditRowProps {
  audit: AuditResult;
  onClick: () => void;
}

export function AuditRow({ audit, onClick }: AuditRowProps) {
  const score = audit.overall_score;
  const scorePercent = Math.round(score * 100);
  
  // Color based on score
  const getScoreColor = (score: number) => {
    if (score >= 0.8) return '#00ff88';
    if (score >= 0.6) return '#ffcc00';
    if (score >= 0.4) return '#ff9933';
    return '#ff3366';
  };
  
  const color = getScoreColor(score);
  const time = new Date(audit.timestamp).toLocaleTimeString();

  return (
    <div
      onClick={onClick}
      className="group flex items-center gap-4 p-3 rounded-lg cursor-pointer transition-all duration-200 hover:translate-x-1 animate-slide-in"
      style={{
        backgroundColor: 'rgba(26, 26, 36, 0.6)',
        borderLeft: `3px solid ${color}`,
      }}
      onMouseEnter={(e) => {
        e.currentTarget.style.backgroundColor = 'rgba(26, 26, 36, 0.9)';
        e.currentTarget.style.boxShadow = `0 0 15px ${color}30`;
      }}
      onMouseLeave={(e) => {
        e.currentTarget.style.backgroundColor = 'rgba(26, 26, 36, 0.6)';
        e.currentTarget.style.boxShadow = 'none';
      }}
    >
      {/* Score Badge */}
      <div
        className="flex-shrink-0 w-12 h-12 flex items-center justify-center rounded-lg font-bold text-lg"
        style={{
          backgroundColor: `${color}15`,
          color: color,
          textShadow: `0 0 10px ${color}`,
        }}
      >
        {scorePercent}
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 mb-1">
          {audit.hallucination_detected && (
            <span className="px-2 py-0.5 text-xs font-semibold rounded bg-red-500/20 text-red-400 uppercase tracking-wider">
              ⚠ Hallucination
            </span>
          )}
          <span className="text-xs text-gray-500">
            {audit.claims?.length || 0} claims
          </span>
        </div>
        
        <p className="text-sm text-gray-300 truncate">
          {audit.user_query || 'No query'}
        </p>
      </div>

      {/* Meta */}
      <div className="flex-shrink-0 text-right">
        <div className="text-xs text-gray-500">{time}</div>
        <div className="text-xs text-gray-600 font-mono mt-1">
          {audit.processing_time_ms}ms
        </div>
      </div>

      {/* Arrow */}
      <div className="flex-shrink-0 text-gray-600 group-hover:text-cyan-400 transition-colors">
        →
      </div>
    </div>
  );
}
