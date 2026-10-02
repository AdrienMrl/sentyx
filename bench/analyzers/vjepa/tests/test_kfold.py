"""Recording-group k-fold splits and the exact one-sided error bounds."""
from pathlib import Path
import sys
import pytest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
from vjb.train import grouped_folds, summarize_groups, upper_rate_bound, cross_validate_groups
from test_plumbing import make_cache, synthetic_cases, FPS


def case(cid, contact, group=None, source="reddit", start=None):
    tags = [f"real-group:{group}"] if group else []
    return {"id": cid, "contact": contact, "tags": tags, "source": source,
            "start": start, "end": (start + 1 if start is not None else None)}


def test_groups_are_never_split_across_folds():
    cases = [case("a1", True, "rec1"), case("a2", False, "rec1"),
             case("b1", True, "rec2"), case("c1", False, None), case("c2", True, None)]
    folds = grouped_folds(cases, 3)
    where = {c["id"]: i for i, fold in enumerate(folds) for c in fold}
    assert where["a1"] == where["a2"]
    assert len(folds) == 3
    assert sorted(c["id"] for fold in folds for c in fold) == ["a1", "a2", "b1", "c1", "c2"]


def test_folds_are_class_balanced_when_possible():
    cases = [case(f"p{i}", True, f"gp{i}") for i in range(6)] + \
            [case(f"n{i}", False, f"gn{i}") for i in range(6)]
    folds = grouped_folds(cases, 3)
    positives = [sum(c["contact"] for c in fold) for fold in folds]
    assert positives == [2, 2, 2]


def test_k_must_be_at_least_two():
    with pytest.raises(ValueError, match="k must be at least 2"):
        grouped_folds([case("a", True)], 1)


def test_upper_bound_matches_clopper_pearson_reference():
    # 0/11 and 0/59 are the numbers the advisor cited.
    assert upper_rate_bound(0, 11) == pytest.approx(0.238, abs=0.002)
    assert upper_rate_bound(0, 59) == pytest.approx(0.0495, abs=0.002)
    assert upper_rate_bound(1, 11) > upper_rate_bound(0, 11)
    assert upper_rate_bound(0, 0) is None


def test_summarize_groups_reports_rates_and_folds():
    cases = [case("p1", True, start=3.0), case("p2", True, start=None),
             case("n1", False), case("n2", False)]
    # p1 and n1 are predicted correctly at 0.5; p2 missed; n2 phantom.
    per_case = {"p1": [0.9], "p2": [0.1], "n1": [0.2], "n2": [0.7]}
    locs = {c["id"]: (0.0, 0.0) for c in cases}
    fold_of = {"p1": 1, "n1": 1, "p2": 2, "n2": 2}
    s = summarize_groups(cases, per_case, locs, 0.5, fold_of)
    assert s["contact_missed"] == 1 and s["contact_phantom"] == 1
    assert s["false_positive_rate"] == pytest.approx(0.5)
    assert s["false_negative_rate"] == pytest.approx(0.5)
    assert s["fpr_upper95"] == pytest.approx(0.9747, abs=0.001)
    assert s["by_fold"][1] == {"cases": 2, "positives": 1, "negatives": 1, "missed": 0, "phantoms": 0}


def test_aux_cases_train_every_fold_but_are_not_scored(tmp_path):
    cases = synthetic_cases(k_pos=2, k_neg=2)
    for c in cases:
        c["tags"] = list(c.get("tags") or [])
    aux = {"id": "aux0", "clip": "/x/aux0/c.mp4", "contact": True, "source": "syn", "n": 40,
           "at": 8, "start": 8 / FPS, "end": 8 / FPS + 2, "tags": ["aux"]}
    allc = cases + [aux]
    meta = make_cache(str(tmp_path), allc)
    hp = {"lr": 1e-3, "weight_decay": 0.0, "epochs": 2, "pos_weight": 1.0, "mil_temp": 4.0,
          "batch": 2, "time_crop": 0.2, "token_drop": 0.0, "noise": 0.0}
    per_case, locs, fold_of, thresholds = cross_validate_groups(str(tmp_path), allc, meta, "temporal", hp,
                                                                [0], "cpu", False, 2)
    assert "aux0" not in per_case
    assert set(per_case) == {c["id"] for c in cases}
    assert set(fold_of) == {c["id"] for c in cases}
