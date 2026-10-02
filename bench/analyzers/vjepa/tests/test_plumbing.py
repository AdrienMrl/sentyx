"""End-to-end plumbing with the fake encoder and synthetic clips: windowing,
the cache, both heads, cross-validation, and the analyzer verdict. Runs on a
laptop in seconds; needs torch and numpy, not ffmpeg or any weights."""

import json
import os
import subprocess
import sys

import numpy as np
import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, ROOT)

from vjb import frames as fr, store, train as tr  # noqa: E402
from vjb.encoder import FakeEncoder  # noqa: E402

SIZE, FPS, FRAMES, STRIDE = 64, 4, 8, 4


def synthetic_frames(n, contact_at=None, seed=0):
    """Grey noise; a bright block sits in the lower band for 8 frames (2 s at
    FPS) from `contact_at`, matching the labeled interval, then disappears."""
    rng = np.random.default_rng(seed)
    f = rng.integers(90, 110, size=(n, SIZE, SIZE, 3), dtype=np.uint8)
    if contact_at is not None:
        f[contact_at:contact_at + 8, 32:64, 16:48] = 230
    return f


def make_cache(tmp, cases):
    enc = FakeEncoder(SIZE)
    meta = {"encoder": enc.info.as_dict(), "fps": FPS, "size": SIZE, "crop": "full", "frames": FRAMES, "stride": STRIDE, "match": "none"}
    store.write_meta(tmp, meta)
    for c in cases:
        frames = synthetic_frames(c["n"], c.get("at"), seed=hash(c["id"]) % 1000)
        starts = fr.windows(len(frames), FRAMES, STRIDE)
        toks, valid = [], []
        for s in starts:
            w, nv = fr.pad_window(frames, s, FRAMES)
            toks.append(enc.encode(w))
            valid.append(nv)
        store.save_features(store.feature_path(tmp, c["id"], c["clip"]), np.stack(toks), starts, valid, len(frames))
    return meta


def synthetic_cases(k_pos=4, k_neg=6):
    cases = []
    for i in range(k_pos):
        at = 12 + 3 * i
        cases.append({"id": f"pos{i}", "clip": f"/x/pos{i}/c.mp4", "contact": True, "source": "test", "n": 40,
                      "at": at, "start": at / FPS, "end": at / FPS + 2, "tags": []})
    for i in range(k_neg):
        cases.append({"id": f"neg{i}", "clip": f"/x/neg{i}/c.mp4", "contact": False, "source": "test", "n": 36, "start": None,
                      "end": None, "tags": []})
    return cases


def test_windows_cover_clip():
    assert fr.windows(10, 8, 4) == [0, 2]
    assert fr.windows(8, 8, 4) == [0]
    assert fr.windows(3, 8, 4) == [0]
    assert fr.windows(20, 8, 4) == [0, 4, 8, 12]
    w, valid = fr.pad_window(np.zeros((5, 2, 2, 3), np.uint8), 2, 8)
    assert w.shape[0] == 8 and valid == 3


