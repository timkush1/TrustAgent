import type { ClaimVerification, VerificationStatus } from '../../types/audit';

interface ClaimBreakdownProps {
  claims: ClaimVerification[];
}

const statusConfig: Record<VerificationStatus, { color: string; label: string; icon: string }> = {
  SUPPORTED: { color: '#00ff88', label: 'Verified', icon: '✓' },
  UNSUPPORTED: { color: '#ff3366', label: 'Hallucination', icon: '✗' },
  PARTIALLY_SUPPORTED: { color: '#ffcc00', label: 'Partial', icon: '~' },
  UNKNOWN: { color: '#888', label: 'Unknown', icon: '?' },
};

export function ClaimBreakdown({ claims }: ClaimBreakdownProps) {
  if (!claims || claims.length === 0) {
    return (
      <div className="text-gray-500 text-sm italic p-4">
        No claims extracted
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold text-gray-400 uppercase tracking-wider">
        Claim Analysis ({claims.length})
      </h3>
      
      <div className="space-y-2">
        {claims.map((claim, index) => {
          const config = statusConfig[claim.status] || statusConfig.UNKNOWN;
          
          return (
            <div
              key={index}
              className="p-3 rounded-lg border animate-slide-in"
              style={{
                backgroundColor: 'rgba(26, 26, 36, 0.8)',
                borderColor: config.color,
                borderLeftWidth: '4px',
                animationDelay: `${index * 50}ms`,
              }}
            >
              {/* Header */}
              <div className="flex items-center justify-between mb-2">
                <div className="flex items-center gap-2">
                  <span
                    className="w-6 h-6 flex items-center justify-center rounded-full text-xs font-bold"
                    style={{
                      backgroundColor: `${config.color}20`,
                      color: config.color,
                    }}
                  >
                    {config.icon}
                  </span>
                  <span
                    className="text-xs font-medium uppercase tracking-wider"
                    style={{ color: config.color }}
                  >
                    {config.label}
                  </span>
                </div>
                
                <span className="text-xs text-gray-500">
                  {Math.round(claim.confidence * 100)}% confidence
                </span>
              </div>
              
              {/* Claim text */}
              <p className="text-sm text-gray-200 leading-relaxed">
                "{claim.claim}"
              </p>
              
              {/* Evidence */}
              {claim.evidence && claim.evidence.length > 0 && (
                <div className="mt-2 pt-2 border-t border-gray-700">
                  <span className="text-xs text-gray-500 block mb-1">Evidence:</span>
                  {claim.evidence.map((ev, i) => (
                    <p key={i} className="text-xs text-gray-400 italic pl-2 border-l border-gray-600">
                      {ev}
                    </p>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
