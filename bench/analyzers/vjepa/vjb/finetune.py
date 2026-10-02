"""End-to-end fine-tuning of the last K transformer blocks of a V-JEPA 2.1
encoder together with a head, from video frames.

Why this exists: a frozen encoder plus a head plateaus because the frozen
features encode *proximity* — how close two things are on screen — not
*contact*. The hypothesis is that the last few blocks can be adapted to make
contact separable while everything below stays the general video model.

The whole design is one trick. A window's path through the network splits at
block `depth - K`:

    pixels -> [blocks 0 .. depth-K)  frozen, no_grad, bf16 ] -> h
    h      -> [blocks depth-K .. depth) + final norm, trainable ] -> tokens
    tokens -> head -> window logit

`h` never changes, because nothing below the split is ever updated. So the
first epoch writes every window's `h` to disk as fp16 and every epoch after
that reads it back and only runs the K trainable blocks. On a 24-block ViT-L
with K=4 that is a ~6x saving per epoch, and it is what makes fine-tuning fit
in the time budget of a 12 GB card.

Memory, per window, at 384 px / 64 frames (18432 tokens x 1024 dims):

    h                       37 MB fp16 on disk, 74 MB fp32 on the device
    trainable activations   K x ~74 MB with gradient checkpointing
    frozen weights          ~0.6 GB bf16

so a batch of one window with gradient accumulation is the unit of work, and
`-accum` says how many windows a step averages.

The split is found by hooking `model.blocks[split]` and letting the model's own
`forward` run the prelude (patch embedding, rope, modality embeddings), so no
part of Meta's code is reimplemented here and a checkpoint that changes the
prelude keeps working.
"""

import hashlib
import json
import os
import sys
import time

import numpy as np
import torch
import torch.nn.functional as F

from . import train as tr
from .frames import match_clip, pad_window, sample_frames, windows
from .heads import build_head


# --------------------------------------------------------------------------
# splitting the encoder
# --------------------------------------------------------------------------


class _Captured(Exception):
    """Carries a block's inputs out of the model's own forward."""

    def __init__(self, args, kwargs):
        super().__init__("trunk captured")
        self.c_args, self.c_kwargs = args, kwargs


def capture_block_input(model, x, index):
    """Run `model`'s forward until block `index` and return (hidden, kwargs).

    The model's prelude is executed by the model itself; a pre-hook on the
    block aborts the forward as soon as the hidden state reaches it. `kwargs`
    are the per-block arguments (mask, T, H_patches, W_patches, ...) that the
    tail has to pass on unchanged.
    """

    def pre(_mod, args, kwargs):
        raise _Captured(args, kwargs)

    handle = model.blocks[index].register_forward_pre_hook(pre, with_kwargs=True)
    try:
        model(x)
    except _Captured as c:
        return c.c_args[0], dict(c.c_kwargs)
    finally:
        handle.remove()
    raise RuntimeError(f"block {index} never ran; the model's forward does not reach it")


def final_norm(model):
    """The norm the encoder applies after its last block, or None."""
    norms = getattr(model, "norms_block", None)
    if norms is not None:
        return norms[-1]
    return getattr(model, "norm", None)


def run_blocks(model, h, kwargs, index, checkpointing):
    """Blocks [index:] plus the final norm. Blocks of the 2.1 code return
    (x, attn); the V-JEPA 2 code returns x."""
    for blk in model.blocks[index:]:
        if checkpointing and torch.is_grad_enabled():
            # Non-reentrant checkpointing passes keyword arguments through and
            # preserves the RNG state, so the rotary attention (which casts q/k
            # to v.dtype in third_party) recomputes to the same values.
            out = torch.utils.checkpoint.checkpoint(blk, h, use_reentrant=False, **kwargs)
        else:
            out = blk(h, **kwargs)
        h = out[0] if isinstance(out, tuple) else out
    norm = final_norm(model)
    return norm(h) if norm is not None else h