def test_fake_encoder_grid():
    enc = FakeEncoder(SIZE)
    out = enc.encode(synthetic_frames(FRAMES))
    assert out.shape == (FRAMES // 2, SIZE // 16, SIZE // 16, enc.info.hidden)


LOCALIZING = ("temporal", "motion", "region", "local")


@pytest.mark.parametrize("head", ["meanpool", "probe", "temporal", "motion", "region", "local"])
def test_cv_learns_synthetic_contact(tmp_path, head):
    cases = synthetic_cases()
    meta = make_cache(str(tmp_path), cases)
    hp = {"lr": 3e-3, "weight_decay": 0.01, "epochs": 30, "pos_weight": 2.0, "mil_temp": 4.0, "batch": 4,
          "time_crop": 0.2, "token_drop": 0.05, "noise": 0.02}
    per_case, locs = tr.cross_validate(str(tmp_path), cases, meta, head, hp, [0], "cpu", False)
    s = tr.summarize(cases, per_case, locs, 0.5)
    assert s["auc"] is not None and s["auc"] >= 0.9, s
    if head in LOCALIZING:
        # The located span should overlap the labeled interval (+-0.5 s).
        for c in cases:
            if c["contact"]:
                lo, hi = locs[c["id"]]
                assert lo <= c["end"] + 0.5 and hi >= c["start"] - 0.5, (c["id"], locs[c["id"]], c["start"], c["end"])


def test_window_labels_from_interval(tmp_path):
    cases = synthetic_cases(1, 0)
    meta = make_cache(str(tmp_path), cases)
    ex = tr.load_examples(str(tmp_path), cases, False)[0]
    labels = ex.window_labels(meta["fps"], meta["encoder"]["tubelet"])
    assert labels is not None and labels.sum() >= 1 and labels.sum() < len(labels)


def test_analyze_contract(tmp_path):
    """The CLI prints exactly one JSON object with the fields the bench reads."""
    cases = synthetic_cases(3, 3)
    meta = make_cache(str(tmp_path), cases)
    hp = {"lr": 3e-3, "weight_decay": 0.01, "epochs": 20, "pos_weight": 2.0, "mil_temp": 4.0, "batch": 4,
          "time_crop": 0.0, "token_drop": 0.0, "noise": 0.0}
    head = tr.fit_final(str(tmp_path), cases, meta, "temporal", hp, 0, "cpu", False)
    head_file = str(tmp_path / "head.pt")
    tr.save_head(head_file, head, "temporal", meta, hp, 0.5)
    # A cached clip is addressed by <cache>/<case>/<clip>.npz, keyed on the
    # clip's parent directory name, so pretend the bench layout.
    clip = str(tmp_path / "pos0" / "c.mp4")
    out = subprocess.run([sys.executable, os.path.join(ROOT, "vjepa_bench.py"), "analyze", "-head-file", head_file,
                          "-cache", str(tmp_path), "-cache-only", "-device", "cpu", clip],
                         capture_output=True, text=True)
    assert out.returncode == 0, out.stderr
    v = json.loads(out.stdout)
    assert set(v) >= {"description", "contact", "threat", "start_seconds", "end_seconds", "contact_probability"}
    assert v["threat"] in ("none", "low")
    assert v["end_seconds"] >= v["start_seconds"] >= 0


# --- crop modes -------------------------------------------------------------
# These need ffmpeg (the rest of this file does not), so they build a tiny
# clip with a distinct top and bottom and check what the sampler kept.

RED, BLUE, GREEN = (200, 20, 20), (20, 20, 200), (20, 200, 20)


def make_two_band_clip(path, w=640, h=360, n=8, fps=4):
    """A clip whose top 45% is red and bottom 55% blue, with a green marker in
    the bottom-left quarter so a horizontal flip is visible."""
    top = int(round(h * (1 - fr.BOTTOM_FRACTION)))
    f = np.zeros((h, w, 3), np.uint8)
    f[:top] = RED
    f[top:] = BLUE
    f[h - 120 : h - 40, 60 : 200] = GREEN
    raw = np.repeat(f[None], n, axis=0).tobytes()
    cmd = [fr.ffmpeg_exe(), "-v", "error", "-y", "-f", "rawvideo", "-pix_fmt", "rgb24",
           "-s", f"{w}x{h}", "-r", str(fps), "-i", "-", "-c:v", "libx264", "-qp", "0",
           "-pix_fmt", "yuv444p", path]
    subprocess.run(cmd, input=raw, check=True, capture_output=True)
    return top


def dominant(px):
    return int(np.argmax(px))          # 0 red, 1 green, 2 blue


def test_crop_bottom_keeps_the_lower_band(tmp_path):
    clip = str(tmp_path / "bands.mp4")
    make_two_band_clip(clip)
    out = fr.sample_frames(clip, 4, 64, "bottom")
    assert out.shape == (8, 64, 64, 3)
    # Nothing from the red top band survives: blue dominates almost everywhere,
    # except the green marker. Allow a couple of rows of resampling bleed.
    red = (out[..., 0] > out[..., 2]).mean()
    assert red < 0.03, red
    # `squash` on the same clip does keep the red top half.
    full = fr.sample_frames(clip, 4, 64, "squash")
    assert (full[..., 0] > full[..., 2]).mean() > 0.35

    # The green marker sat in the bottom-left; `bottom` keeps it on the left.
    green = np.argwhere(out[0, ..., 1] > np.maximum(out[0, ..., 0], out[0, ..., 2]))
    assert len(green) > 20
    assert green[:, 1].mean() < 32, green[:, 1].mean()


def test_crop_bottom_flip_mirrors(tmp_path):
    clip = str(tmp_path / "bands.mp4")
    make_two_band_clip(clip)
    out = fr.sample_frames(clip, 4, 64, "bottom")
    flipped = fr.sample_frames(clip, 4, 64, "bottom", flip=True)
    assert np.array_equal(flipped, out[:, :, ::-1])
    green = np.argwhere(flipped[0, ..., 1] > np.maximum(flipped[0, ..., 0], flipped[0, ..., 2]))
    assert len(green) > 20
    assert green[:, 1].mean() > 32, green[:, 1].mean()


def test_crop_full_is_a_deprecated_alias_of_squash(capsys):
    fr._warned_aliases.discard("full")
    assert fr.normalize_crop("full") == "squash"
    assert "renamed" in capsys.readouterr().err
    assert fr.crop_filter("full", 64) == fr.crop_filter("squash", 64)
    assert fr.crop_filter("bottom", 64) == fr.crop_filter(f"band:{1 - fr.BOTTOM_FRACTION},1.0", 64)
    with pytest.raises(ValueError):
        fr.crop_filter("sideways", 64)
