// Audit-related types matching the gRPC/WebSocket messages

export type VerificationStatus = 'UNKNOWN' | 'SUPPORTED' | 'UNSUPPORTED' | 'PARTIALLY_SUPPORTED';

export interface ClaimVerification {
  claim: string;
  status: VerificationStatus;
  confidence: number;
  evidence: string[];
}

export interface AuditResult {
  audit_id: string;
  request_id: string;
  user_query: string;
  llm_response: string;
  faithfulness_score: number;
  relevancy_score: number;
  overall_score: number;
  hallucination_detected: boolean;
  claims: ClaimVerification[];
  reasoning_trace: string;
  processing_time_ms: number;
  timestamp: string;
  provider?: string;
  model?: string;
}

export interface WSMessage {
  type: 'audit_result' | 'metric_update' | 'error' | 'pong' | 'connected';
  timestamp: string;
  data?: AuditResult | MetricUpdate | ErrorPayload;
}

export interface MetricUpdate {
  total_requests: number;
  total_audits: number;
  avg_score: number;
  hallucination_rate: number;
}

export interface ErrorPayload {
  code: string;
  message: string;
}

export interface ConnectionStatus {
  connected: boolean;
  lastConnected?: Date;
  error?: string;
}
