"""Tests for the evaluation metric implementations."""

import pytest

from evals.metrics import auroc, compute_metrics, expected_calibration_error


def test_perfect_classifier():
    labels = [True, True, False, False]
    predictions = [True, True, False, False]
    scores = [0.9, 0.8, 0.2, 0.1]

    m = compute_metrics(labels, predictions, scores)

    assert m.precision == 1.0
    assert m.recall == 1.0
    assert m.f1 == 1.0
    assert m.accuracy == 1.0
    assert m.balanced_accuracy == 1.0
    assert m.auroc == 1.0


def test_confusion_matrix_counts():
    labels = [True, True, False, False]
    predictions = [True, False, True, False]  # 1 TP, 1 FN, 1 FP, 1 TN
    scores = [0.9, 0.4, 0.6, 0.1]

    m = compute_metrics(labels, predictions, scores)

    assert (m.true_positives, m.false_negatives, m.false_positives, m.true_negatives) == (
        1,
        1,
        1,
        1,
    )
    assert m.precision == 0.5
    assert m.recall == 0.5
    assert m.accuracy == 0.5


def test_zero_division_guards():
    # No positive predictions and no positive labels.
    m = compute_metrics([False, False], [False, False], [0.1, 0.2])

    assert m.precision == 0.0
    assert m.recall == 0.0
    assert m.f1 == 0.0
    assert m.auroc == 0.5  # undefined -> chance


def test_auroc_with_ties():
    labels = [True, False, True, False]
    scores = [0.5, 0.5, 0.9, 0.1]
    # Tie at 0.5 contributes 0.5; (1 + 1 + 0.5 + 0.5) / 4 pairs = 0.875
    assert auroc(labels, scores) == pytest.approx(0.875)


def test_ece_perfectly_calibrated_bins():
    # In each bin, confidence equals empirical accuracy -> ECE 0.
    labels = [True, False, True, True]
    scores = [0.5, 0.5, 0.95, 0.95]
    # Bin [0.5,0.6): conf 0.5, acc 0.5. Bin [0.9,1.0]: conf 0.95, acc 1.0 -> gap 0.05 * 0.5
    assert expected_calibration_error(labels, scores) == pytest.approx(0.025)


def test_ece_overconfident():
    # Always 0.99 confident but only half correct -> ECE ~ 0.49
    labels = [True, False] * 10
    scores = [0.99] * 20
    assert expected_calibration_error(labels, scores) == pytest.approx(0.49)


def test_length_mismatch_raises():
    with pytest.raises(ValueError):
        compute_metrics([True], [True, False], [0.5, 0.5])
