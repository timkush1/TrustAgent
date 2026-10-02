/**
 * The evaluator plane of the control plane (SPEC §2.4 #12, #30-#33, #39-#46b; P4-PLAN D54-D63):
 * evaluator writes, the label queue and labels, calibration records (import, fit, activate,
 * deactivate) and the glass-box decision request. #51 (evidence suggestions) is already live on the
 * P3 ControlPlane as `getEvidenceSuggestions`.
 *
 * The signatures are fixed by Step 0b (P4-PLAN §0 rule 3); the bodies and EVALUATOR_SHAPES are
 * WEB-EV's (EVW-1). Labels answer 204, so their methods resolve with nothing.
 */
import type {
  CalibrationFitRequest,
  CalibrationFitResponse,
  CalibrationImportRequest,
  CalibrationImportResult,
  CalibrationList,
  CalibrationRecord,
  CalibrationRecordMeta,
  DecisionRequestView,
  EvaluatorArchiveRequest,
  EvaluatorCreateRequest,
  EvaluatorMeta,
  EvaluatorValidateRequest,
  EvaluatorValidateResult,
  EvaluatorVersion,
  EvaluatorVersionCreateRequest,
  LabelDelete,
  LabelPut,
  LabelQueue,
  TimeWindow,
} from '@/types/api';
import type { HttpClient } from '../http';
import { normalizeShaped, type NestedShape } from '../normalize';
import { enc, rejectNotImplemented } from './shared';

/** GET /api/traces/:id/decision-request (#12): attempt 1 compiles the final response text. */
export interface DecisionRequestParams {
  attempt?: 0 | 1;
}

/** GET /api/labels/queue (#39, D60): default window 7 d, limit at most 50. */
export interface LabelQueueParams {
  /** "<id>@<version>". */
  evaluator?: string;
  window?: TimeWindow;
  limit?: number;
}

/** GET /api/calibration (#42). */
export interface CalibrationListParams {
  snapshot?: string;
  active?: boolean;
}

export interface EvaluatorPlane {
  /** GET /api/traces/:id/decision-request (#12, content scope): the glass box; 409 `body_expired`. */
  getDecisionRequest(traceId: string, params?: DecisionRequestParams, signal?: AbortSignal): Promise<DecisionRequestView>;

  /** POST /api/evaluators/validate (#30): schema errors and lints (Go is the reference, D54). */
  validateEvaluator(request: EvaluatorValidateRequest, signal?: AbortSignal): Promise<EvaluatorValidateResult>;
  /** POST /api/evaluators (#31): 201; 409 when the id exists. */
  createEvaluator(request: EvaluatorCreateRequest): Promise<EvaluatorVersion>;
  /** POST /api/evaluators/:id/versions (#32): 201; 403 built-in, 409 `file_managed`, 412 stale `base_version`. */
  createEvaluatorVersion(id: string, request: EvaluatorVersionCreateRequest): Promise<EvaluatorVersion>;
  /** POST /api/evaluators/:id/archive (#33): 409 while a route uses it. */
  archiveEvaluator(id: string, request: EvaluatorArchiveRequest): Promise<EvaluatorMeta>;

  /** GET /api/labels/queue (#39, admin): the uncertain band first. */
  getLabelQueue(params: LabelQueueParams, signal?: AbortSignal): Promise<LabelQueue>;
  /** PUT /api/labels (#40): 204; 409 `body_expired`. */
  putLabel(request: LabelPut): Promise<void>;
  /** DELETE /api/labels (#41): 204 (Undo). */
  deleteLabel(request: LabelDelete): Promise<void>;

  /** GET /api/calibration (#42). */
  listCalibrations(params: CalibrationListParams, signal?: AbortSignal): Promise<CalibrationList>;
  /** GET /api/calibration/:id (#43). */
  getCalibration(id: number, signal?: AbortSignal): Promise<CalibrationRecord>;
  /** POST /api/calibration (#44): quality is re-derived on the server. */
  importCalibration(request: CalibrationImportRequest): Promise<CalibrationImportResult>;
  /** POST /api/calibration/fit (#45): the adopt flow fits with `activate: false` first (D62). */
  fitCalibration(request: CalibrationFitRequest): Promise<CalibrationFitResponse>;
  /** POST /api/calibration/:id/deactivate (#46). */
  deactivateCalibration(id: number): Promise<CalibrationRecordMeta>;
  /** POST /api/calibration/:id/activate (#46b, D62): after "Preview impact" with `calibration_ids`. */
  activateCalibration(id: number): Promise<CalibrationRecordMeta>;
}

/** What each evaluator-plane response must carry (see normalizeShaped). */
export const EVALUATOR_SHAPES = {
  decisionRequest: { lists: ['requests', 'request_sha256'] },
  validate: { lists: ['errors', 'lints'] },
  evaluatorVersion: { strings: ['evaluator_id'], records: ['spec'] },
  evaluatorMeta: { strings: ['id', 'name'], lists: ['attached_routes'] },
  labelQueue: { lists: ['items'], each: { items: { strings: ['trace_id', 'evaluator_ref'], lists: ['evidence_labels'] } } },
  calibrations: { lists: ['items'], each: { items: { strings: ['target_key', 'snapshot', 'quality', 'origin'] } } },
  calibrationMeta: { strings: ['target_key', 'snapshot', 'quality', 'origin'] },
  calibrationRecord: { strings: ['target_key', 'snapshot', 'quality', 'origin'], records: ['entry'] },
  calibrationImport: { lists: ['imported', 'rejected'] },
  calibrationFit: {
    records: ['record', 'reliability', 'metrics', 'recommended'],
    inner: { record: { strings: ['target_key', 'quality'], records: ['entry'] }, reliability: { lists: ['bins'], records: ['n_by_source'] } },
  },
} as const satisfies Record<string, NestedShape>;

