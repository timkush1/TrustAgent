"""
Evaluation metrics for hallucination detection.

Pure-python implementations (no sklearn dependency) of the binary-classification
metrics used by the eval harness. The positive class is "hallucinated".

`compute_metrics` is the golden-gate suite (undefined values read 0, AUROC 0.5;
its output is part of the committed baselines). The benchmark suite
(`binary_metrics`, SPEC section 2.13 benchmark-run.v1) reports an undefined
value as None instead, and adds the Brier score, reliability bins
(`reliability_bins`: 10 equal-width; `equal_mass_bins`: 15 equal-mass), a seeded
percentile bootstrap for 95% confidence intervals (`bootstrap_ci`) and
nearest-rank latency percentiles (`percentile`).
"""

import math
import random
from dataclasses import asdict, dataclass
from typing import Callable, Dict, List, Optional, Sequence, Tuple


@dataclass
class ClassificationMetrics:
    """Binary classification metrics for hallucination detection."""

    total: int
    positives: int  # labeled hallucinated
    negatives: int  # labeled faithful
    true_positives: int
    false_positives: int
    true_negatives: int
    false_negatives: int
    precision: float
    recall: float
    f1: float
    accuracy: float
    balanced_accuracy: float
    auroc: float
    ece: float

    def to_dict(self) -> Dict[str, float]:
        return asdict(self)


def _safe_div(numerator: float, denominator: float) -> float:
    return numerator / denominator if denominator else 0.0


def auroc(labels: Sequence[bool], scores: Sequence[float]) -> float:
    """
    Area under the ROC curve via the rank-sum (Mann-Whitney U) formulation,
    with average ranks for ties.

    Args:
        labels: True class per example (True = positive/hallucinated).
        scores: Predicted probability of the positive class per example.
    """
    n_pos = sum(labels)
    n_neg = len(labels) - n_pos
    if n_pos == 0 or n_neg == 0:
        return 0.5  # undefined; conventionally chance level

    order = sorted(range(len(scores)), key=lambda i: scores[i])
    ranks = [0.0] * len(scores)
    i = 0
    while i < len(order):
        j = i
        while j + 1 < len(order) and scores[order[j + 1]] == scores[order[i]]:
            j += 1
        avg_rank = (i + j) / 2 + 1  # 1-based average rank across the tie group
        for k in range(i, j + 1):
            ranks[order[k]] = avg_rank
        i = j + 1

    rank_sum_pos = sum(rank for rank, label in zip(ranks, labels) if label)
    u_statistic = rank_sum_pos - n_pos * (n_pos + 1) / 2
    return u_statistic / (n_pos * n_neg)


def expected_calibration_error(
    labels: Sequence[bool], scores: Sequence[float], n_bins: int = 10
) -> float:
    """
    Expected Calibration Error over equal-width probability bins.

    Args:
        labels: True class per example (True = positive).
        scores: Predicted probability of the positive class per example.
        n_bins: Number of equal-width bins on [0, 1].
    """
    if not labels:
        return 0.0

    bins: List[List[int]] = [[] for _ in range(n_bins)]
    for i, score in enumerate(scores):
        index = min(int(score * n_bins), n_bins - 1)
        bins[index].append(i)

    ece = 0.0
    for members in bins:
        if not members:
            continue
        avg_confidence = sum(scores[i] for i in members) / len(members)
        accuracy = sum(1 for i in members if labels[i]) / len(members)
        ece += (len(members) / len(labels)) * abs(avg_confidence - accuracy)
    return ece


