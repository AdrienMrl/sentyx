"""Motion-guided frame sampling.

Uniform sampling is the reason the fine-tune had nothing to learn from: a
median clip is 28.7 s, a labeled contact window is 2.0 s, and 8 uniform frames
land inside that window 0.56 times on average. The model was being asked
whether anything touched the car while being shown no frame in which anything
did.

This samples where the footage actually changes instead. The clip is decoded
once at low resolution, consecutive-frame absolute difference gives a motion
signal, and frames are placed at equal quantiles of *cumulative motion* rather
than of time. A clip that is still for 20 s and then has a 2 s scuffle puts
most of its frames in the scuffle.

Two guards matter. A floor mixes a fraction of uniform time back in, so a
clip whose motion signal is flat or whose contact is nearly motionless still
gets coverage rather than eight frames crammed into one second of noise. And
the first differences after a scene cut are discarded, because a hard cut in
reposted footage dwarfs any real motion and would otherwise capture the whole
budget.
"""

import subprocess

import numpy as np


WIDTH, HEIGHT = 96, 64


def motion_signal(clip: str, fps: float = 10.0):
    """Per-interval motion, as mean absolute difference between frames.

    The decode is forced to a fixed WIDTHxHEIGHT rather than letting ffmpeg
    pick the height from the aspect ratio: the raw stream carries no
    dimensions, and inferring them from the byte count is ambiguous — every
    divisor fits, and the smallest one wins, which silently reads one frame as
    thirty and puts sample times past the end of the clip. Aspect distortion
    is irrelevant to a frame-difference signal.
    """
    cmd = ["ffmpeg", "-nostdin", "-loglevel", "error", "-i", clip,
           "-vf", f"fps={fps},scale={WIDTH}:{HEIGHT},format=gray",
           "-f", "rawvideo", "-pix_fmt", "gray", "-"]
    proc = subprocess.run(cmd, capture_output=True, check=True)
    buf = np.frombuffer(proc.stdout, dtype=np.uint8)
    if buf.size < 2 * WIDTH * HEIGHT:
        raise RuntimeError(f"too few frames decoded from {clip}")
    n = buf.size // (WIDTH * HEIGHT)
    frames = buf[:n * WIDTH * HEIGHT].reshape(n, HEIGHT, WIDTH).astype(np.float32)
    diff = np.abs(np.diff(frames, axis=0)).mean(axis=(1, 2))
    return diff, fps


def _drop_cuts(diff: np.ndarray, z: float = 6.0):
    """Zero out hard scene cuts, which are not motion in the scene."""
    if diff.size < 4:
        return diff
    med = np.median(diff)
    mad = np.median(np.abs(diff - med)) + 1e-6
    out = diff.copy()
    out[(diff - med) / (1.4826 * mad) > z] = med
    return out


def motion_times(clip: str, n_frames: int, floor: float = 0.30,
                 fps: float = 10.0):
    """Timestamps for `n_frames`, weighted toward motion.

    `floor` is the share of the sampling mass held uniform, so coverage never
    collapses onto a single burst.
    """
    diff, fps = motion_signal(clip, fps=fps)
    diff = _drop_cuts(diff)
    if diff.sum() <= 0:
        diff = np.ones_like(diff)
    weight = diff / diff.sum()
    uniform = np.full_like(weight, 1.0 / len(weight))
    mass = (1.0 - floor) * weight + floor * uniform

    cdf = np.cumsum(mass)
    cdf /= cdf[-1]
    # midpoints of equal-mass slices, mapped back through the CDF to time
    targets = (np.arange(n_frames) + 0.5) / n_frames
    idx = np.searchsorted(cdf, targets, side="left")
    idx = np.clip(idx, 0, len(cdf) - 1)
    # +1 because diff[i] describes the interval ending at frame i+1
    return np.sort((idx + 1) / fps)
