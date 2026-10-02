"""Scoring, weighted the way Adrien asked for: phantom alerts are the
expensive error, so precision and the false-positive count lead.

With 14 negatives in the whole set a single false positive moves precision by
several points, so every number here is reported with its raw counts and
nothing is quoted without them.
"""

import numpy as np
from sklearn.metrics import average_precision_score, roc_auc_score


def roc_auc(y, s):
    """Ranking quality, tolerant of ties.

    Hand-rolling this matched sklearn exactly over 300 randomised trials, but
    the average-precision twin did not: with tied scores a naive cumulative
    precision counts each tied positive at its most favourable rank and comes
    out several points optimistic. A saturated VLM emits ties constantly — it
    answers "yes" with probability 1.0 on many clips at once — so both metrics
    defer to sklearn rather than keep a second implementation honest.
    """
    return float(roc_auc_score(np.asarray(y, dtype=int), np.asarray(s, dtype=float)))


def average_precision(y, s):
    return float(average_precision_score(np.asarray(y, dtype=int),
                                         np.asarray(s, dtype=float)))


def at_threshold(y, s, thr):
    y = np.asarray(y, dtype=bool)
    pred = np.asarray(s, dtype=float) >= thr
    tp = int((pred & y).sum())
    fp = int((pred & ~y).sum())
    fn = int((~pred & y).sum())
    tn = int((~pred & ~y).sum())
    return {
        "threshold": float(thr),
        "tp": tp, "fp": fp, "fn": fn, "tn": tn,
        "precision": tp / (tp + fp) if tp + fp else float("nan"),
        "recall": tp / (tp + fn) if tp + fn else float("nan"),
        "accuracy": (tp + tn) / len(y),
    }


def best_threshold_for_precision(y, s, min_recall=0.5):
    """The threshold with the highest precision that still finds `min_recall`
    of the contact cases. Chosen on training folds only, never on the fold
    being scored."""
    cands = sorted(set(np.asarray(s, dtype=float).tolist()))
    best, best_key = None, None
    for thr in cands:
        m = at_threshold(y, s, thr)
        if m["recall"] < min_recall:
            continue
        key = (m["precision"], m["recall"])
        if best_key is None or key > best_key:
            best, best_key = thr, key
    return float(best) if best is not None else 0.5


def report(y, s, thr=None, min_recall=0.5):
    thr = best_threshold_for_precision(y, s, min_recall) if thr is None else thr
    out = at_threshold(y, s, thr)
    out["auc"] = float(roc_auc(y, s))
    out["ap"] = float(average_precision(y, s))
    out["n"] = len(y)
    out["n_pos"] = int(np.sum(np.asarray(y, dtype=bool)))
    return out
