/**
 * Calibration fitting (SPEC §2.11, §7.3): Wilson intervals, the threshold grid, max-F1 and
 * minimum-precision thresholds, the Wilson-bound critical_at, PAV isotonic regression, Platt
 * scaling, the full fit with its metrics, quality derivation and Studio's quick calibration.
 *
 * Port of backend-go/internal/decision/calibrate*.go (the reference); pinned by
 * contracts/vectors/calibrate_fit. Like the Go fitter it is pure and deterministic: labels are
 * counted as integers and compared by cross-multiplication where the result decides a branch,
 * sums run in input order, thresholds compare with EPS, published tables are rounded to six
 * decimals (r6), and ties are broken by fixed rules. Keep the operation order of every sum and
 * formula identical to Go's so the doubles agree.
 */
import { applyCalibration, CALIBRATION_TABLE_SIZE } from './calibration';
import { clamp01, EPS } from './floats';

/** One labelled risk (QuickCalibrationRequest.labeled): label true = the evaluator should fire. */
export interface LabeledScore {
  risk_raw: number;
  label: boolean;
}

/** The two-sided 95% standard normal quantile of every Wilson interval. */
export const WILSON_Z95 = 1.959963984540054;
/** The fewest labels any fit accepts (§7.3: n >= 8 with at least 3 of each class). */
export const MIN_FIT_LABELS = 8;
export const MIN_FIT_PER_CLASS = 3;
/** Precision floor of the recommended warn_at (§2.11). */
export const WARN_MIN_PRECISION = 0.8;
/** critical_at: Wilson 95% lower bound of precision >= 0.95 with at least 50 predicted positives. */
export const CRITICAL_MIN_PRECISION_LB = 0.95;
export const CRITICAL_MIN_PREDICTED_POS = 50;
/** Equal-width bins of the reliability table and of ECE. */
export const RELIABILITY_BINS = 10;
/** recommended.critical_null_reason when no critical_at qualifies. */
export const CRITICAL_NULL_REASON = 'insufficient evidence to block';

/** r6 rounds to six decimals as floor(x*1e6 + 0.5) / 1e6 (never Math.round). */
export function r6(x: number): number {
  return Math.floor(x * 1e6 + 0.5) / 1e6;
}

/**
 * The Wilson score interval of k successes in n trials at quantile z, clamped to [0,1] and exact
 * at the ends (lo = 0 when k = 0, hi = 1 when k = n); [0,1] when n = 0.
 */
export function wilsonInterval(k: number, n: number, z: number): [number, number] {
  if (n <= 0) return [0, 1];
  k = Math.min(Math.max(k, 0), n);
  const nf = n;
  const p = k / nf;
  const z2 = z * z;
  const denom = 1 + z2 / nf;
  const center = (p + z2 / (2 * nf)) / denom;
  const half = (z * Math.sqrt((p * (1 - p)) / nf + z2 / (4 * nf * nf))) / denom;
  let lo = clamp01(center - half);
  let hi = clamp01(center + half);
  if (k === 0) lo = 0;
  if (k === n) hi = 1;
  return [lo, hi];
}

/** A confusion matrix at one threshold (an answer fires when risk >= t - EPS). */
export interface Confusion {
  threshold: number;
  tp: number;
  fp: number;
  tn: number;
  fn: number;
}

export function precisionOf(c: Confusion): number {
  return c.tp + c.fp === 0 ? 0 : c.tp / (c.tp + c.fp);
}

export function recallOf(c: Confusion): number {
  return c.tp + c.fn === 0 ? 0 : c.tp / (c.tp + c.fn);
}

export function f1Of(c: Confusion): number {
  return c.tp === 0 ? 0 : (2 * c.tp) / (2 * c.tp + c.fp + c.fn);
}

/** a has a strictly higher F1 than b, compared exactly by cross-multiplication. */
function f1Greater(a: Confusion, b: Confusion): boolean {
  const an = 2 * a.tp;
  const ad = 2 * a.tp + a.fp + a.fn;
  const bn = 2 * b.tp;
  const bd = 2 * b.tp + b.fp + b.fn;
  if (ad === 0 || bd === 0) return an > 0 && bn === 0;
  return an * bd > bn * ad;
}

interface RiskGroup {
  lo: number;
  hi: number;
  pos: number;
  neg: number;
}

