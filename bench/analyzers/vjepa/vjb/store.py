"""The feature cache and the bench dataset.

Layout of a cache directory (one per encoder configuration):

    meta.json                      encoder + sampling config, written by `encode`
    <case>/<clip>.npz              tokens for every window of that clip
    <case>/<clip>.flip.npz         the horizontally flipped copy, when -flip was given

Each npz holds:
    tokens  float16 (W, T', H', W', D)   the encoder output per window
    starts  int32   (W,)                 first sampled frame of each window
    valid   int32   (W,)                 frames in the window that were real, not padding
    n_frames int                         sampled frames in the whole clip

Everything a head needs to map a token back to a second is in meta.json (fps)
and `starts`, so the same cache serves clip-level and per-timestep training.
"""

import json
import os

import numpy as np

META = "meta.json"


def dataset_cases(dataset_path):
    """Labeled cases from bench/dataset.json as a list of dicts with
    id, clip (absolute path), contact, start, end. Unlabeled cases are skipped
    with a note on stderr, mirroring the Go runner."""
    import sys

    with open(dataset_path) as f:
        ds = json.load(f)
    if ds.get("version") != 1:
        raise ValueError(f"{dataset_path}: dataset version {ds.get('version')}, want 1")
    clips_dir = os.path.join(os.path.dirname(os.path.abspath(dataset_path)), "clips")
    cases = []
    for c in ds["cases"]:
        if not c.get("label"):
            print(f"skip {c['id']}: unlabeled", file=sys.stderr)
            continue
        if len(c["clips"]) != 1:
            raise ValueError(f"{c['id']}: expected exactly one clip, got {len(c['clips'])}")
        lab = c["label"]
        cases.append({
            "id": c["id"],
            "clip": os.path.join(clips_dir, c["id"], c["clips"][0]),
            "contact": bool(lab["contact"]),
            "start": lab.get("start_seconds"),
            "end": lab.get("end_seconds"),
            "tags": c.get("tags") or [],
        })
    if not cases:
        raise ValueError("no labeled cases")
    return cases


def write_meta(cache_dir, meta):
    os.makedirs(cache_dir, exist_ok=True)
    path = os.path.join(cache_dir, META)
    if os.path.exists(path):
        with open(path) as f:
            old = json.load(f)
        if old != meta:
            raise RuntimeError(f"{cache_dir} was encoded with a different configuration:\n"
                               f"  cached: {old}\n  now:    {meta}\nuse a new -cache directory")
        return
    with open(path, "w") as f:
        json.dump(meta, f, indent=1, sort_keys=True)


def read_meta(cache_dir):
    with open(os.path.join(cache_dir, META)) as f:
        return json.load(f)


def feature_path(cache_dir, case_id, clip, flip=False):
    base = os.path.splitext(os.path.basename(clip))[0]
    return os.path.join(cache_dir, case_id, base + (".flip" if flip else "") + ".npz")


def save_features(path, tokens, starts, valid, n_frames):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    np.savez(path, tokens=tokens.astype(np.float16), starts=np.asarray(starts, np.int32),
             valid=np.asarray(valid, np.int32), n_frames=np.int32(n_frames))


class ClipFeatures:
    """One clip's cached windows, kept as float16 on the host (a long clip is
    hundreds of MB); consumers upcast on the device."""

    def __init__(self, path):
        z = np.load(path)
        self.tokens = z["tokens"]                       # (W, T', H', W', D) float16
        self.starts = z["starts"]
        self.valid = z["valid"]
        self.n_frames = int(z["n_frames"])

    @property
    def n_windows(self):
        return self.tokens.shape[0]

    def window_seconds(self, w, fps, tubelet):
        """(start_s, end_s) of the real frames in window w."""
        s = self.starts[w]
        return s / fps, (s + self.valid[w]) / fps

    def timestep_seconds(self, w, t, fps, tubelet):
        """Timestamp of token row t in window w: the middle of its tubelet."""
        return (self.starts[w] + t * tubelet + (tubelet - 1) / 2) / fps
