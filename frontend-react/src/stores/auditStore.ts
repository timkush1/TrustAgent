import { create } from 'zustand';
import type { AuditResult } from '../types/audit';

const MAX_AUDITS = 100;

interface AuditState {
  audits: AuditResult[];
  selectedAudit: AuditResult | null;
  
  // Actions
  addAudit: (audit: AuditResult) => void;
  selectAudit: (auditId: string | null) => void;
  clearAudits: () => void;
  
  // Computed
  getStats: () => {
    total: number;
    avgScore: number;
    hallucinationCount: number;
    hallucinationRate: number;
  };
}

export const useAuditStore = create<AuditState>((set, get) => ({
  audits: [],
  selectedAudit: null,

  addAudit: (audit) =>
    set((state) => ({
      audits: [audit, ...state.audits].slice(0, MAX_AUDITS),
    })),

  selectAudit: (auditId) =>
    set((state) => ({
      selectedAudit: auditId 
        ? state.audits.find((a) => a.audit_id === auditId) || null 
        : null,
    })),

  clearAudits: () => set({ audits: [], selectedAudit: null }),

  getStats: () => {
    const { audits } = get();
    const total = audits.length;
    if (total === 0) {
      return { total: 0, avgScore: 0, hallucinationCount: 0, hallucinationRate: 0 };
    }
    
    const avgScore = audits.reduce((sum, a) => sum + a.overall_score, 0) / total;
    const hallucinationCount = audits.filter((a) => a.hallucination_detected).length;
    const hallucinationRate = hallucinationCount / total;
    
    return { total, avgScore, hallucinationCount, hallucinationRate };
  },
}));