/** The scores stably sorted by risk. */
function sortByRisk(samples: readonly LabeledScore[]): LabeledScore[] {
  return [...samples].sort((a, b) => (a.risk_raw < b.risk_raw ? -1 : a.risk_raw > b.risk_raw ? 1 : 0));
}

/** Groups risk-sorted scores into runs within EPS of their neighbours, leaving out index skip (-1 keeps all). */
function groupSorted(sorted: readonly LabeledScore[], skip: number): RiskGroup[] {
  const out: RiskGroup[] = [];
  sorted.forEach((s, i) => {
    if (i === skip) return;
    const last = out.length > 0 ? out[out.length - 1] : undefined;
    if (last !== undefined && s.risk_raw - last.hi <= EPS) {
      last.hi = s.risk_raw;
      if (s.label) last.pos++;
      else last.neg++;
      return;
    }
    out.push({ lo: s.risk_raw, hi: s.risk_raw, pos: s.label ? 1 : 0, neg: s.label ? 0 : 1 });
  });
  return out;
}

function groupRisks(samples: readonly LabeledScore[]): RiskGroup[] {
  return groupSorted(sortByRisk(samples), -1);
}

function gridOf(groups: readonly RiskGroup[]): Confusion[] {
  let pos = 0;
  let neg = 0;
  for (const g of groups) {
    pos += g.pos;
    neg += g.neg;
  }
  const out: Confusion[] = [];
  let tp = pos;
  let fp = neg; // everything fires at the lowest candidate
  for (const g of groups) {
    out.push({ threshold: g.lo, tp, fp, tn: neg - fp, fn: pos - tp });
    tp -= g.pos;
    fp -= g.neg;
  }
  return out;
}

/** The confusion matrix at every candidate threshold (the distinct risks), ascending. */
export function thresholdGrid(samples: readonly LabeledScore[]): Confusion[] {
  return gridOf(groupRisks(samples));
}

/** The confusion matrix at threshold t. */
export function confusionAt(samples: readonly LabeledScore[], t: number): Confusion {
  const c: Confusion = { threshold: t, tp: 0, fp: 0, tn: 0, fn: 0 };
  for (const s of samples) {
    const fires = s.risk_raw >= t - EPS;
    if (fires && s.label) c.tp++;
    else if (fires) c.fp++;
    else if (s.label) c.fn++;
    else c.tn++;
  }
  return c;
}

export const OBJECTIVE_MAX_F1 = 'max_f1';
export const OBJECTIVE_MIN_THRESHOLD_AT_PRECISION = 'min_threshold_at_precision';

function maxF1Of(grid: readonly Confusion[], minPrecision: number): Confusion | null {
  let best: Confusion | null = null;
  for (const c of grid) {
    if (c.tp === 0 || precisionOf(c) < minPrecision - EPS) continue;
    if (best === null || !f1Greater(best, c)) best = c; // ascending: an equal F1 moves to the higher threshold
  }
  return best;
}

/** The highest-F1 candidate with precision >= minPrecision (0 disables the floor); ties go to the higher threshold. */
export function maxF1Threshold(samples: readonly LabeledScore[], minPrecision: number): Confusion | null {
  return maxF1Of(thresholdGrid(samples), minPrecision);
}

function minThresholdOf(grid: readonly Confusion[], target: number): Confusion | null {
  for (const c of grid) if (c.tp > 0 && precisionOf(c) >= target - EPS) return c;
  return null;
}

/** The smallest candidate threshold whose precision reaches target and that catches a positive. */
export function minThresholdAtPrecision(samples: readonly LabeledScore[], target: number): Confusion | null {
  return minThresholdOf(thresholdGrid(samples), target);
}

/** recommended.critical_at. */
export interface CriticalAt {
  value: number;
  precision_lb95: number;
  predicted_pos: number;
}

/**
 * The smallest candidate t such that every candidate t' >= t with at least minPredictedPos
 * predicted positives has a Wilson 95% lower bound of precision >= minLB - EPS; null when the
 * highest judgeable candidate already fails or none is judgeable.
 */
export function wilsonCriticalAt(samples: readonly LabeledScore[], minPredictedPos: number, minLB: number): CriticalAt | null {
  const grid = thresholdGrid(samples);
  let best: CriticalAt | null = null;
  for (let i = grid.length - 1; i >= 0; i--) {
    const c = grid[i];
    const pp = c.tp + c.fp;
    if (pp < minPredictedPos) continue;
    const [lo] = wilsonInterval(c.tp, pp, WILSON_Z95);
    if (lo < minLB - EPS) break;
    best = { value: c.threshold, precision_lb95: lo, predicted_pos: pp };
  }
  return best;
}

