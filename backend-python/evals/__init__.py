"""TrustAgent evaluation harness.

Tier 1 (CI regression gate): deterministic golden-set runs with the mock
provider — guards pipeline wiring, parsing, and scoring math on every PR.

Tier 2 (benchmarks): HaluEval-derived datasets with a live model — measures
real detector quality. See docs/EVALUATION.md.
"""
