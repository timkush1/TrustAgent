"""
Benchmark metrics (SPEC section 2.13 benchmark-run.v1): Brier, reliability
bins, nearest-rank percentiles, undefined-aware classification metrics and the
seeded bootstrap, each checked against hand-computed values.
"""

import pytest

from evals.metrics import (
    binary_metrics,
    bootstrap_ci,
    brier_score,
    equal_mass_bins,
    expected_calibration_error,
    percentile,
    reliability_bins,
)

LABELS = [False, True, False, True, True]
SCORES = [0.05, 0.15, 0.12, 0.95, 1.0]


def test_brier_score() -> None:
    # ((0.8 - 1)^2 + (0.3 - 0)^2) / 2 = (0.04 + 0.09) / 2
    assert brier_score([True, False], [0.8, 0.3]) == pytest.approx(0.065)
    assert brier_score([], []) is None
    with pytest.raises(ValueError):
        brier_score([True], [0.1, 0.2])


def test_reliability_bins_are_the_ece_bins() -> None:
    bins = reliability_bins(LABELS, SCORES)

    assert [(b.lo, b.hi, b.n, b.frac_pos) for b in bins] == [
        (0.0, 0.1, 1, 0.0),
        (0.1, 0.2, 2, 0.5),
        (0.9, 1.0, 2, 1.0),
    ]
    assert [b.mean_pred for b in bins] == pytest.approx([0.05, 0.135, 0.975])
    # ECE = 1/5 * 0.05 + 2/5 * |0.135 - 0.5| + 2/5 * |0.975 - 1|
    assert expected_calibration_error(LABELS, SCORES) == pytest.approx(0.166)
    assert bins[0].to_dict() == {"lo": 0.0, "hi": 0.1, "n": 1, "mean_pred": 0.05, "frac_pos": 0.0}
    with pytest.raises(ValueError):
        reliability_bins(LABELS, SCORES, n_bins=0)


def test_equal_mass_bins() -> None:
    bins = equal_mass_bins(LABELS, SCORES, n_bins=2)

    # Sorted scores 0.05, 0.12 | 0.15, 0.95, 1.0
    assert [(b.lo, b.hi, b.n) for b in bins] == [(0.05, 0.12, 2), (0.15, 1.0, 3)]
    assert bins[0].frac_pos == 0.0 and bins[1].frac_pos == 1.0
    assert bins[1].mean_pred == pytest.approx((0.15 + 0.95 + 1.0) / 3)
    assert len(equal_mass_bins(LABELS, SCORES)) == 5  # 15 bins over 5 examples: none empty


def test_nearest_rank_percentile() -> None:
    values = [7.0, 1.0, 3.0, 9.0, 5.0, 2.0, 10.0, 4.0, 8.0, 6.0]

    assert percentile(values, 50) == 5.0
    assert percentile(values, 95) == 10.0
    assert percentile(values, 0) == 1.0
    assert percentile(values, 100) == 10.0
    assert percentile([], 50) is None
    with pytest.raises(ValueError):
        percentile(values, 101)


def test_binary_metrics_by_hand() -> None:
    labels = [True, True, True, False, False, False, False, False]
    predictions = [True, True, False, True, False, False, False, False]
    scores = [0.9, 0.8, 0.3, 0.7, 0.2, 0.1, 0.4, 0.6]

    metrics = binary_metrics(labels, predictions, scores)

    # TP 2, FN 1, FP 1, TN 4
    assert metrics["precision"] == pytest.approx(2 / 3)
    assert metrics["recall"] == pytest.approx(2 / 3)
    assert metrics["f1"] == pytest.approx(2 / 3)
    assert metrics["accuracy"] == pytest.approx(6 / 8)
    assert metrics["balanced_accuracy"] == pytest.approx((2 / 3 + 4 / 5) / 2)
    # Positive-negative pairs ranked correctly: 0.9 and 0.8 beat all five
    # negatives, 0.3 beats 0.2 and 0.1: (5 + 5 + 2) / 15.
    assert metrics["auroc"] == pytest.approx(12 / 15)
    assert metrics["brier"] == pytest.approx(
        (0.01 + 0.04 + 0.49 + 0.49 + 0.04 + 0.01 + 0.16 + 0.36) / 8
    )
    assert metrics["ece"] == pytest.approx(expected_calibration_error(labels, scores))


def test_undefined_metrics_are_none() -> None:
    one_class = binary_metrics([False, False], [False, False], [0.1, 0.2])
    assert one_class["precision"] is None  # no positive prediction
    assert one_class["recall"] is None  # no positive label
    assert one_class["f1"] is None
    assert one_class["balanced_accuracy"] is None and one_class["auroc"] is None
    assert one_class["accuracy"] == 1.0

    all_wrong = binary_metrics([True, False], [False, True], [0.2, 0.8])
    assert all_wrong["precision"] == 0.0 and all_wrong["recall"] == 0.0
    assert all_wrong["f1"] is None  # 0/0

    empty = binary_metrics([], [], [])
    assert all(value is None for value in empty.values())


def test_bootstrap_is_seeded_and_brackets_the_estimate() -> None:
    labels = [i % 3 == 0 for i in range(90)]
    scores = [(0.7 if label else 0.3) + (i % 10) / 50 for i, label in enumerate(labels)]
    predictions = [score >= 0.5 for score in scores]

    first = bootstrap_ci(labels, predictions, scores, n_resamples=200, seed=11)
    again = bootstrap_ci(labels, predictions, scores, n_resamples=200, seed=11)
    other = bootstrap_ci(labels, predictions, scores, n_resamples=200, seed=12)

    assert first == again and first != other
    point = binary_metrics(labels, predictions, scores)
    for name, (lo, hi) in first.items():
        value = point[name]
        assert value is not None and lo <= value + 1e-9 and value - 1e-9 <= hi, name
    assert set(first) == set(point)


def test_bootstrap_of_a_perfect_classifier_and_of_undefined_metrics() -> None:
    labels = [True, False] * 20
    perfect = bootstrap_ci(
        labels, labels, [1.0 if label else 0.0 for label in labels], n_resamples=50
    )
    assert perfect["balanced_accuracy"] == (1.0, 1.0) and perfect["auroc"] == (1.0, 1.0)

    one_class = bootstrap_ci([False] * 10, [False] * 10, [0.1] * 10, n_resamples=50)
    assert "auroc" not in one_class and "precision" not in one_class
    assert one_class["accuracy"] == (1.0, 1.0)
    assert bootstrap_ci([], [], []) == {}