function countLabels(samples: readonly LabeledScore[]): [number, number] {
  let pos = 0;
  let neg = 0;
  for (const s of samples) {
    if (s.label) pos++;
    else neg++;
  }
  return [pos, neg];
}

function checkSamples(samples: readonly LabeledScore[]): void {
  samples.forEach((s, i) => {
    if (typeof s.risk_raw !== 'number' || Number.isNaN(s.risk_raw) || s.risk_raw < 0 || s.risk_raw > 1) {
      throw new RangeError(`calibrate: label ${i} has risk ${String(s.risk_raw)}, want a value in [0,1]`);
    }
  });
}

function enoughLabels(pos: number, neg: number): boolean {
  return pos + neg >= MIN_FIT_LABELS && pos >= MIN_FIT_PER_CLASS && neg >= MIN_FIT_PER_CLASS;
}

/** One row of the reliability table. */
export interface ReliabilityBin {
  lo: number;
  hi: number;
  n: number;
  mean_pred: number;
  frac_pos: number;
}

/** Equal-width reliability bins (non-empty ones, ascending) and ECE; ECE is null without samples. */
export function reliability(samples: readonly LabeledScore[], preds: readonly number[]): [ReliabilityBin[], number | null] {
  const bins = Array.from({ length: RELIABILITY_BINS }, () => ({ n: 0, pos: 0, sum: 0 }));
  samples.forEach((s, i) => {
    const p = clamp01(preds[i]);
    const k = Math.min(Math.floor(p * RELIABILITY_BINS), RELIABILITY_BINS - 1);
    bins[k].n++;
    bins[k].sum += p;
    if (s.label) bins[k].pos++;
  });
  const out: ReliabilityBin[] = [];
  if (samples.length === 0) return [out, null];
  let ece = 0;
  bins.forEach((b, k) => {
    if (b.n === 0) return;
    const mean = b.sum / b.n;
    const frac = b.pos / b.n;
    out.push({ lo: k / RELIABILITY_BINS, hi: (k + 1) / RELIABILITY_BINS, n: b.n, mean_pred: mean, frac_pos: frac });
    ece += (b.n / samples.length) * Math.abs(mean - frac);
  });
  return [out, ece];
}

/** Mean squared difference between predictions and labels (1 = should fire); null without samples. */
export function brier(samples: readonly LabeledScore[], preds: readonly number[]): number | null {
  if (samples.length === 0) return null;
  let sum = 0;
  samples.forEach((s, i) => {
    const d = s.label ? 1 - preds[i] : preds[i];
    sum += d * d;
  });
  return sum / samples.length;
}

/** P(a random positive outranks a random negative), ties counting one half; null unless both classes. */
export function auroc(samples: readonly LabeledScore[]): number | null {
  const [pos, neg] = countLabels(samples);
  if (pos === 0 || neg === 0) return null;
  let twice = 0;
  let negBelow = 0;
  for (const g of groupRisks(samples)) {
    twice += g.pos * (2 * negBelow + g.neg);
    negBelow += g.neg;
  }
  return twice / (2 * pos * neg);
}

// ------------------------------------------------------------------------------ isotonic (PAV)

export interface IsotonicBlock {
  x_lo: number;
  x_hi: number;
  value: number;
  n: number;
}

/** Pool-adjacent-violators isotonic regression; risks within EPS pool first, equal shares merge. */
export function fitIsotonic(samples: readonly LabeledScore[]): IsotonicBlock[] {
  const stack: { lo: number; hi: number; pos: number; cnt: number }[] = [];
  for (const g of groupRisks(samples)) {
    stack.push({ lo: g.lo, hi: g.hi, pos: g.pos, cnt: g.pos + g.neg });
    while (stack.length > 1) {
      const a = stack[stack.length - 2];
      const b = stack[stack.length - 1];
      if (a.pos * b.cnt < b.pos * a.cnt) break;
      stack.splice(stack.length - 2, 2, { lo: a.lo, hi: b.hi, pos: a.pos + b.pos, cnt: a.cnt + b.cnt });
    }
  }
  return stack.map((b) => ({ x_lo: b.lo, x_hi: b.hi, value: b.pos / b.cnt, n: b.cnt }));
}

