// Typed client for the knowledge-base REST API (VERITAS-lite, Phase 6).

export interface KBClaim {
  claim_id: string;
  claim: string;
  source_doc_id: string;
  source_excerpt: string;
  status: 'accepted' | 'quarantined' | string;
  entailment_score: number;
  conflicts_with: string[];
  ingested_at_ms: number;
}

export interface KBClaimsResponse {
  claims: KBClaim[];
  total: number;
  limit: number;
  offset: number;
}

export interface ConflictPair {
  claim_a: KBClaim;
  claim_b: KBClaim;
}

export interface KBStats {
  total_claims: number;
  accepted: number;
  quarantined: number;
  conflict_pairs: number;
}

const API_BASE = import.meta.env.VITE_API_URL || 'http://localhost:8080';

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`);
  if (res.status === 503) {
    throw new Error('Knowledge base is not available (audit engine not connected)');
  }
  if (!res.ok) {
    throw new Error(`Knowledge-base request failed (${res.status})`);
  }
  return res.json();
}

export function fetchKBClaims(
  options: { limit?: number; offset?: number; status?: string } = {}
): Promise<KBClaimsResponse> {
  const params = new URLSearchParams();
  if (options.limit !== undefined) params.set('limit', String(options.limit));
  if (options.offset !== undefined) params.set('offset', String(options.offset));
  if (options.status) params.set('status', options.status);
  const query = params.toString();
  return getJSON(`/api/kb/claims${query ? `?${query}` : ''}`);
}

export function fetchConflicts(limit = 50): Promise<{ conflicts: ConflictPair[]; total: number }> {
  return getJSON(`/api/kb/conflicts?limit=${limit}`);
}

export function fetchKBStats(): Promise<KBStats> {
  return getJSON('/api/kb/stats');
}
