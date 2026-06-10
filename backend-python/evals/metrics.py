"""
Evaluation metrics for hallucination detection.

Pure-python implementations (no sklearn dependency) of the binary-classification
metrics used by the eval harness. The positive class is "hallucinated".
"""

from dataclasses import asdict, dataclass
from typing import Dict, List, Sequence


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