/** An isotonic fit at x: the block value, linear between blocks, clipped outside. */
export function isotonicAt(blocks: readonly IsotonicBlock[], x: number): number {
  if (x <= blocks[0].x_lo) return blocks[0].value;
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    if (x <= b.x_hi) {
      if (x >= b.x_lo) return b.value;
      const prev = blocks[i - 1];
      return prev.value + ((b.value - prev.value) * (x - prev.x_hi)) / (b.x_lo - prev.x_hi);
    }
  }
  return blocks[blocks.length - 1].value;
}

/** The 101-point table of an isotonic fit: t[i] = r6(isotonicAt(i/100)). */
export function isotonicTable(blocks: readonly IsotonicBlock[]): number[] {
  return Array.from({ length: CALIBRATION_TABLE_SIZE }, (_, i) => r6(isotonicAt(blocks, i / 100)));
}

// ------------------------------------------------------------------------------ Platt scaling

/** calibrated(x) = 1 / (1 + exp(-(a·x + b))), a >= 0. */
export interface PlattParams {
  a: number;
  b: number;
}

const PLATT_MAX_ITER = 100;
const PLATT_MIN_STEP = 1e-10;
const PLATT_SIGMA = 1e-12;
const PLATT_GRAD_TOL = 1e-12;
const PLATT_STEP_TOL = 1e-12;
const PLATT_ARMIJO = 1e-4;
const PLATT_FLAT_SPAN = 1e-9;

export const WARN_PLATT_FLAT_SCORES = 'every label has the same risk, so the mapping is flat';
export const WARN_PLATT_ANTI_CORRELATED = 'risk falls as the labels rise, so the Platt slope is clamped to 0 and the mapping is flat';
export const WARN_PLATT_NOT_CONVERGED = 'the Platt fit did not converge; its parameters are the last Newton iterate';

function sigmoid(z: number): number {
  if (z >= 0) return 1 / (1 + Math.exp(-z));
  const e = Math.exp(z);
  return e / (1 + e);
}

function softplus(z: number): number {
  if (z > 0) return z + Math.log1p(Math.exp(-z));
  return Math.log1p(Math.exp(z));
}

function logit(p: number): number {
  return Math.log(p / (1 - p));
}

function plattTargets(samples: readonly LabeledScore[]): number[] {
  const [pos, neg] = countLabels(samples);
  const hi = (pos + 1) / (pos + 2);
  const lo = 1 / (neg + 2);
  return samples.map((s) => (s.label ? hi : lo));
}

function flatPlatt(targets: readonly number[]): PlattParams {
  let sum = 0;
  for (const t of targets) sum += t;
  return { a: 0, b: logit(sum / targets.length) };
}

/**
 * Newton's method with a backtracking line search on the smoothed-target log-likelihood, from
 * a = 0, b = log((N+ + 1)/(N- + 1)). Degenerate inputs give a flat mapping. samples must be non-empty.
 */
