/**
 * The TrustAgent decision core in TypeScript (SPEC §2.9-§2.11): canonical JSON, token estimates,
 * the question-set compiler, answer normalization, calibration apply and fitting, decide(), the
 * Tier-0 detectors, evidence extraction and evaluator lint.
 *
 * CONTRACT-OWNED (SPEC §3.3). This is a port of backend-go/internal/decision (the reference) and
 * internal/evidence; both are held to the same behaviour by the golden vectors in contracts/vectors
 * (vectors/vectors.test.ts). A change that alters any output is a contract change: it needs a new
 * vector, and Go changes first. Everything here is pure and runs in the browser (the static demo
 * decides with the same code as the gateway): no clocks, no randomness, no I/O, no Node APIs.
 */
export {
  auroc,
  brier,
  confusionAt,
  deriveQuality,
  fitCalibration,
  fitIsotonic,
  fitPlatt,
  isotonicTable,
  maxF1Threshold,
  minThresholdAtPrecision,
  plattTable,
  quickCalibrate,
  r6,
  reliability,
  thresholdGrid,
  wilsonCriticalAt,
  wilsonInterval,
  type CalibrationFit,
  type Confusion,
  type LabeledScore,
  type QuickCalibration,
  type QuickCalibrationRequest,
} from './calibrate';
export { applyCalibration, CALIBRATION_TABLE_SIZE, checkCalibrationTable } from './calibration';
export {
  compile,
  CompileError,
  decideEvaluators,
  defaultBudget,
  FACTUAL_CLAIMS_ID,
  FACTUAL_CLAIMS_REF,
  MAX_REQUEST_TOKENS,
  NONE_PROVIDED,
  type Budget,
  type Compiled,
  type CompiledEvaluator,
  type CompiledRequest,
  type CompileInputs,
  type CompileRoute,
  type CompileRouteEvaluator,
  type DecisionEvidenceItem,
  type Exclusion,
  type OnNoEvidence,
  type ThresholdOverrides,
} from './compile';
export { decide, DecideError, GROUNDING_DETECTORS, type DecisionCore } from './decide';
export {
  DEFAULT_MIN_INTEGER,
  detect,
  detectOne,
  DETECTOR_IDS,
  detectorStates,
  maskSecret,
  type DetectorFinding,
  type DetectorInput,
} from './detectors';
export {
  DEFAULT_SOURCES,
  decisionItems,
  EvidenceError,
  extractEvidence,
  forwardBody,
  GATEWAY_MEMBERS,
  LABEL_EXTENSION,
  LABEL_SYSTEM_PROMPT,
  LABEL_TOOL_RESULT,
  LABEL_USER_CONTEXT,
  type Extraction,
  type ExtractFormat,
} from './evidence';
export { clamp01, EPS, formatRisk, r2 } from './floats';
export { globAny, globMatch } from './glob';
export { canonicalize, canonicalizeJSON, CanonicalJsonError, formatNumber, sha256Hex, type JsonValue } from './jcs';
export { ERROR_CODES, LINT_CODES, lintCodes, lintSpec, NOT_ATTACHED, type LintOptions } from './lint';
export { normalize, type ConfidenceSource, type Normalized, type NormalizeError, type RawAnswerInput } from './normalize';
export { compileRE2, translateRE2 } from './re2';
export {
  cloneJSON,
  DEFAULT_MIN_CHARS,
  hasStateField,
  parseRef,
  parseSpec,
  questionBody,
  questionId,
  SpecError,
  specHashes,
  specOptions,
  specRef,
  type Spec,
  type SpecHashes,
} from './spec';
export { BUILTIN_TEMPLATES, builtinSpec } from './templates';
export { compareUTF16, toWellFormed } from './text';
export { checkCPT, countTokens, DEFAULT_CPT, est, MAX_CPT, MIN_CPT, OVERHEAD, type TokenCount } from './tokens';
