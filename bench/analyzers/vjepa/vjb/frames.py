"""Frame sampling: a clip becomes a uint8 array (T, S, S, 3) at a fixed fps.

All geometry happens in ffmpeg so the encoder's own processor sees frames that
are already the model's square input size and does nothing but normalize.
"""

import os
import re
import shutil
import subprocess
import sys

import numpy as np


def ffmpeg_exe():
    exe = shutil.which("ffmpeg")
    if exe:
        return exe
    try:
        import imageio_ffmpeg
    except ImportError as e:
        raise RuntimeError("ffmpeg is not on PATH and imageio-ffmpeg is not installed") from e
    return imageio_ffmpeg.get_ffmpeg_exe()


# The fraction of frame height the `bottom` mode keeps. In Sentry footage the
# car's own body edge runs along the bottom of every camera view and a person
# touching it is in the lower half, so the contact zone is always in this band.
BOTTOM_FRACTION = 0.55

# Renamed modes. The old name stays accepted so a cache whose meta.json still
# says "full" keeps loading; it is normalized wherever a crop spec is read.
CROP_ALIASES = {"full": "squash"}

_warned_aliases = set()


def normalize_crop(crop):
    """Map a deprecated crop name onto its current one, noting it once."""
    if crop not in CROP_ALIASES:
        return crop
    new = CROP_ALIASES[crop]
    if crop not in _warned_aliases:
        _warned_aliases.add(crop)
        print(f"note: crop {crop!r} was renamed to {new!r}; the old name still works", file=sys.stderr)
    return new


def crop_filter(crop, size):
    """Translate a --crop spec into an ffmpeg -vf chain producing size x size.

    squash    squash the whole frame to a square (keeps every pixel, distorts aspect)
    center    resize the short side to `size`, center-crop (what the HF processor does)
    bottom    keep the bottom BOTTOM_FRACTION of the height at full width -- the
              contact zone -- then squash it to a square, doubling the pixels on it
    band:A,B  keep rows A..B (fractions of height, e.g. 0.35,1.0 for the lower
              band where the ego body sits), then squash to a square
    """
    crop = normalize_crop(crop)
    if crop == "squash":
        return f"scale={size}:{size}:flags=area"
    if crop == "center":
        return f"scale='if(gt(iw,ih),-2,{size})':'if(gt(iw,ih),{size},-2)':flags=area,crop={size}:{size}"
    if crop == "bottom":
        return band_filter(1 - BOTTOM_FRACTION, 1.0, size)
    if crop.startswith("band:"):
        a, b = (float(x) for x in crop[5:].split(","))
        return band_filter(a, b, size)
    raise ValueError(f"unknown crop {crop!r}; expected squash, center, bottom, or band:A,B")


def band_filter(a, b, size):
    """Keep rows a..b (fractions of height, full width), then squash to a square."""
    if not (0 <= a < b <= 1):
        raise ValueError(f"band fractions must satisfy 0 <= A < B <= 1, got {a},{b}")
    return f"crop=iw:ih*{b - a}:0:ih*{a},scale={size}:{size}:flags=area"


def probe(clip):
    """(fps, width, height, duration_s, bitrate_bps) parsed from ffmpeg -i."""
    r = subprocess.run([ffmpeg_exe(), "-i", clip], capture_output=True, text=True)
    err = r.stderr
    m = re.search(r"Duration: (\d+):(\d+):([\d.]+).*?bitrate: (\d+) kb/s", err)
    if not m:
        raise RuntimeError(f"{clip}: could not parse duration/bitrate from ffmpeg")
    dur = int(m[1]) * 3600 + int(m[2]) * 60 + float(m[3])
    v = re.search(r"Video:.*?, (\d+)x(\d+).*?([\d.]+) fps", err)
    if not v:
        raise RuntimeError(f"{clip}: could not parse video stream from ffmpeg")
    return float(v[3]), int(v[1]), int(v[2]), dur, int(m[4]) * 1000


