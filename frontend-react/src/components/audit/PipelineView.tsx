interface PipelineViewProps {
  stepTimings?: Record<string, number>;
}

const PIPELINE_STEPS = [
  { key: 'decompose_ms', label: 'Decompose', icon: '📝' },
  { key: 'retrieve_ms', label: 'Retrieve', icon: '🔍' },
  { key: 'verify_ms', label: 'Verify', icon: '✓' },
  { key: 'score_ms', label: 'Score', icon: '📊' },
];

function formatMs(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`;
  return `${ms}ms`;
}

export function PipelineView({ stepTimings }: PipelineViewProps) {
  if (!stepTimings || Object.keys(stepTimings).length === 0) {
    return null;
  }

  const totalMs = Object.values(stepTimings).reduce((sum, v) => sum + v, 0);

  return (
    <div className="flex items-center gap-1 px-4 py-3 overflow-x-auto">
      {PIPELINE_STEPS.map((step, idx) => {
        const ms = stepTimings[step.key];
        const completed = ms !== undefined;

        return (
          <div key={step.key} className="flex items-center">
            {/* Step node */}
            <div
              className="flex flex-col items-center px-3 py-2 rounded-lg border min-w-[90px]"
              style={{
                borderColor: completed ? '#06b6d4' : '#374151',
                backgroundColor: completed ? 'rgba(6, 182, 212, 0.08)' : 'rgba(55, 65, 81, 0.3)',
              }}
            >
              <span className="text-sm">{step.icon}</span>
              <span
                className="text-xs font-medium mt-1"
                style={{ color: completed ? '#06b6d4' : '#6b7280' }}
              >
                {step.label}
              </span>
              {completed && (
                <span className="text-[10px] text-cyan-300 mt-0.5 font-mono">
                  {formatMs(ms)}
                </span>
              )}
            </div>

            {/* Arrow connector */}
            {idx < PIPELINE_STEPS.length - 1 && (
              <div
                className="mx-1 text-xs"
                style={{ color: completed ? '#06b6d4' : '#374151' }}
              >
                →
              </div>
            )}
          </div>
        );
      })}

      {/* Total time */}
      <div className="ml-3 pl-3 border-l border-gray-700">
        <span className="text-[10px] text-gray-500 block">Total</span>
        <span className="text-xs text-cyan-400 font-mono">{formatMs(totalMs)}</span>
      </div>
    </div>
  );
}
