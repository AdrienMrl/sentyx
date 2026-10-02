"""Even frame sampling into JPEGs the Qwen processor can read.

Sentry clips run 12-50 s at wildly different resolutions (554x360 up to
1448x938), so every clip is reduced to the same fixed number of frames at the
same long-edge size. Sampling is cached on disk keyed by the settings, because
a cross-validation sweep re-reads the same clips many times and ffmpeg is the
slow part of a pass.

Two ffmpeg details cost an hour each and are worth recording:

* Tesla repeater clips carry non-full-range YUV, which the mjpeg encoder
  refuses outright ("Non full-range YUV is non-standard"), so the filter chain
  ends in an explicit `format=yuvj420p`.
* Some clips have no keyframe in their second half, so input-side `-ss` past
  that point silently produces no frame at all. Frames therefore come from one
  linear decode; the per-frame fallback seeks on the *output* side, which
  decodes from the start and always lands.
"""

import json
import os
import shutil
import subprocess


def probe_duration(clip: str) -> float:
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "csv=p=0", clip],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    return float(out)


SCALE = "scale='if(gt(iw,ih),{s},-2)':'if(gt(iw,ih),-2,{s})',format=yuvj420p"


def frame_times(duration: float, n_frames: int) -> list[float]:
    """Midpoints of `n_frames` equal slices — never the very first or last
    frame, which are often black or a fade in reposted footage."""
    return [duration * (i + 0.5) / n_frames for i in range(n_frames)]


def _single_pass(clip, paths, duration, n_frames, size):
    """One linear decode, keeping a frame every duration/n seconds.

    ffmpeg picks its muxer from the output extension, so the staging files
    have to be real .jpg names in a scratch directory rather than a .tmp
    suffix, which fails with exit 8.
    """
    step = duration / n_frames
    stage = os.path.join(os.path.dirname(paths[0]), "_stage")
    shutil.rmtree(stage, ignore_errors=True)
    os.makedirs(stage, exist_ok=True)
    vf = (f"select='isnan(prev_selected_t)+gte(t-prev_selected_t,{step:.6f})',"
          + SCALE.format(s=size))
    subprocess.run(
        ["ffmpeg", "-nostdin", "-y", "-loglevel", "error",
         "-ss", f"{step / 2:.4f}", "-i", clip,
         "-vf", vf, "-fps_mode", "passthrough", "-frames:v", str(n_frames),
         "-q:v", "3", os.path.join(stage, "%03d.jpg")],
        check=True, capture_output=True,
    )
    got = 0
    for i in range(n_frames):
        s = os.path.join(stage, "%03d.jpg" % (i + 1))
        if os.path.exists(s):
            os.replace(s, paths[i])
            got += 1
    shutil.rmtree(stage, ignore_errors=True)
    return got == n_frames


def _per_frame(clip, paths, duration, n_frames, size):
    """Output-side seek, one decode per frame. Slow but lands anywhere."""
    for path, t in zip(paths, frame_times(duration, n_frames)):
        subprocess.run(
            ["ffmpeg", "-nostdin", "-y", "-loglevel", "error", "-i", clip,
             "-ss", f"{t:.3f}", "-frames:v", "1",
             "-vf", SCALE.format(s=size), "-q:v", "3", path],
            check=True, capture_output=True,
        )
        if not os.path.exists(path):
            raise RuntimeError(f"no frame at {t:.3f}s of {clip}")


def sample_frames(clip: str, out_dir: str, n_frames: int, size: int,
                  force: bool = False, sampling: str = "uniform") -> list[str]:
    os.makedirs(out_dir, exist_ok=True)
    stamp = os.path.join(out_dir, "done.json")
    paths = [os.path.join(out_dir, f"{i:03d}.jpg") for i in range(n_frames)]
    if not force and os.path.exists(stamp) and all(os.path.exists(p) for p in paths):
        return paths

    duration = probe_duration(clip)
    if sampling == "motion":
        # motion sampling picks arbitrary timestamps, so the linear
        # single-pass trick does not apply — every frame is seeked directly
        from .motion import motion_times
        how = "motion"
        _at_times(clip, paths, [float(t) for t in motion_times(clip, n_frames)],
                  size)
    else:
        how = "single-pass"
        if not _single_pass(clip, paths, duration, n_frames, size):
            how = "per-frame"
            _per_frame(clip, paths, duration, n_frames, size)
    missing = [p for p in paths if not os.path.exists(p)]
    if missing:
        raise RuntimeError(f"{clip}: {len(missing)} frames missing after {how}")
    with open(stamp, "w") as fh:
        json.dump({"clip": clip, "n_frames": n_frames, "size": size,
                   "duration": duration, "method": how,
                   "sampling": sampling}, fh)
    return paths


def _at_times(clip, paths, times, size):
    """One decode per frame at explicit timestamps, seeking on the output
    side so a clip with no keyframe in its second half still lands."""
    for path, t in zip(paths, times):
        subprocess.run(
            ["ffmpeg", "-nostdin", "-y", "-loglevel", "error", "-i", clip,
             "-ss", f"{t:.3f}", "-frames:v", "1",
             "-vf", SCALE.format(s=size), "-q:v", "3", path],
            check=True, capture_output=True,
        )
        if not os.path.exists(path):
            raise RuntimeError(f"no frame at {t:.3f}s of {clip}")


def frames_for(case, cache_root: str, n_frames: int, size: int,
               sampling: str = "uniform") -> list[str]:
    suffix = "" if sampling == "uniform" else f"-{sampling}"
    out_dir = os.path.join(cache_root, f"{n_frames}f-{size}px{suffix}", case.id)
    return sample_frames(case.clip, out_dir, n_frames, size, sampling=sampling)