# Domain matching profiles: every clip is pushed through the same transcode the
# in-car unit applies before upload (internal/videocompress), so a clip that was
# downloaded at 30 fps and a field clip that arrived at 3 fps look alike to the
# encoder. Otherwise frame-rate and compression alone separate the two sources.
MATCH_PROFILES = {
    "none": None,
    "pi": {"fps": 3, "max_width": 640, "target_ratio": 0.55, "min_bitrate": 64_000, "max_bitrate": 300_000},
}


def match_clip(clip, profile, out_dir):
    """Return the path of `clip` transcoded to `profile` (cached in out_dir), or
    `clip` itself when the profile is none or the clip already conforms."""
    if profile not in MATCH_PROFILES:
        raise ValueError(f"unknown match profile {profile!r}; choose from {sorted(MATCH_PROFILES)}")
    prof = MATCH_PROFILES[profile]
    if prof is None:
        return clip
    fps, w, h, dur, br = probe(clip)
    if fps <= prof["fps"] + 0.5 and w <= prof["max_width"]:
        return clip                                   # already what the unit uploads
    os.makedirs(out_dir, exist_ok=True)
    out = os.path.join(out_dir, os.path.splitext(os.path.basename(clip))[0] + f".{profile}.mp4")
    if os.path.exists(out) and os.path.getmtime(out) >= os.path.getmtime(clip):
        return out
    target = int(min(prof["max_bitrate"], max(prof["min_bitrate"], br * prof["target_ratio"])))
    vf = f"fps={prof['fps']},scale='min(iw,{prof['max_width']})':-2"
    cmd = [ffmpeg_exe(), "-v", "error", "-y", "-i", clip, "-vf", vf, "-an",
           "-c:v", "libx264", "-preset", "medium", "-b:v", str(target), "-maxrate", str(target),
           "-bufsize", str(2 * target), "-pix_fmt", "yuv420p", out + ".tmp.mp4"]
    subprocess.run(cmd, check=True, capture_output=True)
    os.replace(out + ".tmp.mp4", out)
    return out


def sample_frames(clip, fps, size, crop, flip=False):
    """Decode `clip` at `fps` into an array of shape (T, size, size, 3), RGB.

    Frame i sits at timestamp i / fps (ffmpeg's fps filter emits the first
    frame at t=0). A horizontal flip is applied in ffmpeg when requested so a
    flipped copy is a genuinely different encoder input, not a feature trick.
    """
    if not os.path.exists(clip):
        raise FileNotFoundError(clip)
    vf = f"fps={fps:g}," + crop_filter(crop, size)
    if flip:
        vf += ",hflip"
    cmd = [ffmpeg_exe(), "-v", "error", "-i", clip, "-vf", vf,
           "-f", "rawvideo", "-pix_fmt", "rgb24", "-"]
    out = subprocess.run(cmd, check=True, capture_output=True).stdout
    per = size * size * 3
    n = len(out) // per
    if n == 0:
        raise RuntimeError(f"ffmpeg produced no frames for {clip}")
    return np.frombuffer(out[: n * per], dtype=np.uint8).reshape(n, size, size, 3)


def windows(n_frames, frames_per_window, stride):
    """Start indices of windows covering n_frames; the last window may run past
    the end and is padded by the caller. Always yields at least one window."""
    if frames_per_window <= 0 or stride <= 0:
        raise ValueError("frames_per_window and stride must be positive")
    starts = list(range(0, max(1, n_frames - frames_per_window + 1), stride))
    last_end = starts[-1] + frames_per_window
    if last_end < n_frames:
        starts.append(n_frames - frames_per_window)
    return starts


def pad_window(frames, start, length):
    """frames[start:start+length], repeating the final frame when short."""
    w = frames[start : start + length]
    if len(w) < length:
        pad = np.repeat(w[-1:], length - len(w), axis=0)
        w = np.concatenate([w, pad], axis=0)
    return w, min(length, len(frames) - start)
