"""Known source siblings must not silently leak through case-level validation."""
from pathlib import Path
import sys
import pytest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from vjb.train import assert_split_disjoint, cross_validate, holdout_validate


@pytest.mark.parametrize("prefix", ["real-group:", "group:"])
def test_group_overlap_rejected(prefix):
    a = {"id": "a", "tags": [prefix + "recording"]}
    b = {"id": "b", "tags": [prefix + "recording"]}
    with pytest.raises(ValueError, match="recording overlap"):
        assert_split_disjoint([a], [b])


def test_same_id_rejected_and_unrelated_allowed():
    with pytest.raises(ValueError, match="recording overlap"):
        assert_split_disjoint([{"id": "same"}], [{"id": "same"}])
    assert_split_disjoint([{"id": "a", "tags": ["real-group:a"]}],
                          [{"id": "b", "tags": ["real-group:b"]}])


@pytest.mark.parametrize("mode", ["loo", "holdout"])
def test_validation_rejects_before_loading_cache_or_model(mode):
    a = {"id": "before", "tags": ["real-group:cart"]}
    b = {"id": "impact", "tags": ["real-group:cart"]}
    with pytest.raises(ValueError, match="recording overlap"):
        if mode == "loo":
            cross_validate("missing", [a, b], {}, "invalid", {}, [], "cpu", False)
        else:
            holdout_validate("missing", [a], [b], {}, "invalid", {}, [], "cpu", False)