export function fitPlatt(samples: readonly LabeledScore[]): [PlattParams, string[]] {
  const targets = plattTargets(samples);
  let lo = samples[0].risk_raw;
  let hi = samples[0].risk_raw;
  for (const s of samples) {
    lo = Math.min(lo, s.risk_raw);
    hi = Math.max(hi, s.risk_raw);
  }
  if (hi - lo < PLATT_FLAT_SPAN) {
    const p = flatPlatt(targets);
    return [{ a: 0, b: r6(p.b) }, [WARN_PLATT_FLAT_SCORES]];
  }
  const [pos, neg] = countLabels(samples);
  let a = 0;
  let b = Math.log((pos + 1) / (neg + 1));
  const loss = (a: number, b: number): number => {
    let sum = 0;
    samples.forEach((s, i) => {
      const z = a * s.risk_raw + b;
      sum += softplus(z) - targets[i] * z;
    });
    return sum;
  };
  const warnings: string[] = [];
  const tol = PLATT_GRAD_TOL * samples.length;
  let f = loss(a, b);
  let converged = false;
  for (let iter = 0; iter < PLATT_MAX_ITER; iter++) {
    let ga = 0;
    let gb = 0;
    let h11 = 0;
    let h12 = 0;
    let h22 = 0;
    samples.forEach((s, i) => {
      const p = sigmoid(a * s.risk_raw + b);
      const d = p - targets[i];
      const w = p * (1 - p);
      ga += d * s.risk_raw;
      gb += d;
      h11 += w * s.risk_raw * s.risk_raw;
      h12 += w * s.risk_raw;
      h22 += w;
    });
    if (Math.abs(ga) < tol && Math.abs(gb) < tol) {
      converged = true;
      break;
    }
    h11 += PLATT_SIGMA;
    h22 += PLATT_SIGMA;
    const det = h11 * h22 - h12 * h12;
    const da = -(h22 * ga - h12 * gb) / det;
    const db = -(-h12 * ga + h11 * gb) / det;
    if (Math.max(Math.abs(da), Math.abs(db)) <= PLATT_STEP_TOL * (1 + Math.max(Math.abs(a), Math.abs(b)))) {
      a += da;
      b += db;
      converged = true;
      break;
    }
    const slope = ga * da + gb * db;
    let step = 1;
    let accepted = false;
    while (step >= PLATT_MIN_STEP) {
      const na = a + step * da;
      const nb = b + step * db;
      const nf = loss(na, nb);
      if (nf < f + PLATT_ARMIJO * step * slope) {
        a = na;
        b = nb;
        f = nf;
        accepted = true;
        break;
      }
      step /= 2;
    }
    if (!accepted) {
      converged = true;
      break;
    }
  }
  if (!converged) warnings.push(WARN_PLATT_NOT_CONVERGED);
  if (a < 0) {
    const p = flatPlatt(targets);
    return [{ a: 0, b: r6(p.b) }, [...warnings, WARN_PLATT_ANTI_CORRELATED]];
  }
  return [{ a: r6(a), b: r6(b) }, warnings];
}

export function plattAt(p: PlattParams, x: number): number {
  return sigmoid(p.a * x + p.b);
}

/** The 101-point table of a Platt mapping: t[i] = r6(plattAt(i/100)). */
export function plattTable(p: PlattParams): number[] {
  return Array.from({ length: CALIBRATION_TABLE_SIZE }, (_, i) => r6(plattAt(p, i / 100)));
}

// ------------------------------------------------------------------------------ the full fit

export const METHOD_AUTO = 'auto';
export const METHOD_ISOTONIC = 'isotonic';
export const METHOD_PLATT = 'platt';
export const ISOTONIC_MIN_LABELS = 500;
export const CV_FOLDS = 5;

export const WARN_ISOTONIC_FEW_LABELS =
  'isotonic regression with fewer than 500 labels tends to overfit; auto would use Platt scaling';
export const WARN_NO_WARN_AT = 'no threshold reaches precision 0.8, so there is no recommended warn_at';
export const WARN_WARN_AT_CAPPED =
  'the max-F1 warn_at was above critical_at, so warn_at is lowered to critical_at (warn_at must not exceed critical_at)';

export interface FitMetrics {
  ece_before: number | null;
  ece_after: number | null;
  brier_before: number | null;
  brier_after: number | null;
  auroc: number | null;
}

export interface WarnAt {
  value: number;
  precision: number;
  recall: number;
}

export interface Recommended {
  space: 'calibrated';
  warn_at: WarnAt | null;
  critical_at: CriticalAt | null;
  critical_null_reason: string | null;
}

export interface CalibrationFit {
  method: string;
  table: number[];
  platt: PlattParams | null;
  n: number;
  n_pos: number;
  n_neg: number;
  metrics: FitMetrics;
  reliability: ReliabilityBin[];
  recommended: Recommended;
  cv_brier: { isotonic: number; platt: number } | null;
  warnings: string[];
}

