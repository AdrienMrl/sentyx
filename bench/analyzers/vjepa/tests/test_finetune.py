"""Fine-tuning plumbing on a stub transformer: the encoder split, the trunk
cache, the window sampler, both loss branches, the two splits, and a whole
`finetune` run that learns a synthetic contact.

The stub mirrors the shape of Meta's V-JEPA 2.1 VisionTransformer — a
`blocks` ModuleList whose blocks take (x, mask=, T=, H_patches=, W_patches=,
return_attn=, mode=) and return `(x, attn)`, a `norms_block` whose last entry
is the output norm, and a Conv3d patch embedding flattened time-outermost — so
the split, the pre-hook capture and the kwargs pass-through are exercised
against the real calling convention without the 4.8 GB checkpoint. Frames are
injected, so no ffmpeg and no video files are needed.
"""

import os
import sys

import numpy as np
import pytest
import torch
import torch.nn as nn

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, ROOT)

import vjepa_bench  # noqa: E402
from vjb import finetune as ft  # noqa: E402
from vjb.frames import windows as frame_windows  # noqa: E402
from vjb import train as tr  # noqa: E402
from vjb.encoder import EncoderInfo  # noqa: E402
from vjb.heads import HEADS  # noqa: E402

SIZE, FPS, FRAMES, STRIDE, PATCH, TUBELET, DIM = 32, 4, 8, 4, 16, 2, 24


class StubBlock(nn.Module):
    """A residual block with the 2.1 signature, including the extra kwargs the
    tail has to forward and the (x, attn) return."""

    def __init__(self, dim):
        super().__init__()
        self.norm = nn.LayerNorm(dim)
        self.fc = nn.Linear(dim, dim)
        self.seen = None

    def forward(self, x, mask=None, T=None, H_patches=None, W_patches=None, return_attn=False, mode="video"):
        # Record the geometry so a test can prove the kwargs survived the split.
        self.seen = (T, H_patches, W_patches, mode)
        return x + torch.tanh(self.fc(self.norm(x))), None