def compute_metrics(
    labels: Sequence[bool],
    predictions: Sequence[bool],
    scores: Sequence[float],
) -> ClassificationMetrics:
    """
    Compute the full metric suite.

    Args:
        labels: Ground truth per example (True = hallucinated).
        predictions: Detector decision per example (True = flagged).
        scores: Predicted probability of hallucination per example
                (used for AUROC and ECE).
    """
    if not (len(labels) == len(predictions) == len(scores)):
        raise ValueError("labels, predictions, and scores must have equal length")

    tp = sum(1 for label, pred in zip(labels, predictions) if label and pred)
    fp = sum(1 for label, pred in zip(labels, predictions) if not label and pred)
    tn = sum(1 for label, pred in zip(labels, predictions) if not label and not pred)
    fn = sum(1 for label, pred in zip(labels, predictions) if label and not pred)

    precision = _safe_div(tp, tp + fp)
    recall = _safe_div(tp, tp + fn)
    specificity = _safe_div(tn, tn + fp)

    return ClassificationMetrics(
        total=len(labels),
        positives=tp + fn,
        negatives=tn + fp,
        true_positives=tp,
        false_positives=fp,
        true_negatives=tn,
        false_negatives=fn,
        precision=precision,
        recall=recall,
        f1=_safe_div(2 * precision * recall, precision + recall),
        accuracy=_safe_div(tp + tn, len(labels)),
        balanced_accuracy=(recall + specificity) / 2,
        auroc=auroc(labels, scores),
        ece=expected_calibration_error(labels, scores),
    )


# ---------------------------------------------------------------------------
# Benchmark suite (benchmark-run.v1)
# ---------------------------------------------------------------------------

# benchmark-run.v1 metrics, in schema order.
METRIC_NAMES: Tuple[str, ...] = (
    "precision",
    "recall",
    "f1",
    "accuracy",
    "balanced_accuracy",
    "auroc",
    "ece",
    "brier",
)

# Reliability bins: 10 equal-width (the ECE bins) and a 15 equal-mass variant.
RELIABILITY_BINS = 10
EQUAL_MASS_BINS = 15

# Bootstrap: resamples, and the share of resamples in which a metric must be
# defined for its interval to be reported.
BOOTSTRAP_RESAMPLES = 1000
BOOTSTRAP_MIN_DEFINED = 0.9


@dataclass(frozen=True)
class ReliabilityBin:
    """One reliability-diagram bin: scores in [lo, hi) ([lo, hi] for the last)."""

    lo: float
    hi: float
    n: int
    mean_pred: float
    frac_pos: float

    def to_dict(self) -> Dict[str, float]:
        return asdict(self)


def _check_lengths(*columns: Sequence[object]) -> None:
    if len({len(column) for column in columns}) > 1:
        raise ValueError("labels, predictions and scores must have equal length")


def brier_score(labels: Sequence[bool], scores: Sequence[float]) -> Optional[float]:
    """Mean squared error of the predicted probability; None without examples."""
    _check_lengths(labels, scores)
    if not labels:
        return None
    return sum(
        (score - (1.0 if label else 0.0)) ** 2 for label, score in zip(labels, scores)
    ) / len(labels)


def _bin(
    members: Sequence[int], labels: Sequence[bool], scores: Sequence[float], lo: float, hi: float
) -> ReliabilityBin:
    return ReliabilityBin(
        lo=lo,
        hi=hi,
        n=len(members),
        mean_pred=sum(scores[i] for i in members) / len(members),
        frac_pos=sum(1 for i in members if labels[i]) / len(members),
    )


def reliability_bins(
    labels: Sequence[bool], scores: Sequence[float], n_bins: int = RELIABILITY_BINS
) -> List[ReliabilityBin]:
    """
    Equal-width bins over [0, 1] (the ECE bins), empty bins omitted.

    A score lands in bin min(floor(score * n_bins), n_bins - 1), as in
    `expected_calibration_error`.
    """
    _check_lengths(labels, scores)
    if n_bins < 1:
        raise ValueError("n_bins must be at least 1")
    members: List[List[int]] = [[] for _ in range(n_bins)]
    for i, score in enumerate(scores):
        members[min(int(score * n_bins), n_bins - 1)].append(i)
    return [
        _bin(group, labels, scores, index / n_bins, (index + 1) / n_bins)
        for index, group in enumerate(members)
        if group
    ]


