"""The bench -analyzer contract: clip paths in, one JSON verdict on stdout.

The verdict's `threat` is derived from contact alone (none / low): this
analyzer detects contact and localizes it, it does not rate severity, so the
bench's threat columns say nothing about it. `contact_probability` is kept so a
stored run can be re-thresholded without re-encoding.
"""

import json
import os
import sys

import numpy as np

from .frames import match_clip, pad_window, sample_frames, windows
from .store import ClipFeatures, feature_path, save_features


def encode_clip(encoder, clip, meta, match_dir, flip=False, log=sys.stderr):
    """Sample and encode one clip according to `meta`; returns the npz fields.
    `match_dir` holds the domain-matched transcodes (see frames.match_clip)."""
    src = match_clip(clip, meta["match"], match_dir)
    frames = sample_frames(src, meta["fps"], meta["size"], meta["crop"], flip=flip)
    starts = windows(len(frames), meta["frames"], meta["stride"])
    toks, valid = [], []
    for s in starts:
        w, n = pad_window(frames, s, meta["frames"])
        toks.append(encoder.encode(w))
        valid.append(n)
    print(f"  {os.path.basename(clip)}{' (flip)' if flip else ''}: {len(frames)} frames, {len(starts)} windows",
          file=log)
    return np.stack(toks), starts, valid, len(frames)


def features_for(encoder, clip, meta, cache_dir):
    """Cached features when the clip is a bench case in the cache, otherwise a
    fresh encode. Lets the analyzer run on a laptop over a cache produced on
    the GPU box, and keeps the bench run from paying for encoding twice."""
    if cache_dir:
        case_id = os.path.basename(os.path.dirname(os.path.abspath(clip)))
        p = feature_path(cache_dir, case_id, clip)
        if os.path.exists(p):
            return ClipFeatures(p)
        if encoder is None:
            raise FileNotFoundError(f"{p}: not in cache and no encoder given (drop -cache-only)")
    if encoder is None:
        raise ValueError("no encoder and no cache")
    match_dir = os.path.join(cache_dir or ".", "_matched", "_adhoc")
    tokens, starts, valid, n = encode_clip(encoder, clip, meta, match_dir)
    tmp = os.path.join(cache_dir or ".", "_adhoc", os.path.basename(clip) + ".npz") if cache_dir else None
    if tmp:
        save_features(tmp, tokens, starts, valid, n)
        return ClipFeatures(tmp)
    z = type("F", (), {})()
    z.tokens, z.starts, z.valid, z.n_frames = tokens.astype(np.float16), np.asarray(starts), np.asarray(valid), n
    z.n_windows = tokens.shape[0]
    z.window_seconds = lambda w, fps, tb: (z.starts[w] / fps, (z.starts[w] + z.valid[w]) / fps)
    z.timestep_seconds = lambda w, t, fps, tb: (z.starts[w] + t * tb + (tb - 1) / 2) / fps
    return z


def verdict(prob, loc, threshold, head_kind, encoder_name, n_clips):
    contact = prob >= threshold
    start, end = (int(round(loc[0])), int(round(loc[1]))) if loc else (0, 0)
    desc = (f"V-JEPA 2 {head_kind} head: contact probability {prob:.2f} "
            f"({'above' if contact else 'below'} threshold {threshold:.2f}); "
            f"most suspicious span {start}-{end}s. Severity is not rated by this analyzer.")
    v = {
        "description": desc,
        "contact": bool(contact),
        "contact_probability": round(float(prob), 4),
        "start_seconds": start,
        "end_seconds": max(end, start),
        "threat": "low" if contact else "none",
        "analyzer": {"encoder": encoder_name, "head": head_kind, "clips_seen": n_clips},
    }
    return json.dumps(v)
