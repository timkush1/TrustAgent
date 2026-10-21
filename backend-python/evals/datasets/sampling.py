"""
Seeded stratified sampling for the benchmark datasets.

Every stratum gets its share of the sample by largest-remainder allocation
(proportional to its size by default, or equal across the groups of
`balance`, e.g. the label), then members are drawn with one `random.Random`
seeded by the caller. Strata are visited in sorted order and candidates are
ordered by the caller's key first, so the same input, `n` and seed always give
the same sample, whatever order the rows arrived in.
"""

import math
import random
from typing import Callable, Dict, List, Mapping, Optional, Sequence, TypeVar

T = TypeVar("T")


def allocate(sizes: Mapping[str, int], n: int) -> Dict[str, int]:
    """
    Split `n` over strata in proportion to their sizes (largest remainder; ties
    go to the larger stratum, then the smaller key). A stratum never gets more
    than its size; with `n` at least the total, every stratum is taken whole.
    """
    total = sum(sizes.values())
    if n >= total:
        return dict(sizes)
    if n <= 0 or total == 0:
        return {key: 0 for key in sizes}
    quotas = {key: n * size / total for key, size in sizes.items()}
    shares = {key: min(sizes[key], math.floor(quota)) for key, quota in quotas.items()}
    left = n - sum(shares.values())
    order = sorted(
        sizes, key=lambda key: (-(quotas[key] - math.floor(quotas[key])), -sizes[key], key)
    )
    while left > 0:
        progressed = False
        for key in order:
            if left == 0:
                break
            if shares[key] < sizes[key]:
                shares[key] += 1
                left -= 1
                progressed = True
        if not progressed:
            break
    return shares


def _balanced_targets(groups: Mapping[str, int], n: int) -> Dict[str, int]:
    """Equal shares of `n` per group, the remainder (and any shortfall) spread by size."""
    targets = {key: 0 for key in groups}
    left = min(n, sum(groups.values()))
    open_groups = sorted(groups)
    while left > 0 and open_groups:
        share = max(1, left // len(open_groups))
        for key in list(open_groups):
            if left == 0:
                break
            take = min(share, groups[key] - targets[key], left)
            targets[key] += take
            left -= take
            if targets[key] >= groups[key]:
                open_groups.remove(key)
    return targets


def stratified_sample(
    items: Sequence[T],
    stratum: Callable[[T], str],
    n: int,
    seed: int,
    *,
    key: Callable[[T], str],
    balance: Optional[Callable[[T], str]] = None,
) -> List[T]:
    """
    Draw `n` items, stratified by `stratum` (see the module docstring).

    Args:
        items: The population.
        stratum: The stratum of an item (e.g. "label|generator model").
        n: Sample size (all items when n is at least their number).
        seed: Seed of the draw.
        key: A unique, stable key per item (orders candidates before drawing).
        balance: Optional grouping (e.g. the label) whose groups get equal shares
            of `n`; strata are then allocated proportionally within each group.
            Every member of a stratum must be in the same group.

    Returns:
        The sample, in `key` order.

    Raises:
        ValueError: If a stratum spans several balance groups.
    """
    by_stratum: Dict[str, List[T]] = {}
    for item in items:
        by_stratum.setdefault(stratum(item), []).append(item)
    for members in by_stratum.values():
        members.sort(key=key)

    if balance is None:
        shares = allocate({name: len(members) for name, members in by_stratum.items()}, n)
    else:
        group_of: Dict[str, str] = {}
        for name, members in by_stratum.items():
            groups = {balance(member) for member in members}
            if len(groups) != 1:
                raise ValueError(f"stratum {name!r} mixes balance groups {sorted(groups)}")
            group_of[name] = groups.pop()
        group_sizes: Dict[str, int] = {}
        for name, members in by_stratum.items():
            group_sizes[group_of[name]] = group_sizes.get(group_of[name], 0) + len(members)
        targets = _balanced_targets(group_sizes, n)
        shares = {}
        for group, target in targets.items():
            sizes = {
                name: len(members)
                for name, members in by_stratum.items()
                if group_of[name] == group
            }
            shares.update(allocate(sizes, target))

    # A seeded, reproducible draw (not used for anything secret).
    rng = random.Random(seed)  # nosec B311
    picked: List[T] = []
    for name in sorted(by_stratum):
        members = by_stratum[name]
        picked.extend(rng.sample(members, min(shares.get(name, 0), len(members))))
    return sorted(picked, key=key)
