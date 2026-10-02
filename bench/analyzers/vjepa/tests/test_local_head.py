from pathlib import Path
import sys
import numpy as np
import torch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from vjb.heads import LocalGridHead, build_head


def test_shapes_gradients_and_local_aggregation():
    torch.set_num_threads(2)
    head = build_head("local", 4)
    grid = torch.randn(2, 4, 24, 24, 4)
    rep = head.standardize_rep(head.prepare_raw(grid))
    assert rep.shape == (2, 4, 8, 8, 4)
    assert rep.dtype == torch.float16
    window, step = head.forward_rep(rep)
    local = head.local_logits(rep)
    assert torch.equal(step, local.amax(dim=(-1, -2)))
    assert torch.equal(window, step.amax(dim=1))
    window.sum().backward()
    assert all(p.grad is not None and torch.isfinite(p.grad).all() for p in head.parameters())


def test_standardization_and_augmentation():
    head = LocalGridHead(4)
    head.set_stats(torch.arange(4).float(), torch.arange(1, 5).float())
    grid = torch.randn(1, 6, 24, 24, 4)
    expected = head.prepare(head.standardize(grid))
    actual = head.standardize_rep(head.prepare_raw(grid)).float()
    assert torch.allclose(actual, expected, atol=.002, rtol=.002)
    hp = dict(time_crop=.3, token_drop=.1, noise=.01)
    assert torch.isfinite(head.augment(actual, np.random.default_rng(0), hp)).all()


def test_preserves_locations_lost_inside_old_six_patch_region():
    head = LocalGridHead(2)
    a = torch.zeros(1, 4, 24, 24, 2)
    b = torch.zeros_like(a)
    a[0, :, 0, 0, 0] = 1
    b[0, :, 5, 5, 0] = 1
    assert not torch.equal(head.prepare_raw(a), head.prepare_raw(b))
