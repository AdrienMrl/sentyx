"""Positive temporal support must survive augmentation until targets follow it."""
from types import SimpleNamespace
from pathlib import Path
import sys
import numpy as np
import pytest
import torch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from vjb.train import clip_loss
from vjb.heads import _time_crop


class RecordingHead:
    def augment(self, rep, rng, hp):
        self.hp = hp
        return rep

    def forward_rep(self, rep):
        return rep, None


@pytest.mark.parametrize("timed", [True, False])
def test_positive_crop_disabled_without_mutating_config(timed):
    hp = dict(time_crop=.3, pos_weight=2., mil_temp=4., token_drop=.1, noise=.05)
    ex = SimpleNamespace(case={"contact": True}, rep=torch.zeros(2),
                         window_labels=lambda fps, tubelet: np.array([0., 1.], dtype=np.float32) if timed else None)
    head = RecordingHead()
    loss = clip_loss(head, ex, 3., 2, "cpu", np.random.default_rng(0), hp)
    assert torch.isfinite(loss)
    assert head.hp["time_crop"] == 0
    assert head.hp["token_drop"] == hp["token_drop"]
    assert head.hp["noise"] == hp["noise"]
    assert hp["time_crop"] == .3


def test_negative_crop_kept():
    hp = dict(time_crop=.3, pos_weight=2., mil_temp=4.)
    ex = SimpleNamespace(case={"contact": False}, rep=torch.zeros(2),
                         window_labels=lambda fps, tubelet: None)
    head = RecordingHead()
    clip_loss(head, ex, 3., 2, "cpu", np.random.default_rng(0), hp)
    assert head.hp["time_crop"] == .3


def test_actual_crop_can_remove_only_contact_but_positive_loss_preserves_it():
    class FixedRng:
        def uniform(self, low, high):
            return high

        def integers(self, low, high):
            return 1

    class CropHead:
        def augment(self, rep, rng, hp):
            return _time_crop(rep, rng, hp["time_crop"])

        def forward_rep(self, rep):
            return rep.max(dim=1).values, None

    rep = torch.tensor([[10., -10., -10., -10., -10., -10., -10., -10.]])
    hp = dict(time_crop=.3, pos_weight=1., mil_temp=4.)
    head, rng = CropHead(), FixedRng()
    # Old code trains an all-negative crop against a positive target.
    old_logit, _ = head.forward_rep(head.augment(rep, rng, hp))
    assert old_logit.item() == -10.
    ex = SimpleNamespace(case={"contact": True}, rep=rep,
                         window_labels=lambda fps, tubelet: np.ones(1, dtype=np.float32))
    assert clip_loss(head, ex, 3., 2, "cpu", rng, hp).item() < .001
