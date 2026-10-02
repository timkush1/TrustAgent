/**
 * The evaluator plane of the control plane (SPEC §2.4 #12, #30-#33, #39-#46b; P4-PLAN D54-D63):
 * evaluator writes, the label queue and labels, calibration records (import, fit, activate,
 * deactivate) and the glass-box decision request. #51 (evidence suggestions) is already live on the
 * P3 ControlPlane as `getEvidenceSuggestions`.
 *
 * Step 0b declares the signatures; every method rejects with 404 `not_implemented` until WEB-EV
 * lands its body. The signatures are fixed (P4-PLAN §0 rule 3); the bodies and EVALUATOR_SHAPES belong to WEB-EV.
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
import type { NestedShape } from '../normalize';
import { rejectNotImplemented } from './shared';

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
  labelQueue: { lists: ['items'] },
  calibrations: { lists: ['items'] },
} as const satisfies Record<string, NestedShape>;

/** The evaluator plane over HTTP. */
export function liveEvaluatorPlane(_http: HttpClient): EvaluatorPlane {
  return {
    getDecisionRequest: rejectNotImplemented(),
    validateEvaluator: rejectNotImplemented(),
    createEvaluator: rejectNotImplemented(),
    createEvaluatorVersion: rejectNotImplemented(),
    archiveEvaluator: rejectNotImplemented(),
    getLabelQueue: rejectNotImplemented(),
    putLabel: rejectNotImplemented(),
    deleteLabel: rejectNotImplemented(),
    listCalibrations: rejectNotImplemented(),
    getCalibration: rejectNotImplemented(),
    importCalibration: rejectNotImplemented(),
    fitCalibration: rejectNotImplemented(),
    deactivateCalibration: rejectNotImplemented(),
    activateCalibration: rejectNotImplemented(),
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
