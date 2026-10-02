/**
 * msw handlers and state for the evaluator plane (data/control/evaluatorPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-EV after Step 0b: the state is seeded from the golden
 * fixtures (contracts/fixtures/api); the handlers arrive with EVW-1.
 */
import type { HttpHandler } from 'msw';
import calibrationFitFixture from '@contracts/fixtures/api/CalibrationFitResponse.provisional.json';
import calibrationImportFixture from '@contracts/fixtures/api/CalibrationImportResult.mixed.json';
import calibrationListFixture from '@contracts/fixtures/api/CalibrationList.default.json';
import calibrationRecordFixture from '@contracts/fixtures/api/CalibrationRecord.provisional.json';
import decisionRequestFixture from '@contracts/fixtures/api/DecisionRequestView.match.json';
import validateCleanFixture from '@contracts/fixtures/api/EvaluatorValidateResult.clean.json';
import labelQueueFixture from '@contracts/fixtures/api/LabelQueue.default.json';
import type {
  CalibrationFitResponse,
  CalibrationImportResult,
  CalibrationList,
  CalibrationRecord,
  DecisionRequestView,
  EvaluatorValidateResult,
  LabelQueue,
} from '@/types/api';
import type { HandlerKit, Reply } from './kit';

export interface EvaluatorState {
  /** GET /api/traces/:id/decision-request (#12). */
  decisionRequest: Reply<DecisionRequestView>;
  /** POST /api/evaluators/validate (#30). */
  evaluatorValidate: Reply<EvaluatorValidateResult>;
  /** GET /api/labels/queue (#39). */
  labelQueue: Reply<LabelQueue>;
  /** GET /api/calibration (#42) and GET /api/calibration/:id (#43). */
  calibrations: Reply<CalibrationList>;
  calibrationRecord: Reply<CalibrationRecord>;
  /** POST /api/calibration (#44) and /api/calibration/fit (#45). */
  calibrationImport: Reply<CalibrationImportResult>;
  calibrationFit: Reply<CalibrationFitResponse>;
}

export function defaultEvaluatorState(): EvaluatorState {
  return {
    decisionRequest: structuredClone(decisionRequestFixture) as DecisionRequestView,
    evaluatorValidate: structuredClone(validateCleanFixture) as EvaluatorValidateResult,
    labelQueue: structuredClone(labelQueueFixture) as LabelQueue,
    calibrations: structuredClone(calibrationListFixture) as CalibrationList,
    calibrationRecord: structuredClone(calibrationRecordFixture) as CalibrationRecord,
    calibrationImport: structuredClone(calibrationImportFixture) as CalibrationImportResult,
    calibrationFit: structuredClone(calibrationFitFixture) as CalibrationFitResponse,
  };
}

export function evaluatorHandlers(_state: EvaluatorState, _kit: HandlerKit): HttpHandler[] {
  return [];
}