class StubViT(nn.Module):
    def __init__(self, dim=DIM, depth=6, size=SIZE, patch=PATCH, tubelet=TUBELET):
        super().__init__()
        self.patch_size, self.tubelet_size, self.embed_dim = patch, tubelet, dim
        self.patch_embed = nn.Conv3d(3, dim, (tubelet, patch, patch), stride=(tubelet, patch, patch))
        self.blocks = nn.ModuleList(StubBlock(dim) for _ in range(depth))
        self.norms_block = nn.ModuleList([nn.LayerNorm(dim)])
        self.calls = 0

    def forward(self, x):
        self.calls += 1
        _, _, t, h, w = x.shape
        kw = dict(mask=None, T=t // self.tubelet_size, H_patches=h // self.patch_size,
                  W_patches=w // self.patch_size, return_attn=False, mode="video")
        # (B, D, T', H', W') -> (B, N, D) with time outermost, like PatchEmbed3D.
        y = self.patch_embed(x).flatten(2).transpose(1, 2)
        for blk in self.blocks:
            y, _ = blk(y, **kw)
        return self.norms_block[-1](y)


class StubEncoder:
    """The surface `SplitEncoder` needs from `encoder.VJEPA21Encoder`."""

    def __init__(self, device="cpu", depth=6):
        torch.manual_seed(0)
        self.model = StubViT(depth=depth).to(device).eval()
        self.device, self.dtype = device, torch.float32
        self.info = EncoderInfo("stub", DIM, TUBELET, PATCH, SIZE)
        self.info.kind, self.info.arch = "vjepa21", "stub"
        self.mean = torch.zeros(1, 3, 1, 1, 1, device=device)
        self.std = torch.ones(1, 3, 1, 1, 1, device=device)


def synthetic_frames(n, contact_at=None, seed=0):
    """Grey noise with a bright block in the lower band for 8 frames from
    `contact_at`, the same signal test_plumbing uses."""
    rng = np.random.default_rng(seed)
    f = rng.integers(90, 110, size=(n, SIZE, SIZE, 3), dtype=np.uint8)
    if contact_at is not None:
        f[contact_at:contact_at + 8, SIZE // 2:, SIZE // 4:3 * SIZE // 4] = 235
    return f


class FakeClip(ft.Clip):
    """A Clip whose frames come from memory instead of ffmpeg."""

    def __init__(self, case, meta, frames, flip):
        self.case, self.flip = case, flip
        self.variant = "flip" if flip else "orig"
        self.frames = frames[:, :, ::-1].copy() if flip else frames
        self.starts = frame_windows(len(frames), meta["frames"], meta["stride"])
        self.valid = [min(meta["frames"], len(frames) - s) for s in self.starts]
        self.n_windows = len(self.starts)
        self.length = meta["frames"]


def make_meta():
    enc = StubEncoder()
    return {"encoder": enc.info.as_dict(), "fps": FPS, "size": SIZE, "crop": "full",
            "frames": FRAMES, "stride": STRIDE, "match": "none"}


def synthetic_cases(k_pos=3, k_neg=4, holdout=2):
    """Positives carry an interval so the per-window BCE branch runs; the last
    positive and the last negative are tagged `holdout`."""
    cases = []
    for i in range(k_pos):
        at = 8 + 4 * i
        cases.append({"id": f"pos{i}", "clip": f"/x/pos{i}/c.mp4", "contact": True, "source": "test",
                      "start": at / FPS, "end": at / FPS + 2, "tags": [], "n": 36, "at": at})
    for i in range(k_neg):
        cases.append({"id": f"neg{i}", "clip": f"/x/neg{i}/c.mp4", "contact": False, "source": "test",
                      "start": None, "end": None, "tags": [], "n": 32, "at": None})
    for c in cases[k_pos - holdout // 2 - 1:k_pos] + cases[-1:]:
        c["tags"] = ["holdout"]
    return cases


def loader(cases):
    """A `load` callable for ft.run that fabricates frames per case."""
    by_id = {c["id"]: c for c in cases}

    def load(case, meta, cache_root, flip):
        c = by_id[case["id"]]
        frames = synthetic_frames(c["n"], c["at"], seed=abs(hash(c["id"])) % 1000)
        return FakeClip(case, meta, frames, flip)

    return load


# --------------------------------------------------------------------------


@pytest.mark.parametrize("k", [0, 1, 3, 6])
def test_split_reproduces_the_full_forward(k):
    """trunk + tail must equal the encoder's own forward for every split,
    including K=0 (head-only) and K=depth (everything trainable)."""
    enc = StubEncoder()
    x = synthetic_frames(FRAMES, 4)
    with torch.no_grad():
        want = enc.model(ft.SplitEncoder(StubEncoder(), 0, False).pixels(x))
    se = ft.SplitEncoder(enc, k, checkpointing=False)
    with torch.no_grad():
        h, kwargs = se.trunk(x)
        got = se.tail(h, kwargs)
    assert got.shape == want.shape
    assert torch.allclose(got, want, atol=1e-5), (k, (got - want).abs().max())


def test_split_forwards_block_kwargs():
    """The tail's blocks must see the same geometry the prelude computed."""
    enc = StubEncoder()
    se = ft.SplitEncoder(enc, 2, checkpointing=False)
    with torch.no_grad():
        h, kwargs = se.trunk(synthetic_frames(FRAMES))
        se.tail(h, kwargs)
    t2, h2, w2 = se.info.grid(FRAMES)
    assert enc.model.blocks[-1].seen == (t2, h2, w2, "video")


def test_gradients_reach_only_the_last_k_blocks():
    enc = StubEncoder()
    se = ft.SplitEncoder(enc, 2, checkpointing=True)
    h, kwargs = se.trunk(synthetic_frames(FRAMES, 4))
    out = se.tail(h, kwargs)
    out.sum().backward()
    grads = [p.grad is not None for blk in enc.model.blocks for p in blk.parameters()]
    assert not any(grads[: 4 * 2]), "frozen blocks received gradients"
    assert all(grads[-4 * 2:]), "trainable blocks received no gradients"
    assert len(se.trainable) == 2 * 4 + 2   # two blocks (norm + linear, weight+bias) plus the output norm


def test_trunk_cache_skips_the_frozen_blocks(tmp_path):
    """The second epoch over a window must not run the encoder prelude again."""
    enc = StubEncoder()
    se = ft.SplitEncoder(enc, 2, checkpointing=False)
    meta = make_meta()
    cases = synthetic_cases(1, 0)
    clip = loader(cases)(cases[0], meta, None, False)
    cache = ft.TrunkCache(str(tmp_path), ft.cache_key(meta, 2))
    trainer = ft.Trainer(se, "temporal", meta, dict(lr=1e-3, lr_encoder=1e-4, weight_decay=0.0, epochs=1,
                                                    pos_weight=2.0, mil_temp=4.0, accum=4,
                                                    max_windows_per_clip=0),
                         "cpu", cache)
    trainer.prime_kwargs(clip)
    before = enc.model.calls
    first = [trainer.window_logits(clip, w)[0].detach().clone() for w in range(clip.n_windows)]
    assert enc.model.calls > before, "the first pass must run the frozen trunk"
    assert cache.misses == clip.n_windows
    paths = list(tmp_path.rglob("w*.npy"))
    assert len(paths) == clip.n_windows and ft.cache_key(meta, 2) in str(paths[0])
    mid = enc.model.calls
    second = [trainer.window_logits(clip, w)[0].detach().clone() for w in range(clip.n_windows)]
    assert enc.model.calls == mid, "the cached pass must not touch the frozen trunk"
    assert cache.hits == clip.n_windows
    for a, b in zip(first, second):
        assert torch.allclose(a, b, atol=2e-3), (a, b)   # fp16 on disk


def test_sampler_biases_positives_toward_the_interval():
    meta = make_meta()
    cases = synthetic_cases(1, 1)
    load = loader(cases)
    rng = np.random.default_rng(0)
    pos = load(cases[0], meta, None, False)
    labels = pos.window_labels(FPS, TUBELET)
    assert labels is not None and 0 < labels.sum() < len(labels)
    idx = ft.sample_windows(pos, FPS, TUBELET, 2, rng)
    assert len(idx) == 2 and any(labels[i] for i in idx), (idx, labels)
    neg = load(cases[1], meta, None, False)
    assert neg.window_labels(FPS, TUBELET) is None      # MIL branch
    assert len(ft.sample_windows(neg, FPS, TUBELET, 2, rng)) == 2
    assert ft.sample_windows(neg, FPS, TUBELET, 0, rng) == list(range(neg.n_windows))


def test_window_labels_match_train():
    """The interval -> per-window mapping is train.Example's, not a copy."""
    meta = make_meta()
    cases = synthetic_cases(1, 0)
    clip = loader(cases)(cases[0], meta, None, False)
    mine = clip.window_labels(FPS, TUBELET)
    theirs = tr.Example(cases[0], clip).window_labels(FPS, TUBELET)
    assert np.array_equal(mine, theirs)


def test_loss_branches_match_train_clip_loss():
    """Both branches must reproduce train.clip_loss on the same logits."""
    hp = {"pos_weight": 2.0, "mil_temp": 4.0}
    logits = torch.tensor([-1.0, 0.5, 2.0])
    labels = torch.tensor([0.0, 1.0, 0.0])
    w = torch.where(labels > 0, torch.tensor(hp["pos_weight"]), torch.tensor(1.0))
    want = torch.nn.functional.binary_cross_entropy_with_logits(logits, labels, weight=w)
    assert torch.allclose(ft.window_bce(logits, labels, hp["pos_weight"]), want)

    # MIL: the loss value, and the analytic coefficients against autograd.
    g = logits.clone().requires_grad_(True)
    clip_logit = ft.mil_clip_logit(g, hp["mil_temp"])
    loss = ft.mil_loss(clip_logit, True, hp["pos_weight"])
    loss.backward()
    value, coeffs = ft.mil_backward_coeffs(logits, True, hp)
    assert value == pytest.approx(loss.detach().item())
    assert torch.allclose(coeffs, g.grad, atol=1e-6), (coeffs, g.grad)


def test_split_cases():
    cases = synthetic_cases()
    train, ev = ft.split_cases(cases, "holdout")[0]
    assert ev and train and not ({c["id"] for c in train} & {c["id"] for c in ev})
    assert all("holdout" in c["tags"] for c in ev)
    folds = ft.split_cases(cases, "loo")
    assert len(folds) == len(cases)
    assert all(len(e) == 1 and len(t) == len(cases) - 1 for t, e in folds)
    with pytest.raises(SystemExit):
        ft.split_cases([dict(c, tags=[]) for c in cases], "holdout")


def test_holdout_keeps_a_recording_group_together():
    """A recording sibling of a holdout case must be held out too: case ids are
    not independent observations, recordings are."""
    cases = [{"id": "a", "tags": ["holdout", "real-group:rec1"]},
             {"id": "b", "tags": ["real-group:rec1"]},
             {"id": "c", "tags": []}]
    train, held = ft.split_cases(cases, "holdout")[0]
    assert {c["id"] for c in held} == {"a", "b"}
    assert {c["id"] for c in train} == {"c"}


def test_loo_refuses_a_recording_group_split(tmp_path):
    """Leave-one-case-out is not leave-one-recording-out; if a group would be
    split, the run must stop rather than silently leak the recording."""
    meta = make_meta()
    cases = synthetic_cases(1, 1)
    cases[0]["tags"] = ["real-group:rec1"]
    cases[1]["tags"] = ["real-group:rec1"]
    hp = {"lr": 3e-3, "lr_encoder": 3e-4, "weight_decay": 0.01, "epochs": 1, "pos_weight": 2.0,
          "mil_temp": 4.0, "accum": 2, "max_windows_per_clip": 2, "unfreeze": 1}
    se = ft.SplitEncoder(StubEncoder(), 1, checkpointing=False)
    with pytest.raises(ValueError, match="overlap"):
        ft.run(cases, meta, se, "temporal", hp, "cpu", str(tmp_path), "loo", 1, 0,
               flip_augment=False, stats_windows=1, load=loader(cases))


def test_cache_key_names_everything_that_changes_the_trunk():
    """A cached hidden state belongs to one (pixels, encoder, split). Any change
    that alters the input frames or the network must change the key, or two
    configurations silently share each other's features."""
    meta = make_meta()
    base = ft.cache_key(meta, 2)
    for field, value in (("crop", "bottom"), ("match", "pi"),
                         ("fps", meta["fps"] + 1), ("frames", meta["frames"] + 2)):
        other = dict(meta)
        other[field] = value
        assert ft.cache_key(other, 2) != base, field
    assert ft.cache_key(dict(meta, encoder=dict(meta["encoder"], arch="other_arch")), 2) != base
    assert ft.cache_key(meta, 0) != base


def test_cache_identity_distinguishes_same_arch_checkpoints_and_precision(tmp_path):
    meta = dict(make_meta(), dtype="float32")
    meta["encoder"] = dict(meta["encoder"], arch="same_arch", name="first.pt")
    base = ft.cache_key(meta, 2)
    assert ft.cache_key(dict(meta, encoder=dict(meta["encoder"], name="second.pt")), 2) != base
    assert ft.cache_key(dict(meta, dtype="bfloat16"), 2) != base
    checkpoint = tmp_path / "model.pt"
    checkpoint.write_bytes(b"weights A")
    first = ft.checkpoint_digest(checkpoint)
    checkpoint.write_bytes(b"weights B")
    second = ft.checkpoint_digest(checkpoint)
    assert first != second
    assert ft.cache_key(dict(meta, checkpoint_sha256=first), 2) != ft.cache_key(
        dict(meta, checkpoint_sha256=second), 2)


def test_model_digest_tracks_hub_weights():
    model = torch.nn.Linear(2, 1).to(torch.bfloat16)
    first = ft.model_digest(model)
    assert ft.model_digest(model) == first
    with torch.no_grad():
        model.weight.add_(1)
    assert ft.model_digest(model) != first


def test_run_is_reproducible_from_its_seed(tmp_path):
    """The run seed must pin the head's initial weights. The head is built in
    the fold loop before `Trainer.train` seeds, so without an explicit seed the
    result depends on the process's RNG history and two K arms cannot share an
    initialization."""
    hp = {"lr": 3e-3, "lr_encoder": 3e-4, "weight_decay": 0.01, "epochs": 3, "pos_weight": 2.0,
          "mil_temp": 4.0, "accum": 4, "max_windows_per_clip": 4, "unfreeze": 2}

    def head_and_scores(tag, rng_seed, burn):
        meta = make_meta()
        cases = synthetic_cases()
        se = ft.SplitEncoder(StubEncoder(), 2, checkpointing=True)
        torch.manual_seed(rng_seed)
        if burn:
            torch.randn(burn)
        per_case, _, _, trainer = ft.run(cases, meta, se, "temporal", hp, "cpu", str(tmp_path / tag),
                                         "holdout", hp["epochs"], 0, flip_augment=False,
                                         stats_windows=2, load=loader(cases))
        return trainer.head.state_dict(), per_case

    w1, p1 = head_and_scores("first", 11, 0)
    w2, p2 = head_and_scores("second", 22, 37)
    assert set(w1) == set(w2)
    for k in w1:
        assert torch.equal(w1[k], w2[k]), k
    assert p1 == p2


@pytest.mark.parametrize("unfreeze", [0, 2])
def test_finetune_learns_synthetic_contact(tmp_path, unfreeze):
    """A whole holdout run: cache, sampler, both loss branches, scoring, and a
    summary in the shape `train` writes."""
    meta = make_meta()
    cases = synthetic_cases()
    hp = {"lr": 3e-3, "lr_encoder": 3e-4, "weight_decay": 0.01, "epochs": 12, "pos_weight": 2.0,
          "mil_temp": 4.0, "accum": 4, "max_windows_per_clip": 4, "unfreeze": unfreeze}
    se = ft.SplitEncoder(StubEncoder(), unfreeze, checkpointing=True)
    per_case, locs, evaluated, trainer = ft.run(
        cases, meta, se, "temporal", hp, "cpu", str(tmp_path), "holdout", hp["epochs"], 0,
        flip_augment=True, stats_windows=2, log=sys.stderr, load=loader(cases))
    assert {c["id"] for c in evaluated} == {c["id"] for c in cases if "holdout" in c["tags"]}
    s = tr.summarize(evaluated, per_case, locs, 0.5)
    assert set(s) >= {"cases", "auc", "contact_missed", "contact_phantom", "auc_by_source",
                      "zero_miss_threshold", "loc_hits"}
    pos = [per_case[c["id"]][0] for c in evaluated if c["contact"]]
    neg = [per_case[c["id"]][0] for c in evaluated if not c["contact"]]
    assert min(pos) > max(neg), (pos, neg)
    ck_path = str(tmp_path / "ft.pt")
    ft.save_encoder(ck_path, se, trainer.head, "temporal", meta, hp, 0.5)
    ck = torch.load(ck_path, map_location="cpu", weights_only=False)
    assert ck["unfreeze"] == unfreeze and ck["split"] == se.depth - unfreeze
    assert len(ck["tail"]) == (0 if unfreeze == 0 else 4 * unfreeze + 2)
    assert ck["state"] and ck["head"] == "temporal"


def test_loo_split_runs_every_fold(tmp_path):
    meta = make_meta()
    cases = synthetic_cases(1, 1)
    hp = {"lr": 3e-3, "lr_encoder": 3e-4, "weight_decay": 0.01, "epochs": 1, "pos_weight": 2.0,
          "mil_temp": 4.0, "accum": 4, "max_windows_per_clip": 2, "unfreeze": 1}
    se = ft.SplitEncoder(StubEncoder(), 1, checkpointing=False)
    per_case, locs, evaluated, _ = ft.run(cases, meta, se, "temporal", hp, "cpu", str(tmp_path),
                                          "loo", 1, 0, flip_augment=False, stats_windows=1,
                                          load=loader(cases))
    assert len(evaluated) == len(cases) and set(per_case) == {c["id"] for c in cases}
    assert all(0.0 <= locs[c["id"]][0] <= locs[c["id"]][1] for c in cases)


def test_cli_wires_the_finetune_subcommand():
    a = vjepa_bench.build_parser().parse_args(
        ["finetune", "-cache", "c", "-encoder", "vjepa21", "-model", "m.pt", "-arch",
         "vjepa2_1_vit_large_384", "-src", "s", "-device", "cuda", "-dtype", "bfloat16",
         "-fps", "15", "-size", "384", "-crop", "full", "-match", "none", "-frames", "64",
         "-stride", "16", "-unfreeze", "4", "-epochs", "6", "-lr", "1e-3", "-lr-encoder", "1e-5",
         "-split", "holdout", "-out", "o.json"])
    assert a.fn is vjepa_bench.cmd_finetune
    assert (a.unfreeze, a.lr_encoder, a.frames, a.stride, a.split) == (4, 1e-5, 64, 16, "holdout")
    assert vjepa_bench.ft_hp_from_args(a)["max_windows_per_clip"] == a.max_windows_per_clip
    with pytest.raises(SystemExit):        # -unfreeze has no default
        vjepa_bench.build_parser().parse_args(["finetune", "-cache", "c", "-encoder", "fake"])


@pytest.mark.parametrize("head", sorted(HEADS))
def test_every_head_runs_through_finetune(tmp_path, head):
    """Whatever heads.py offers must survive the fine-tuning path: the head is
    driven by `head(tokens)` here, and the probe's label-free projection is
    fitted from the same statistics pass `train.fit` uses."""
    meta = make_meta()
    cases = synthetic_cases(1, 1)
    cases[-1]["tags"] = ["holdout"]
    hp = {"lr": 3e-3, "lr_encoder": 3e-4, "weight_decay": 0.01, "epochs": 1, "pos_weight": 2.0,
          "mil_temp": 4.0, "accum": 2, "max_windows_per_clip": 2, "unfreeze": 1}
    se = ft.SplitEncoder(StubEncoder(), 1, checkpointing=False)
    per_case, locs, evaluated, _ = ft.run(cases, meta, se, head, hp, "cpu", str(tmp_path),
                                          "holdout", 1, 0, flip_augment=False, stats_windows=2,
                                          load=loader(cases))
    assert len(evaluated) == 1
    p = per_case[evaluated[0]["id"]][0]
    assert 0.0 <= p <= 1.0
