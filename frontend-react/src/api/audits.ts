// Typed client for the audit-history REST API (backed by Postgres, Phase 4).

export interface HistoryClaim {
  claim: string;
  status: string;
  confidence: number;
  evidence: string[];
}

export interface HistoryAudit {
  audit_id: string;
  request_id: string;
  user_query: string;
  llm_response: string;
  model?: string;
  faithfulness_score: number;
  grade: string;
  hallucination_detected: boolean;
  reasoning_trace?: string;
  processing_time_ms: number;
  step_timings?: Record<string, number>;
  created_at: string;
  claims: HistoryClaim[];
}

export interface AuditListResponse {
  audits: HistoryAudit[];
  total: number;
  limit: number;
  offset: number;
}

export interface AuditListFilter {
  limit?: number;
  offset?: number;
  grade?: string;
  flagged?: boolean;
}

const API_BASE = import.meta.env.VITE_API_URL || 'http://localhost:8080';

export async function fetchAudits(filter: AuditListFilter = {}): Promise<AuditListResponse> {
  const params = new URLSearchParams();
  if (filter.limit !== undefined) params.set('limit', String(filter.limit));
  if (filter.offset !== undefined) params.set('offset', String(filter.offset));
  if (filter.grade) params.set('grade', filter.grade);
  if (filter.flagged !== undefined) params.set('flagged', String(filter.flagged));

  const query = params.toString();
  const res = await fetch(`${API_BASE}/api/audits${query ? `?${query}` : ''}`);
  if (res.status === 503) {
    throw new Error('Audit history is not available (persistence not configured)');
  }
  if (!res.ok) {
    throw new Error(`Failed to load audit history (${res.status})`);
  }
  return res.json();
}

export async function fetchAudit(auditId: string): Promise<HistoryAudit> {
  const res = await fetch(`${API_BASE}/api/audits/${encodeURIComponent(auditId)}`);
  if (!res.ok) {
    throw new Error(`Failed to load audit ${auditId} (${res.status})`);
  }
  return res.json();
}
