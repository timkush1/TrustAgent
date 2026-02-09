import { useMemo } from 'react';

interface TrustScoreGaugeProps {
  score: number; // 0.0 - 1.0
  size?: 'sm' | 'md' | 'lg';
  showLabel?: boolean;
  animated?: boolean;
}

export function TrustScoreGauge({ 
  score, 
  size = 'md', 
  showLabel = true,
  animated = true 
}: TrustScoreGaugeProps) {
  const dimensions = useMemo(() => {
    switch (size) {
      case 'sm': return { width: 80, height: 80, strokeWidth: 6, fontSize: 16 };
      case 'lg': return { width: 200, height: 200, strokeWidth: 12, fontSize: 36 };
      default: return { width: 120, height: 120, strokeWidth: 8, fontSize: 24 };
    }
  }, [size]);

  const { width, height, strokeWidth, fontSize } = dimensions;
  const radius = (width - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = circumference - (score * circumference);

  // Color based on score
  const getColor = (score: number) => {
    if (score >= 0.8) return '#00ff88'; // Green
    if (score >= 0.6) return '#ffcc00'; // Yellow
    if (score >= 0.4) return '#ff9933'; // Orange
    return '#ff3366'; // Red
  };

  const color = getColor(score);
  const percentage = Math.round(score * 100);

  return (
    <div className="relative inline-flex items-center justify-center">
      <svg width={width} height={height} className="transform -rotate-90">
        {/* Background circle */}
        <circle
          cx={width / 2}
          cy={height / 2}
          r={radius}
          fill="none"
          stroke="#2a2a3a"
          strokeWidth={strokeWidth}
        />
        {/* Progress circle */}
        <circle
          cx={width / 2}
          cy={height / 2}
          r={radius}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeDasharray={circumference}
          strokeDashoffset={strokeDashoffset}
          style={{
            transition: animated ? 'stroke-dashoffset 0.5s ease-out, stroke 0.3s ease' : 'none',
            filter: `drop-shadow(0 0 6px ${color})`,
          }}
        />
      </svg>
      
      {/* Center text */}
      <div 
        className="absolute inset-0 flex flex-col items-center justify-center"
        style={{ color }}
      >
        <span 
          className="font-bold"
          style={{ fontSize, textShadow: `0 0 10px ${color}` }}
        >
          {percentage}
        </span>
        {showLabel && (
          <span className="text-xs text-gray-400 mt-1">TRUST</span>
        )}
      </div>
    </div>
  );
}
