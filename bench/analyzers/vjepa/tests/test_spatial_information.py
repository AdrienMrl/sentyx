"""Representation collisions, not claims of pixel-level model invariance."""
import torch
from vjb.heads import TemporalHead, MotionHead, RegionHead


def test_temporal_and_motion_pooling_discard_fixed_spatial_permutation():
    # One token trajectory moves from left to right, with all time values kept.
    grid = torch.zeros(1, 4, 4, 4, 2)
    grid[0, :, 0, 0, 0] = torch.arange(4)
    moved = grid.flip(3)
    assert not torch.equal(grid, moved)
    for kind in (TemporalHead, MotionHead):
        head = kind(2)
        assert torch.equal(head.prepare_raw(grid), head.prepare_raw(moved))


def test_region_pooling_retains_cross_region_but_not_within_region_location():
    grid = torch.zeros(1, 4, 24, 24, 2)
    grid[0, :, 0, 0, 0] = torch.arange(4)
    within = torch.zeros_like(grid)
    within[0, :, 5, 5, 0] = torch.arange(4)
    across = torch.zeros_like(grid)
    across[0, :, 12, 12, 0] = torch.arange(4)
    head = RegionHead(2, regions=4)
    assert torch.equal(head.prepare_raw(grid), head.prepare_raw(within))
    assert not torch.equal(head.prepare_raw(grid), head.prepare_raw(across))