/** Fits a calibration to labelled raw risks (SPEC §2.11); see the Go FitCalibration. */
export function fitCalibration(samples: readonly LabeledScore[], method: string = METHOD_AUTO): CalibrationFit {
  checkSamples(samples);
  const [pos, neg] = countLabels(samples);
  if (!enoughLabels(pos, neg)) {
    throw new RangeError(
      `calibrate: not enough labels: have ${pos + neg} (${pos} should fire, ${neg} should not), need ${MIN_FIT_LABELS} with at least ${MIN_FIT_PER_CLASS} of each`,
    );
  }
  const fit: CalibrationFit = {
    method: '',
    table: [],
    platt: null,
    n: pos + neg,
    n_pos: pos,
    n_neg: neg,
    metrics: { ece_before: null, ece_after: null, brier_before: null, brier_after: null, auroc: null },
    reliability: [],
    recommended: { space: 'calibrated', warn_at: null, critical_at: null, critical_null_reason: null },
    cv_brier: null,
    warnings: [],
  };
  let chosen = method === '' ? METHOD_AUTO : method;
  if (chosen === METHOD_AUTO) {
    chosen = METHOD_PLATT;
    if (fit.n >= ISOTONIC_MIN_LABELS) {
      const cv = { isotonic: cvBrier(samples, METHOD_ISOTONIC), platt: cvBrier(samples, METHOD_PLATT) };
      fit.cv_brier = cv;
      if (cv.isotonic <= cv.platt + EPS) chosen = METHOD_ISOTONIC;
    }
  } else if (chosen === METHOD_ISOTONIC) {
    if (fit.n < ISOTONIC_MIN_LABELS) fit.warnings.push(WARN_ISOTONIC_FEW_LABELS);
  } else if (chosen !== METHOD_PLATT) {
    throw new RangeError(`calibrate: unknown method ${JSON.stringify(method)} (want auto, isotonic or platt)`);
  }
  fit.method = chosen;
  if (chosen === METHOD_ISOTONIC) {
    fit.table = isotonicTable(fitIsotonic(samples));
  } else {
    const [p, warnings] = fitPlatt(samples);
    fit.platt = p;
    fit.table = plattTable(p);
    fit.warnings.push(...warnings);
  }

  const raw = samples.map((s) => s.risk_raw);
  const cal = samples.map((s) => applyCalibration(fit.table, s.risk_raw));
  const calibrated = samples.map((s, i) => ({ risk_raw: cal[i], label: s.label }));
  fit.metrics.ece_before = reliability(samples, raw)[1];
  const [bins, eceAfter] = reliability(samples, cal);
  fit.reliability = bins;
  fit.metrics.ece_after = eceAfter;
  fit.metrics.brier_before = brier(samples, raw);
  fit.metrics.brier_after = brier(samples, cal);
  fit.metrics.auroc = auroc(samples);

  const warn = maxF1Threshold(calibrated, WARN_MIN_PRECISION);
  if (warn !== null) fit.recommended.warn_at = { value: warn.threshold, precision: precisionOf(warn), recall: recallOf(warn) };
  else fit.warnings.push(WARN_NO_WARN_AT);
  fit.recommended.critical_at = wilsonCriticalAt(calibrated, CRITICAL_MIN_PREDICTED_POS, CRITICAL_MIN_PRECISION_LB);
  if (fit.recommended.critical_at === null) fit.recommended.critical_null_reason = CRITICAL_NULL_REASON;
  const w = fit.recommended.warn_at;
  const c = fit.recommended.critical_at;
  if (w !== null && c !== null && w.value > c.value) {
    const at = confusionAt(calibrated, c.value);
    fit.recommended.warn_at = { value: c.value, precision: precisionOf(at), recall: recallOf(at) };
    fit.warnings.push(WARN_WARN_AT_CAPPED);
  }
  return fit;
}

/** CV_FOLDS-fold cross-validated Brier score of a method (folds after a stable (risk, label) sort). */
function cvBrier(samples: readonly LabeledScore[], method: string): number {
  const order = [...samples].sort((a, b) => {
    if (a.risk_raw < b.risk_raw) return -1;
    if (a.risk_raw > b.risk_raw) return 1;
    if (a.label === b.label) return 0;
    return a.label ? 1 : -1;
  });
  let sum = 0;
  for (let k = 0; k < CV_FOLDS; k++) {
    const train: LabeledScore[] = [];
    const test: LabeledScore[] = [];
    order.forEach((s, j) => (j % CV_FOLDS === k ? test : train).push(s));
    if (test.length === 0) continue;
    const table = method === METHOD_ISOTONIC ? isotonicTable(fitIsotonic(train)) : plattTable(fitPlatt(train)[0]);
    for (const s of test) {
      const p = applyCalibration(table, s.risk_raw);
      const d = s.label ? 1 - p : p;
      sum += d * d;
    }
  }
  return sum / samples.length;
}

// ------------------------------------------------------------------------------ quality

export interface LabelSourceCounts {
  human: number;
  public_dataset: number;
  uniform_audit: number;
  judge?: number;
}

