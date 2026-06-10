import { useCallback, useEffect, useState } from 'react';
import { fetchAudits, type HistoryAudit } from '../../api/audits';

const PAGE_SIZE = 20;

const GRADE_COLORS: Record<string, string> = {
  A: 'bg-green-500/20 text-green-400',
  B: 'bg-blue-500/20 text-blue-400',
  C: 'bg-yellow-500/20 text-yellow-400',
  D: 'bg-orange-500/20 text-orange-400',
  F: 'bg-red-500/20 text-red-400',
};

export function HistoryView() {
  const [audits, setAudits] = useState<HistoryAudit[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [grade, setGrade] = useState('');
  const [flaggedOnly, setFlaggedOnly] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await fetchAudits({
        limit: PAGE_SIZE,
        offset,
        grade: grade || undefined,
        flagged: flaggedOnly ? true : undefined,
      });
      setAudits(response.audits);
      setTotal(response.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load audit history');
      setAudits([]);
      setTotal(0);
    } finally {
      setLoading(false);
    }
  }, [offset, grade, flaggedOnly]);

  useEffect(() => {
    void load();
  }, [load]);

  const page = Math.floor(offset / PAGE_SIZE) + 1;
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <div className="p-6">
      {/* Filters */}
      <div className="flex items-center gap-4 mb-4">
        <h2 className="text-lg font-semibold text-gray-200 flex-1">
          Audit History
          <span className="ml-2 text-sm font-normal text-gray-500">({total} audits)</span>
        </h2>

        <label className="text-sm text-gray-400 flex items-center gap-2">
          Grade
          <select
            value={grade}
            onChange={(e) => {
              setGrade(e.target.value);
              setOffset(0);
            }}
            className="rounded px-2 py-1 text-sm border"
            style={{
              backgroundColor: 'var(--bg-secondary)',
              borderColor: 'var(--border-color)',
              color: 'inherit',
            }}
          >
            <option value="">All</option>
            {['A', 'B', 'C', 'D', 'F'].map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
          </select>
        </label>

        <label className="text-sm text-gray-400 flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={flaggedOnly}
            onChange={(e) => {
              setFlaggedOnly(e.target.checked);
              setOffset(0);
            }}
          />
          Hallucinations only
        </label>
      </div>

      {/* States */}
      {loading && <p className="text-gray-500 py-8 text-center">Loading audit history…</p>}
      {error && !loading && (
        <p className="text-red-400 py-8 text-center" role="alert">
          {error}
        </p>
      )}
      {!loading && !error && audits.length === 0 && (
        <p className="text-gray-500 py-8 text-center">No audits match the current filters.</p>
      )}

      {/* Table */}
      {!loading && !error && audits.length > 0 && (
        <div
          className="rounded-lg border overflow-hidden"
          style={{ borderColor: 'var(--border-color)' }}
        >
          <table className="w-full text-sm">
            <thead>
              <tr
                className="text-left text-xs uppercase tracking-wider text-gray-500"
                style={{ backgroundColor: 'var(--bg-secondary)' }}
              >
                <th className="px-4 py-3">Time</th>
                <th className="px-4 py-3">Query</th>
                <th className="px-4 py-3">Score</th>
                <th className="px-4 py-3">Grade</th>
                <th className="px-4 py-3">Status</th>
              </tr>
            </thead>
            <tbody>
              {audits.map((audit) => (
                <HistoryRow
                  key={audit.audit_id}
                  audit={audit}
                  expanded={expandedId === audit.audit_id}
                  onToggle={() =>
                    setExpandedId(expandedId === audit.audit_id ? null : audit.audit_id)
                  }
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Pagination */}
      {!loading && !error && total > PAGE_SIZE && (
        <div className="flex items-center justify-between mt-4 text-sm text-gray-400">
          <button
            onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            disabled={offset === 0}
            className="px-3 py-1 rounded border disabled:opacity-40"
            style={{ borderColor: 'var(--border-color)' }}
          >
            ← Previous
          </button>
          <span>
            Page {page} of {pageCount}
          </span>
          <button
            onClick={() => setOffset(offset + PAGE_SIZE)}
            disabled={offset + PAGE_SIZE >= total}
            className="px-3 py-1 rounded border disabled:opacity-40"
            style={{ borderColor: 'var(--border-color)' }}
          >
            Next →
          </button>
        </div>
      )}
    </div>
  );
}

interface HistoryRowProps {
  audit: HistoryAudit;
  expanded: boolean;
  onToggle: () => void;
}

function HistoryRow({ audit, expanded, onToggle }: HistoryRowProps) {
  return (
    <>
      <tr
        onClick={onToggle}
        className="cursor-pointer border-t hover:bg-white/5"
        style={{ borderColor: 'var(--border-color)' }}
      >
        <td className="px-4 py-3 text-gray-500 whitespace-nowrap font-mono text-xs">
          {new Date(audit.created_at).toLocaleString()}
        </td>
        <td className="px-4 py-3 text-gray-300 max-w-md truncate">{audit.user_query}</td>
        <td className="px-4 py-3 font-mono">{Math.round(audit.faithfulness_score * 100)}%</td>
        <td className="px-4 py-3">
          <span
            className={`px-2 py-0.5 rounded text-xs font-semibold ${
              GRADE_COLORS[audit.grade] ?? 'bg-gray-500/20 text-gray-400'
            }`}
          >
            {audit.grade}
          </span>
        </td>
        <td className="px-4 py-3">
          {audit.hallucination_detected ? (
            <span className="text-red-400 text-xs font-semibold">⚠ HALLUCINATION</span>
          ) : (
            <span className="text-green-400 text-xs">✓ ok</span>
          )}
        </td>
      </tr>
      {expanded && (
        <tr style={{ backgroundColor: 'var(--bg-secondary)' }}>
          <td colSpan={5} className="px-6 py-4">
            <p className="text-xs text-gray-500 mb-1">Response</p>
            <p className="text-sm text-gray-300 mb-3">{audit.llm_response}</p>
            {audit.claims.length > 0 && (
              <>
                <p className="text-xs text-gray-500 mb-1">Claims ({audit.claims.length})</p>
                <ul className="space-y-1">
                  {audit.claims.map((claim, i) => (
                    <li key={i} className="text-sm flex items-start gap-2">
                      <span
                        className={
                          claim.status === 'SUPPORTED'
                            ? 'text-green-400'
                            : claim.status === 'UNSUPPORTED'
                              ? 'text-red-400'
                              : 'text-yellow-400'
                        }
                      >
                        {claim.status === 'SUPPORTED' ? '✓' : claim.status === 'UNSUPPORTED' ? '✗' : '~'}
                      </span>
                      <span className="text-gray-300">{claim.claim}</span>
                      <span className="text-gray-600 text-xs font-mono">
                        {Math.round(claim.confidence * 100)}%
                      </span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </td>
        </tr>
      )}
    </>
  );
}
