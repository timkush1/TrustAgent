import { useCallback, useEffect, useState } from 'react';
import {
  fetchConflicts,
  fetchKBClaims,
  fetchKBStats,
  type ConflictPair,
  type KBClaim,
  type KBStats,
} from '../../api/kb';

export function KnowledgeBaseView() {
  const [stats, setStats] = useState<KBStats | null>(null);
  const [claims, setClaims] = useState<KBClaim[]>([]);
  const [conflicts, setConflicts] = useState<ConflictPair[]>([]);
  const [statusFilter, setStatusFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [statsResponse, claimsResponse, conflictsResponse] = await Promise.all([
        fetchKBStats(),
        fetchKBClaims({ limit: 50, status: statusFilter || undefined }),
        fetchConflicts(),
      ]);
      setStats(statsResponse);
      setClaims(claimsResponse.claims);
      setConflicts(conflictsResponse.conflicts);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load the knowledge base');
      setStats(null);
      setClaims([]);
      setConflicts([]);
    } finally {
      setLoading(false);
    }
  }, [statusFilter]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center gap-4">
        <h2 className="text-lg font-semibold text-gray-200 flex-1">Knowledge Base</h2>
        <button
          onClick={() => void load()}
          className="px-3 py-1 text-sm rounded border text-gray-400"
          style={{ borderColor: 'var(--border-color)' }}
        >
          ↻ Refresh
        </button>
      </div>

      {loading && <p className="text-gray-500 py-8 text-center">Loading knowledge base…</p>}
      {error && !loading && (
        <p className="text-red-400 py-8 text-center" role="alert">
          {error}
        </p>
      )}

      {!loading && !error && stats && (
        <>
          {/* Stats */}
          <div className="grid grid-cols-4 gap-4">
            <StatCard label="Total claims" value={stats.total_claims} />
            <StatCard label="Accepted" value={stats.accepted} accent="text-green-400" />
            <StatCard label="Quarantined" value={stats.quarantined} accent="text-yellow-400" />
            <StatCard label="Conflicts" value={stats.conflict_pairs} accent="text-red-400" />
          </div>

          {/* Conflicts */}
          {conflicts.length > 0 && (
            <section>
              <h3 className="text-sm font-semibold text-red-400 uppercase tracking-wider mb-2">
                ⚠ Contradicting Claims
              </h3>
              <div className="space-y-2">
                {conflicts.map((pair, i) => (
                  <div
                    key={i}
                    className="rounded-lg border border-red-500/30 p-4 text-sm"
                    style={{ backgroundColor: 'var(--bg-secondary)' }}
                  >
                    <ConflictSide claim={pair.claim_a} />
                    <p className="text-xs text-red-400 my-1 font-semibold">contradicts</p>
                    <ConflictSide claim={pair.claim_b} />
                  </div>
                ))}
              </div>
            </section>
          )}

          {/* Claims */}
          <section>
            <div className="flex items-center gap-4 mb-2">
              <h3 className="text-sm font-semibold text-gray-400 uppercase tracking-wider flex-1">
                Claims
              </h3>
              <label className="text-sm text-gray-400 flex items-center gap-2">
                Status
                <select
                  value={statusFilter}
                  onChange={(e) => setStatusFilter(e.target.value)}
                  className="rounded px-2 py-1 text-sm border"
                  style={{
                    backgroundColor: 'var(--bg-secondary)',
                    borderColor: 'var(--border-color)',
                    color: 'inherit',
                  }}
                >
                  <option value="">All</option>
                  <option value="accepted">Accepted</option>
                  <option value="quarantined">Quarantined</option>
                </select>
              </label>
            </div>

            {claims.length === 0 ? (
              <p className="text-gray-500 py-6 text-center text-sm">
                No claims yet — upload documents to populate the knowledge base.
              </p>
            ) : (
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
                      <th className="px-4 py-3">Claim</th>
                      <th className="px-4 py-3">Entailment</th>
                      <th className="px-4 py-3">Status</th>
                      <th className="px-4 py-3">Source</th>
                    </tr>
                  </thead>
                  <tbody>
                    {claims.map((claim) => (
                      <tr
                        key={claim.claim_id}
                        className="border-t"
                        style={{ borderColor: 'var(--border-color)' }}
                      >
                        <td className="px-4 py-3 text-gray-300">
                          {claim.claim}
                          {claim.conflicts_with.length > 0 && (
                            <span className="ml-2 text-xs text-red-400">⚠ conflict</span>
                          )}
                        </td>
                        <td className="px-4 py-3 font-mono">
                          {Math.round(claim.entailment_score * 100)}%
                        </td>
                        <td className="px-4 py-3">
                          <span
                            className={`px-2 py-0.5 rounded text-xs font-semibold ${
                              claim.status === 'accepted'
                                ? 'bg-green-500/20 text-green-400'
                                : 'bg-yellow-500/20 text-yellow-400'
                            }`}
                          >
                            {claim.status}
                          </span>
                        </td>
                        <td
                          className="px-4 py-3 text-gray-500 text-xs max-w-xs truncate"
                          title={claim.source_excerpt}
                        >
                          {claim.source_excerpt}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}

function StatCard({ label, value, accent = 'text-gray-200' }: {
  label: string;
  value: number;
  accent?: string;
}) {
  return (
    <div
      className="rounded-lg border p-4"
      style={{ backgroundColor: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}
    >
      <p className={`text-2xl font-bold ${accent}`}>{value}</p>
      <p className="text-xs text-gray-500 uppercase tracking-wider mt-1">{label}</p>
    </div>
  );
}

function ConflictSide({ claim }: { claim: KBClaim }) {
  return (
    <p className="text-gray-300">
      {claim.claim}
      <span className="text-gray-600 text-xs ml-2" title={claim.source_excerpt}>
        (source: {claim.source_doc_id || 'unknown'})
      </span>
    </p>
  );
}