export const MEASURED_MIN_LABELS = 200;
export const MEASURED_MIN_PER_CLASS = 30;

/** A calibration's quality (SPEC §2.11): mock, proxy, provisional (family), measured, provisional. */
export function deriveQuality(snapshot: string, src: LabelSourceCounts, nPos: number, nNeg: number, family: boolean): string {
  if (snapshot.startsWith('mock/')) return 'mock';
  if (src.uniform_audit > 0 || (src.judge ?? 0) > 0) return 'proxy';
  if (family) return 'provisional';
  if (nPos + nNeg >= MEASURED_MIN_LABELS && nPos >= MEASURED_MIN_PER_CLASS && nNeg >= MEASURED_MIN_PER_CLASS) return 'measured';
  return 'provisional';
}

// ------------------------------------------------------------------------------ quick calibration

export const QUICK_PLATT_MIN_LABELS = 30;
export const QUICK_ISOTONIC_MIN_LABELS = 50;
export const QUICK_ISOTONIC_MIN_PER_CLASS = 15;
export const QUICK_MAX_INTERVAL_WIDTH = 0.4;
export const QUICK_MAX_LOO_RANGE = 0.15;
export const QUICK_DEFAULT_TARGET_PRECISION = 0.8;

export interface QuickCalibrationRequest {
  labeled: LabeledScore[];
  objective?: string;
  target_precision?: number;
  current?: number;
}

export interface PointCI {
  point: number;
  ci95: [number, number];
}

export interface QuickMapping {
  method: string | null;
  table: number[] | null;
  platt: PlattParams | null;
}

export interface QuickCalibration {
  n: number;
  n_pos: number;
  n_neg: number;
  enough: boolean;
  message: string;
  threshold: number | null;
  precision: PointCI | null;
  recall: PointCI | null;
  loo_range: [number, number] | null;
  unstable: boolean;
  mapping: QuickMapping;
  grid: Confusion[];
}

/** Whole percentage, rounding half up (floor(100p + 0.5)). */
function percent(p: number): string {
  return `${Math.floor(p * 100 + 0.5)}%`;
}

/** A float as Go's strconv.FormatFloat(x, 'f', -1, 64) prints it (shortest, no exponent). */
function plain(x: number): string {
  const s = String(x);
  if (!/e/i.test(s)) return s;
  return x.toFixed(20).replace(/0+$/, '').replace(/\.$/, '');
}

/** Studio's quick calibration (SPEC §7.3): a suggested raw-space threshold; never critical_at. */
export function quickCalibrate(req: QuickCalibrationRequest): QuickCalibration {
  checkSamples(req.labeled);
  const objective = req.objective ?? OBJECTIVE_MAX_F1;
  let target = QUICK_DEFAULT_TARGET_PRECISION;
  if (objective === OBJECTIVE_MIN_THRESHOLD_AT_PRECISION) {
    if (req.target_precision !== undefined) target = req.target_precision;
    if (Number.isNaN(target) || target <= 0 || target > 1) throw new RangeError(`calibrate: target_precision ${target}, want a value in (0,1]`);
  } else if (objective !== OBJECTIVE_MAX_F1) {
    throw new RangeError(`calibrate: unknown objective ${JSON.stringify(objective)} (want ${OBJECTIVE_MAX_F1} or ${OBJECTIVE_MIN_THRESHOLD_AT_PRECISION})`);
  }
  const current = req.current;
  if (current !== undefined && (Number.isNaN(current) || current < 0 || current > 1)) {
    throw new RangeError(`calibrate: current threshold ${current}, want a value in [0,1]`);
  }
  const pick = (grid: readonly Confusion[]) =>
    objective === OBJECTIVE_MIN_THRESHOLD_AT_PRECISION ? minThresholdOf(grid, target) : maxF1Of(grid, 0);

  const [pos, neg] = countLabels(req.labeled);
  const q: QuickCalibration = {
    n: pos + neg,
    n_pos: pos,
    n_neg: neg,
    enough: enoughLabels(pos, neg),
    message: '',
    threshold: null,
    precision: null,
    recall: null,
    loo_range: null,
    unstable: false,
    mapping: { method: null, table: null, platt: null },
    grid: quickGrid(req.labeled, current),
  };
  if (!q.enough) {
    q.message = `Label at least ${MIN_FIT_LABELS} answers, with at least ${MIN_FIT_PER_CLASS} where the check should fire and ${MIN_FIT_PER_CLASS} where it should not (you have ${pos} and ${neg}).`;
    return q;
  }
  const c = pick(thresholdGrid(req.labeled));
  if (c === null) {
    q.message = `With ${q.n} labels, no threshold reaches precision ${percent(target)}; keep it watching and label more traffic.`;
    q.mapping = quickMapping(req.labeled, pos, neg);
    return q;
  }
  q.threshold = c.threshold;
  const [plo, phi] = wilsonInterval(c.tp, c.tp + c.fp, WILSON_Z95);
  const [rlo, rhi] = wilsonInterval(c.tp, c.tp + c.fn, WILSON_Z95);
  q.precision = { point: precisionOf(c), ci95: [plo, phi] };
  q.recall = { point: recallOf(c), ci95: [rlo, rhi] };

  let loLOO = 0;
  let hiLOO = 0;
  let found = false;
  const sorted = sortByRisk(req.labeled);
  for (let i = 0; i < sorted.length; i++) {
    const li = pick(gridOf(groupSorted(sorted, i)));
    if (li === null) continue;
    if (!found) {
      loLOO = li.threshold;
      hiLOO = li.threshold;
      found = true;
      continue;
    }
    loLOO = Math.min(loLOO, li.threshold);
    hiLOO = Math.max(hiLOO, li.threshold);
  }
  if (found) q.loo_range = [loLOO, hiLOO];
  q.unstable =
    phi - plo > QUICK_MAX_INTERVAL_WIDTH + EPS ||
    rhi - rlo > QUICK_MAX_INTERVAL_WIDTH + EPS ||
    (q.loo_range !== null && q.loo_range[1] - q.loo_range[0] > QUICK_MAX_LOO_RANGE + EPS);
  q.mapping = quickMapping(req.labeled, pos, neg);
  q.message = quickMessage(q);
  return q;
}