class SplitEncoder:
    """A V-JEPA encoder cut in two at `depth - unfreeze`.

    `enc` is anything shaped like `encoder.VJEPA21Encoder`: `.model` (a
    VisionTransformer with `.blocks`), `.info`, `.device`, `.dtype`, and the
    `.mean`/`.std` pixel normalization tensors. With `unfreeze == 0` the trunk
    is the whole model and the tail is the identity, which reproduces the
    frozen path exactly.
    """

    def __init__(self, enc, unfreeze, checkpointing):
        if unfreeze < 0:
            raise ValueError("-unfreeze must be >= 0")
        self.enc = enc
        self.model = enc.model
        self.info = enc.info
        self.device = enc.device
        self.dtype = enc.dtype
        self.depth = len(self.model.blocks)
        if unfreeze > self.depth:
            raise ValueError(f"-unfreeze {unfreeze} exceeds the encoder's {self.depth} blocks")
        self.unfreeze = unfreeze
        self.split = self.depth - unfreeze
        self.checkpointing = checkpointing
        self.model.eval()
        for p in self.model.parameters():
            p.requires_grad_(False)
        self.trainable = []
        for blk in self.model.blocks[self.split:]:
            blk.to(torch.float32)
            for p in blk.parameters():
                p.requires_grad_(True)
                self.trainable.append(p)
        if unfreeze:
            norm = final_norm(self.model)
            if norm is not None:
                norm.to(torch.float32)
                for p in norm.parameters():
                    p.requires_grad_(True)
                    self.trainable.append(p)

    def pixels(self, window):
        """(T, S, S, 3) uint8 -> the normalized (1, 3, T, S, S) tensor the
        encoder expects, in the frozen dtype."""
        s = self.info.size
        if window.shape[1] != s or window.shape[2] != s:
            raise ValueError(f"frames are {window.shape[1:3]}, encoder expects {s}x{s}")
        x = torch.from_numpy(np.ascontiguousarray(window)).to(self.device)
        x = x.permute(3, 0, 1, 2).unsqueeze(0).float() / 255.0
        return ((x - self.enc.mean) / self.enc.std).to(self.dtype)

    @torch.no_grad()
    def trunk(self, window):
        """The frozen part: uint8 frames -> hidden state at the split, plus the
        block kwargs. With unfreeze == 0 this is the encoder's whole output."""
        x = self.pixels(window)
        if self.unfreeze == 0:
            return self.model(x), {}
        return capture_block_input(self.model, x, self.split)

    def tail(self, h, kwargs):
        """The trainable part: hidden state -> (T', H', W', D) tokens."""
        if self.unfreeze == 0:
            out = h
        else:
            out = run_blocks(self.model, h.to(torch.float32), kwargs, self.split, self.checkpointing)
        return out

    def tail_state(self):
        """Everything the fine-tuning can change: the unfrozen blocks and, when
        there are any, the final norm. Named by absolute block index so a
        checkpoint says where in the encoder it belongs."""
        state = {}
        for i, blk in enumerate(self.model.blocks[self.split:], start=self.split):
            for k, v in blk.state_dict().items():
                state[f"blocks.{i}.{k}"] = v
        if self.unfreeze:
            norm = final_norm(self.model)
            if norm is not None:
                for k, v in norm.state_dict().items():
                    state[f"final_norm.{k}"] = v
        return state

    @torch.no_grad()
    def load_tail_state(self, state):
        live = self.tail_state()
        if set(live) != set(state):
            raise ValueError("tail state does not match this split")
        for k, v in live.items():
            v.copy_(state[k].to(v.device))

    def grid(self, tokens, frames):
        t2, h2, w2 = self.info.grid(frames)
        n = t2 * h2 * w2
        if tokens.shape[1] != n:
            raise RuntimeError(f"encoder returned {tokens.shape[1]} tokens, expected {n} = {t2}x{h2}x{w2}")
        return tokens.reshape(1, t2, h2, w2, tokens.shape[-1])


# --------------------------------------------------------------------------
# the trunk cache
# --------------------------------------------------------------------------