def equal_mass_bins(
    labels: Sequence[bool], scores: Sequence[float], n_bins: int = EQUAL_MASS_BINS
) -> List[ReliabilityBin]:
    """
    Bins holding (nearly) equal numbers of examples, by ascending score (ties
    keep input order). lo/hi are the lowest and highest score in the bin.
    """
    _check_lengths(labels, scores)
    if n_bins < 1:
        raise ValueError("n_bins must be at least 1")
    order = sorted(range(len(scores)), key=lambda i: (scores[i], i))
    bins: List[ReliabilityBin] = []
    for index in range(n_bins):
        group = order[index * len(order) // n_bins : (index + 1) * len(order) // n_bins]
        if group:
            bins.append(_bin(group, labels, scores, scores[group[0]], scores[group[-1]]))
    return bins


def percentile(values: Sequence[float], q: float) -> Optional[float]:
    """Nearest-rank percentile (q in [0, 100]); None without values."""
    if not values:
        return None
    if not 0 <= q <= 100:
        raise ValueError("q must be within [0, 100]")
    ordered = sorted(values)
    rank = max(1, math.ceil(q / 100 * len(ordered)))
    return ordered[rank - 1]


def _ratio(numerator: float, denominator: float) -> Optional[float]:
    return numerator / denominator if denominator else None


def binary_metrics(
    labels: Sequence[bool], predictions: Sequence[bool], scores: Sequence[float]
) -> Dict[str, Optional[float]]:
    """
    The benchmark-run.v1 metrics; a value that is undefined for the data is None:

    - precision without a positive prediction, recall without a positive label;
    - f1 when precision or recall is undefined, or both are 0;
    - balanced accuracy and AUROC unless both classes are present;
    - accuracy, ECE and Brier without examples.
    """
    _check_lengths(labels, predictions, scores)
    tp = sum(1 for label, pred in zip(labels, predictions) if label and pred)
    fp = sum(1 for label, pred in zip(labels, predictions) if not label and pred)
    tn = sum(1 for label, pred in zip(labels, predictions) if not label and not pred)
    fn = sum(1 for label, pred in zip(labels, predictions) if label and not pred)
    both_classes = tp + fn > 0 and tn + fp > 0

    precision = _ratio(tp, tp + fp)
    recall = _ratio(tp, tp + fn)
    specificity = _ratio(tn, tn + fp)
    f1: Optional[float] = None
    if precision is not None and recall is not None and precision + recall > 0:
        f1 = 2 * precision * recall / (precision + recall)
    balanced = (
        (recall + specificity) / 2
        if both_classes and recall is not None and specificity is not None
        else None
    )
    return {
        "precision": precision,
        "recall": recall,
        "f1": f1,
        "accuracy": _ratio(tp + tn, len(labels)),
        "balanced_accuracy": balanced,
        "auroc": auroc(labels, scores) if both_classes else None,
        "ece": expected_calibration_error(labels, scores) if labels else None,
        "brier": brier_score(labels, scores),
    }


def bootstrap_ci(
    labels: Sequence[bool],
    predictions: Sequence[bool],
    scores: Sequence[float],
    *,
    n_resamples: int = BOOTSTRAP_RESAMPLES,
    seed: int = 0,
    confidence: float = 0.95,
    min_defined: float = BOOTSTRAP_MIN_DEFINED,
    metrics: Callable[
        [Sequence[bool], Sequence[bool], Sequence[float]], Dict[str, Optional[float]]
    ] = binary_metrics,
) -> Dict[str, Tuple[float, float]]:
    """
    Percentile-bootstrap confidence intervals: `n_resamples` resamples of the
    examples with replacement (a seeded `random.Random`, so a run is
    reproducible), each metric's interval taken at the nearest-rank
    (1 - confidence) / 2 and (1 + confidence) / 2 percentiles of its resampled
    values. A metric defined in fewer than `min_defined` of the resamples has
    no interval.
    """
    _check_lengths(labels, predictions, scores)
    if not labels or n_resamples < 1:
        return {}
    # A seeded, reproducible resampler (not used for anything secret).
    rng = random.Random(seed)  # nosec B311
    n = len(labels)
    samples: Dict[str, List[float]] = {}
    for _ in range(n_resamples):
        picks = [rng.randrange(n) for _ in range(n)]
        resampled = metrics(
            [labels[i] for i in picks], [predictions[i] for i in picks], [scores[i] for i in picks]
        )
        for name, value in resampled.items():
            if value is not None:
                samples.setdefault(name, []).append(value)
    lower_q = (1 - confidence) / 2 * 100
    upper_q = (1 + confidence) / 2 * 100
    intervals: Dict[str, Tuple[float, float]] = {}
    for name, values in samples.items():
        if len(values) < min_defined * n_resamples:
            continue
        lo, hi = percentile(values, lower_q), percentile(values, upper_q)
        if lo is not None and hi is not None:
            intervals[name] = (lo, hi)
    return intervals