function quickGrid(samples: readonly LabeledScore[], current: number | undefined): Confusion[] {
  const grid = thresholdGrid(samples);
  if (current === undefined) return grid;
  const at = grid.findIndex((c) => c.threshold >= current - EPS);
  if (at >= 0 && Math.abs(grid[at].threshold - current) <= EPS) return grid;
  const row = confusionAt(samples, current);
  if (at < 0) return [...grid, row];
  return [...grid.slice(0, at), row, ...grid.slice(at)];
}

function quickMapping(samples: readonly LabeledScore[], pos: number, neg: number): QuickMapping {
  const n = pos + neg;
  if (n >= QUICK_ISOTONIC_MIN_LABELS && pos >= QUICK_ISOTONIC_MIN_PER_CLASS && neg >= QUICK_ISOTONIC_MIN_PER_CLASS) {
    return { method: METHOD_ISOTONIC, table: isotonicTable(fitIsotonic(samples)), platt: null };
  }
  if (n >= QUICK_PLATT_MIN_LABELS) {
    const [p] = fitPlatt(samples);
    return { method: METHOD_PLATT, table: plattTable(p), platt: p };
  }
  return { method: null, table: null, platt: null };
}

/**
 * The §7.3 honesty text: "suggested threshold", never "calibrated". An unstable suggestion says
 * why: the leave-one-out range when that range is what makes it unstable, else the wide 95% ranges
 * (a range of one value, "between 0.92 and 0.92", explains nothing).
 */
function quickMessage(q: QuickCalibration): string {
  const t = q.threshold ?? 0;
  const r = q.recall as PointCI;
  const p = q.precision as PointCI;
  let s = `Suggested threshold from ${q.n} labels: ${plain(t)}. `;
  s += `With ${q.n} labels, recall is ${percent(r.point)} (95% range ${percent(r.ci95[0])}–${percent(r.ci95[1])}) and precision is ${percent(p.point)} (95% range ${percent(p.ci95[0])}–${percent(p.ci95[1])})`;
  if (q.unstable) {
    s += '; the suggestion is unstable';
    if (q.loo_range !== null && q.loo_range[1] - q.loo_range[0] > QUICK_MAX_LOO_RANGE + EPS) {
      s += ` (leaving out one label moves it between ${plain(q.loo_range[0])} and ${plain(q.loo_range[1])})`;
    } else {
      s += ' (too few labels: the 95% ranges are wide)';
    }
  }
  return `${s}. Keep it watching and label more traffic.`;
}