def _slug(text):
    """Filesystem-safe token for a cache directory name."""
    return "".join(ch if ch.isalnum() or ch in "-._" else "_" for ch in str(text))


def checkpoint_digest(path):
    """Content identity, including replacement of a checkpoint at the same path."""
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def model_digest(model):
    """Content identity for hub models without a single local checkpoint file."""
    digest = hashlib.sha256()
    for name, tensor in sorted(model.state_dict().items()):
        digest.update(json.dumps([name, str(tensor.dtype), list(tensor.shape)]).encode())
        raw = tensor.detach().cpu().contiguous().reshape(-1).view(torch.uint8)
        digest.update(raw.numpy().tobytes())
    return digest.hexdigest()


def encoder_fingerprint(meta):
    """Hash full encoder identity and precision, not just architecture."""
    identity = {"encoder": meta.get("encoder", {}),
                "dtype": meta.get("dtype"),
                "checkpoint_sha256": meta.get("checkpoint_sha256")}
    return hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()[:24]


def cache_key(meta, unfreeze):
    """What makes two trunk outputs interchangeable: the sampled pixels (fps,
    size, crop, match), the window layout, the encoder that produced them, and
    the split. A different crop, match or checkpoint reads different pixels or a
    different network, so it must never reuse another configuration's cache."""
    return (f"ft-fps{meta['fps']:g}-size{meta['size']}-crop{_slug(meta['crop'])}"
            f"-match{_slug(meta['match'])}-f{meta['frames']}-s{meta['stride']}"
            f"-{encoder_fingerprint(meta)}-k{unfreeze}")


class TrunkCache:
    """One fp16 .npy per (case, variant, window). Filled lazily, so a run that
    only ever samples some windows only ever pays for those, and a rerun with
    the same key pays for none."""

    def __init__(self, root, key):
        self.root = root
        self.key = key
        self.hits = self.misses = 0

    def path(self, case_id, variant, w):
        if not self.root:
            return None
        return os.path.join(self.root, case_id, self.key, variant, f"w{w:05d}.npy")

    def load(self, case_id, variant, w, device):
        p = self.path(case_id, variant, w)
        if p is None or not os.path.exists(p):
            return None
        self.hits += 1
        return torch.from_numpy(np.load(p)).to(device)

    def store(self, case_id, variant, w, h):
        self.misses += 1
        p = self.path(case_id, variant, w)
        if p is None:
            return
        os.makedirs(os.path.dirname(p), exist_ok=True)
        tmp = p + ".tmp.npy"
        np.save(tmp, h.detach().to(torch.float16).cpu().numpy())
        os.replace(tmp, p)


# --------------------------------------------------------------------------
# clips
# --------------------------------------------------------------------------


class Clip:
    """A case's sampled frames and window layout, plus the label plumbing.

    A 60 s clip at 15 fps / 384 px is 400 MB of uint8 frames, and a fold keeps
    every training clip in a list, so frames are not held by the Clip: they are
    decoded on demand into a single-slot cache shared by all clips. Training and
    statistics visit one clip at a time, so each pass decodes each clip once.
    """

    _slot = (None, None)   # (clip id, frames): the only decoded clip in memory

    def __init__(self, case, meta, match_dir, flip):
        self.case = case
        self.flip = flip
        self.variant = "flip" if flip else "orig"
        self._src = match_clip(case["clip"], meta["match"], match_dir)
        self._meta = meta
        n_frames = len(self.frames)
        self.starts = windows(n_frames, meta["frames"], meta["stride"])
        self.valid = [min(meta["frames"], n_frames - s) for s in self.starts]
        self.n_windows = len(self.starts)
        self.length = meta["frames"]

    @property
    def frames(self):
        pinned = self.__dict__.get("_pinned")
        if pinned is not None:
            return pinned
        key, frames = Clip._slot
        if key != id(self):
            Clip._slot = (None, None)   # release the previous clip before decoding the next
            m = self._meta
            frames = sample_frames(self._src, m["fps"], m["size"], m["crop"], flip=self.flip)
            Clip._slot = (id(self), frames)
        return frames

    @frames.setter
    def frames(self, value):
        """Explicitly supplied frames (tests, in-memory callers) stay pinned."""
        self._pinned = value

    def window(self, w):
        frames, _ = pad_window(self.frames, self.starts[w], self.length)
        return frames

    def window_seconds(self, w, fps, tubelet):
        return self.starts[w] / fps, (self.starts[w] + self.valid[w]) / fps

    def timestep_seconds(self, w, t, fps, tubelet):
        return (self.starts[w] + t * tubelet + (tubelet - 1) / 2) / fps

    def window_labels(self, fps, tubelet):
        """Per-window targets, or None for a clip-level (MIL) label. Delegates
        to `train.Example.window_labels` so the two paths cannot drift."""
        return tr.Example(self.case, self).window_labels(fps, tubelet)