/** Fitting reads every label of a target and may take longer than an ordinary request. */
export const CALIBRATION_FIT_TIMEOUT_MS = 60_000;

/** The evaluator plane over HTTP. */
export function liveEvaluatorPlane(http: HttpClient): EvaluatorPlane {
  return {
    async getDecisionRequest(traceId, params, signal) {
      const raw = await http.get<unknown>(`/api/traces/${enc(traceId)}/decision-request`, {
        query: { attempt: params?.attempt },
        signal,
      });
      return normalizeShaped<DecisionRequestView>(raw, 'GET /api/traces/:id/decision-request', EVALUATOR_SHAPES.decisionRequest);
    },

    async validateEvaluator(request, signal) {
      const raw = await http.request<unknown>('/api/evaluators/validate', { method: 'POST', body: request, signal });
      return normalizeShaped<EvaluatorValidateResult>(raw, 'POST /api/evaluators/validate', EVALUATOR_SHAPES.validate);
    },
    async createEvaluator(request) {
      const raw = await http.request<unknown>('/api/evaluators', { method: 'POST', body: request });
      return normalizeShaped<EvaluatorVersion>(raw, 'POST /api/evaluators', EVALUATOR_SHAPES.evaluatorVersion);
    },
    async createEvaluatorVersion(id, request) {
      const raw = await http.request<unknown>(`/api/evaluators/${enc(id)}/versions`, { method: 'POST', body: request });
      return normalizeShaped<EvaluatorVersion>(raw, 'POST /api/evaluators/:id/versions', EVALUATOR_SHAPES.evaluatorVersion);
    },
    async archiveEvaluator(id, request) {
      const raw = await http.request<unknown>(`/api/evaluators/${enc(id)}/archive`, { method: 'POST', body: request });
      return normalizeShaped<EvaluatorMeta>(raw, 'POST /api/evaluators/:id/archive', EVALUATOR_SHAPES.evaluatorMeta);
    },

    async getLabelQueue(params, signal) {
      const raw = await http.get<unknown>('/api/labels/queue', {
        query: { evaluator: params.evaluator, window: params.window, limit: params.limit },
        signal,
      });
      return normalizeShaped<LabelQueue>(raw, 'GET /api/labels/queue', EVALUATOR_SHAPES.labelQueue);
    },
    async putLabel(request) {
      await http.request<unknown>('/api/labels', { method: 'PUT', body: request });
    },
    async deleteLabel(request) {
      await http.request<unknown>('/api/labels', { method: 'DELETE', body: request });
    },

    async listCalibrations(params, signal) {
      const raw = await http.get<unknown>('/api/calibration', {
        query: { snapshot: params.snapshot, active: params.active },
        signal,
      });
      return normalizeShaped<CalibrationList>(raw, 'GET /api/calibration', EVALUATOR_SHAPES.calibrations);
    },
    async getCalibration(id, signal) {
      const raw = await http.get<unknown>(`/api/calibration/${id}`, { signal });
      return normalizeShaped<CalibrationRecord>(raw, 'GET /api/calibration/:id', EVALUATOR_SHAPES.calibrationRecord);
    },
    async importCalibration(request) {
      const raw = await http.request<unknown>('/api/calibration', { method: 'POST', body: request });
      return normalizeShaped<CalibrationImportResult>(raw, 'POST /api/calibration', EVALUATOR_SHAPES.calibrationImport);
    },
    async fitCalibration(request) {
      const raw = await http.request<unknown>('/api/calibration/fit', {
        method: 'POST',
        body: request,
        timeoutMs: CALIBRATION_FIT_TIMEOUT_MS,
      });
      return normalizeShaped<CalibrationFitResponse>(raw, 'POST /api/calibration/fit', EVALUATOR_SHAPES.calibrationFit);
    },
    async deactivateCalibration(id) {
      const raw = await http.request<unknown>(`/api/calibration/${id}/deactivate`, { method: 'POST' });
      return normalizeShaped<CalibrationRecordMeta>(raw, 'POST /api/calibration/:id/deactivate', EVALUATOR_SHAPES.calibrationMeta);
    },
    async activateCalibration(id) {
      const raw = await http.request<unknown>(`/api/calibration/${id}/activate`, { method: 'POST' });
      return normalizeShaped<CalibrationRecordMeta>(raw, 'POST /api/calibration/:id/activate', EVALUATOR_SHAPES.calibrationMeta);
    },
  };
}

/** An evaluator plane that serves nothing (the static build, tests): every call rejects with 404 `not_implemented`. */
export function unavailableEvaluatorPlane(message?: string): EvaluatorPlane {
  const reject = rejectNotImplemented(message);
  return {
    getDecisionRequest: reject,
    validateEvaluator: reject,
    createEvaluator: reject,
    createEvaluatorVersion: reject,
    archiveEvaluator: reject,
    getLabelQueue: reject,
    putLabel: reject,
    deleteLabel: reject,
    listCalibrations: reject,
    getCalibration: reject,
    importCalibration: reject,
    fitCalibration: reject,
    deactivateCalibration: reject,
    activateCalibration: reject,
  };
}
