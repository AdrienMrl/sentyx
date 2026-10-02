"""Cross-validation over a very small labeled set.

42 cases, 28 of them contact. That is few enough that a single case moving
across the decision boundary shifts precision by several points, so:

* folds are stratified on the contact label, keeping the positive rate stable;
* the decision threshold is chosen on the training folds only and applied to
  the held-out fold, never tuned on the fold being scored;
* every run is repeated over several seeds and reported with its spread,
  because one lucky split is not a result at this size.
"""

import numpy as np


def stratified_folds(labels, k=5, seed=0):
    """Indices of `k` folds, each with roughly the same contact rate."""
    labels = np.asarray(labels, dtype=bool)
    rng = np.random.default_rng(seed)
    folds = [[] for _ in range(k)]
    for value in (True, False):
        idx = np.flatnonzero(labels == value)
        rng.shuffle(idx)
        for j, i in enumerate(idx):
            folds[j % k].append(int(i))
    return [sorted(f) for f in folds]


def splits(labels, k=5, seed=0):
    """(train_idx, test_idx) for each fold."""
    folds = stratified_folds(labels, k, seed)
    everything = set(range(len(labels)))
    for f in folds:
        test = sorted(f)
        train = sorted(everything - set(test))
        yield train, test


def describe(labels, k=5, seed=0):
    out = []
    for i, (tr, te) in enumerate(splits(labels, k, seed)):
        pos = sum(1 for j in te if labels[j])
        out.append(f"fold {i}: train={len(tr)} test={len(te)} (test pos={pos})")
    return "\n".join(out)