def load_clip(case, meta, cache_root, flip):
    """Indirection so tests can supply frames without ffmpeg."""
    match_dir = os.path.join(cache_root or ".", "_matched", case["id"])
    return Clip(case, meta, match_dir, flip)


# --------------------------------------------------------------------------
# sampling windows for a training step
# --------------------------------------------------------------------------


def sample_windows(clip, fps, tubelet, max_windows, rng):
    """A random subset of a clip's windows for one epoch.

    When the case carries start/end seconds, half the budget is reserved for
    windows that overlap the labeled interval — otherwise a 60 s clip with a
    2 s touch trains almost entirely on its own negatives.
    """
    n = clip.n_windows
    if max_windows <= 0 or max_windows >= n:
        return list(range(n))
    labels = clip.window_labels(fps, tubelet)
    if labels is None or labels.sum() == 0:
        return sorted(rng.choice(n, size=max_windows, replace=False).tolist())
    pos = np.flatnonzero(labels)
    neg = np.flatnonzero(labels == 0)
    want_pos = min(len(pos), max(1, max_windows // 2))
    want_neg = min(len(neg), max_windows - want_pos)
    want_pos = min(len(pos), max_windows - want_neg)
    take = np.concatenate([rng.choice(pos, size=want_pos, replace=False),
                           rng.choice(neg, size=want_neg, replace=False)])
    return sorted(int(i) for i in take)


# --------------------------------------------------------------------------
# loss
# --------------------------------------------------------------------------


def window_bce(win_logits, targets, pos_weight):
    """Per-window BCE, the `clip_loss` branch for cases with an interval."""
    weight = torch.where(targets > 0, torch.full_like(targets, pos_weight), torch.ones_like(targets))
    return F.binary_cross_entropy_with_logits(win_logits, targets, weight=weight)


def mil_clip_logit(win_logits, mil_temp):
    """The soft max over a clip's windows used when only a clip label exists."""
    return torch.logsumexp(win_logits * mil_temp, dim=0) / mil_temp


def mil_loss(clip_logit, contact, pos_weight):
    target = torch.tensor(1.0 if contact else 0.0, device=clip_logit.device, dtype=clip_logit.dtype)
    weight = pos_weight if contact else 1.0
    return F.binary_cross_entropy_with_logits(clip_logit, target) * weight


def mil_backward_coeffs(win_logits, contact, hp):
    """(loss, per-window dL/dlogit) for the MIL branch, computed from detached
    logits.

    The exactness that matters: d/dlogit_i of logsumexp(t * logit)/t is
    softmax(t * logit)_i, so a first no-grad pass over the clip's windows is
    enough to hand every window a scalar coefficient, and the second pass can
    then backward one window at a time with O(1) activation memory instead of
    holding the whole clip's graph. This is what lets a 12 GB card do MIL over
    a clip with dozens of windows.
    """
    logits = win_logits.detach().float()
    clip_logit = mil_clip_logit(logits, hp["mil_temp"])
    target = 1.0 if contact else 0.0
    weight = hp["pos_weight"] if contact else 1.0
    loss = mil_loss(clip_logit, contact, hp["pos_weight"])
    dclip = (torch.sigmoid(clip_logit) - target) * weight
    coeffs = torch.softmax(logits * hp["mil_temp"], dim=0) * dclip
    return float(loss), coeffs


# --------------------------------------------------------------------------
# training
# --------------------------------------------------------------------------


class Trainer:
    def __init__(self, split_enc, head_kind, meta, hp, device, cache, log=sys.stderr):
        self.enc = split_enc
        self.meta = meta
        self.hp = hp
        self.device = device
        self.cache = cache
        self.log = log
        self.fps = meta["fps"]
        self.tubelet = meta["encoder"]["tubelet"]
        self.head_kind = head_kind
        self.head = build_head(head_kind, meta["encoder"]["hidden"]).to(device)
        # Autocast is what keeps the trainable blocks in bf16 while their master
        # weights stay fp32; it only applies on cuda and only for a half dtype.
        self.amp_dtype = split_enc.dtype
        self.autocast = device == "cuda" and self.amp_dtype in (torch.bfloat16, torch.float16)

    # -- one window -------------------------------------------------------

    def hidden(self, clip, w):
        """The trunk output for one window, from the cache when possible."""
        h = self.cache.load(clip.case["id"], clip.variant, w, self.device)
        if h is None:
            h, kwargs = self.enc.trunk(clip.window(w))
            self.cache.store(clip.case["id"], clip.variant, w, h[0])
            self.block_kwargs = kwargs
            return h.to(self.device), kwargs
        return h.unsqueeze(0), self.block_kwargs

    def prime_kwargs(self, clip):
        """The per-block arguments depend only on the input shape, so one
        uncached window at the start of a run fixes them for all of them."""
        _h, kwargs = self.enc.trunk(clip.window(0))
        self.block_kwargs = kwargs

    def window_logits(self, clip, w):
        h, kwargs = self.hidden(clip, w)
        with torch.autocast(device_type=self.device, dtype=self.amp_dtype, enabled=self.autocast):
            tokens = self.enc.tail(h, kwargs)
        tokens = self.enc.grid(tokens.float(), self.meta["frames"])
        win, step = self.head(tokens)
        return win, step

    # -- statistics -------------------------------------------------------

    @torch.no_grad()
    def fit_stats(self, clips, max_windows, rng, sample_tokens=20_000):
        """Per-dimension mean/std of the encoder's output before any training,
        so the head does not spend its first epochs rediscovering the scale.
        They stay fixed afterwards: the tail drifts, but a moving target under
        a head that is training at the same time is not worth the instability.

        Heads that fit a label-free projection (the attentive probe) get their
        basis from the same pass, exactly as `train.fit` does.
        """
        dim = self.meta["encoder"]["hidden"]
        acc_sum = acc_sq = None
        n = 0
        samples = []
        per_clip = max(1, sample_tokens // max(1, len(clips)))
        for clip in clips:
            idx = sample_windows(clip, self.fps, self.tubelet, max_windows, rng)
            for w in idx:
                h, kwargs = self.hidden(clip, w)
                tokens = self.enc.tail(h, kwargs).float().reshape(-1, dim)
                s, q = tokens.sum(0), (tokens * tokens).sum(0)
                acc_sum = s if acc_sum is None else acc_sum + s
                acc_sq = q if acc_sq is None else acc_sq + q
                n += tokens.shape[0]
                if len(samples) * per_clip < sample_tokens:
                    pick = torch.as_tensor(
                        rng.choice(tokens.shape[0], size=min(per_clip, tokens.shape[0]), replace=False),
                        device=tokens.device)
                    samples.append(tokens[pick].cpu())
        mu = acc_sum / n
        sigma = (acc_sq / n - mu * mu).clamp_min(0).sqrt()
        self.head.set_stats(mu, sigma)
        if hasattr(self.head, "fit_projection"):
            sample = torch.cat(samples).to(self.device)
            self.head.fit_projection((sample - mu) / sigma.clamp_min(1e-6))

    # -- the loop ---------------------------------------------------------

    def train(self, clips, epochs, seed):
        rng = np.random.default_rng(seed)
        torch.manual_seed(seed)
        hp = self.hp
        groups = [{"params": list(self.head.parameters()), "lr": hp["lr"]}]
        if self.enc.trainable:
            groups.append({"params": self.enc.trainable, "lr": hp["lr_encoder"]})
        opt = torch.optim.AdamW(groups, weight_decay=hp["weight_decay"])
        self.head.train()
        opt.zero_grad(set_to_none=True)
        pending = 0
        for epoch in range(epochs):
            order = rng.permutation(len(clips))
            total, seen = 0.0, 0
            t0 = time.time()
            for i in order:
                clip = clips[i]
                idx = sample_windows(clip, self.fps, self.tubelet, hp["max_windows_per_clip"], rng)
                loss = self.clip_step(clip, idx)
                total += loss
                seen += 1
                pending += len(idx)
                if pending >= hp["accum"]:
                    self.step(opt)
                    pending = 0
            if pending:
                self.step(opt)
                pending = 0
            print(f"  epoch {epoch + 1}/{epochs} loss {total / max(1, seen):.4f} "
                  f"({time.time() - t0:.0f}s, cache {self.cache.hits} hit / {self.cache.misses} miss)",
                  file=self.log)
        self.head.eval()
        return self.head

    def step(self, opt):
        params = list(self.head.parameters()) + self.enc.trainable
        torch.nn.utils.clip_grad_norm_(params, 1.0)
        opt.step()
        opt.zero_grad(set_to_none=True)

    def clip_step(self, clip, idx):
        """Forward+backward one clip's sampled windows, one window at a time.

        The per-window BCE branch is separable, so each window is backwarded as
        it is computed. The MIL branch is not, so it takes a no-grad pass to get
        the softmax coefficients and a second pass to apply them.
        """
        labels = clip.window_labels(self.fps, self.tubelet)
        hp = self.hp
        if labels is not None:
            total = 0.0
            for w in idx:
                win, _ = self.window_logits(clip, w)
                target = torch.tensor([labels[w]], device=self.device)
                loss = window_bce(win, target, hp["pos_weight"]) / len(idx)
                loss.backward()
                total += loss.detach().item()
            return total
        with torch.no_grad():
            logits = torch.cat([self.window_logits(clip, w)[0].detach() for w in idx])
        loss, coeffs = mil_backward_coeffs(logits, clip.case["contact"], hp)
        for j, w in enumerate(idx):
            c = float(coeffs[j])
            if c == 0.0:
                continue
            win, _ = self.window_logits(clip, w)
            (win.squeeze(0) * c).backward()
        return loss

    # -- scoring ----------------------------------------------------------

    @torch.no_grad()
    def score(self, clip):
        """(clip_prob, (start_s, end_s)): the clip score is the max window
        probability, and the span comes from the best window's timestep logits
        the way `train.score_clip` derives it."""
        self.head.eval()
        best_p, best_w, best_step = -1.0, 0, None
        for w in range(clip.n_windows):
            win, step = self.window_logits(clip, w)
            p = float(torch.sigmoid(win[0]))
            if p > best_p:
                best_p, best_w = p, w
                best_step = None if step is None else torch.sigmoid(step[0]).float().cpu().numpy()
        if best_step is None:
            loc = clip.window_seconds(best_w, self.fps, self.tubelet)
        else:
            t = int(best_step.argmax())
            lo = hi = t
            while lo > 0 and best_step[lo - 1] >= best_step[t] / 2:
                lo -= 1
            while hi + 1 < len(best_step) and best_step[hi + 1] >= best_step[t] / 2:
                hi += 1
            loc = (clip.timestep_seconds(best_w, lo, self.fps, self.tubelet),
                   clip.timestep_seconds(best_w, hi, self.fps, self.tubelet))
        return best_p, loc


# --------------------------------------------------------------------------
# splits
# --------------------------------------------------------------------------

HOLDOUT_TAG = "holdout"


def split_cases(cases, split):
    """[(train_cases, eval_cases)], one entry per fold.

    A holdout tag holds out its whole recording group: case ids are not the
    unit of independence, recordings are, so a tagged clip drags its untagged
    siblings into the evaluation side instead of leaking them into training.
    """
    if split == "holdout":
        held_groups = {tr.group_of(c) for c in cases if HOLDOUT_TAG in c["tags"]}
        if not held_groups:
            raise SystemExit(f"-split holdout: no case carries the {HOLDOUT_TAG!r} tag")
        held = [c for c in cases if tr.group_of(c) in held_groups]
        rest = [c for c in cases if tr.group_of(c) not in held_groups]
        if not rest:
            raise SystemExit(f"-split holdout: every case carries the {HOLDOUT_TAG!r} tag")
        return [(rest, held)]
    if split == "loo":
        return [([c for c in cases if c["id"] != h["id"]], [h]) for h in cases]
    raise ValueError(f"unknown -split {split!r}")


# --------------------------------------------------------------------------
# the run
# --------------------------------------------------------------------------


def run(cases, meta, split_enc, head_kind, hp, device, cache_root, split, epochs, seed,
        flip_augment, stats_windows, log=sys.stderr, load=load_clip):
    """Fine-tune and evaluate. Returns (per_case, locs, evaluated_cases) in the
    shape `train.summarize` expects."""
    folds = split_cases(cases, split)
    for train_cases, eval_cases in folds:
        tr.assert_split_disjoint(train_cases, eval_cases)
    if split == "loo":
        print(f"warning: -split loo retrains the encoder tail {len(folds)} times; "
              f"at one fold per case this is {len(folds)}x the cost of -split holdout",
              file=log)
    # Fix every trainable weight before anything is built: the head is created
    # in the fold loop, so `Trainer.train` re-seeds too late to pin its initial
    # weights. Without this a run depends on the process's RNG history and two
    # K arms cannot start from the same head.
    torch.manual_seed(seed)
    cache = TrunkCache(cache_root, cache_key(meta, split_enc.unfreeze))
    per_case, locs, evaluated = {}, {}, []
    # Only the tail is ever updated, so a fold restarts from a copy of the tail
    # alone (K blocks in fp32) rather than the whole encoder.
    state0 = {k: v.detach().to("cpu", copy=True) for k, v in split_enc.tail_state().items()}
    for fold, (train_cases, eval_cases) in enumerate(folds):
        if fold:
            split_enc.load_tail_state(state0)
        clips = []
        for c in train_cases:
            clips.append(load(c, meta, cache_root, False))
            if flip_augment:
                clips.append(load(c, meta, cache_root, True))
        trainer = Trainer(split_enc, head_kind, meta, hp, device, cache, log=log)
        trainer.prime_kwargs(clips[0])
        rng = np.random.default_rng(seed)
        trainer.fit_stats(clips, stats_windows, rng)
        print(f"fold {fold + 1}/{len(folds)}: {len(clips)} training clips, "
              f"eval {[c['id'] for c in eval_cases]}", file=log)
        trainer.train(clips, epochs, seed)
        for c in eval_cases:
            clip = load(c, meta, cache_root, False)
            p, loc = trainer.score(clip)
            per_case[c["id"]] = [p]
            locs[c["id"]] = loc
            evaluated.append(c)
            print(f"  {c['id']:<32} p={p:.3f}  label={'contact' if c['contact'] else 'none'}", file=log)
    return per_case, locs, evaluated, trainer


def save_encoder(path, split_enc, head, head_kind, meta, hp, threshold):
    """The unfrozen block weights and the head: everything a fine-tuned run
    changed. The frozen trunk is whatever `-model` pointed at."""
    tail = {k: v.detach().cpu() for k, v in split_enc.tail_state().items()}
    os.makedirs(os.path.dirname(os.path.abspath(path)) or ".", exist_ok=True)
    torch.save({"tail": tail, "state": head.state_dict(), "head": head_kind, "meta": meta,
                "hp": hp, "threshold": threshold, "unfreeze": split_enc.unfreeze,
                "split": split_enc.split, "depth": split_enc.depth}, path)
