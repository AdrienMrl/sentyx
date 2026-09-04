"""Leave-one-case-out training of a head on cached features, and the metrics
the bench cares about: contact missed, contact phantom, and a rank-based AUC
that stays meaningful when there are five positives.

Windows are the training unit. A clip's label is the max over its windows
(multiple-instance learning), unless the label carries start/end seconds, in
which case windows overlapping that interval are positive and the rest are
negative, which is a far stronger signal and what staged clips should carry.

Cost model: the token cache for a long clip is hundreds of MB, so every clip's
tokens go to the device once (half precision), each fold standardizes them
with that fold's training statistics and reduces them to the head's
representation once, and the optimizer steps only touch that representation.
"""

import os
import sys
import time

import numpy as np
import torch
import torch.nn.functional as F

from .heads import build_head
from .store import ClipFeatures, feature_path


class Example:
    def __init__(self, case, feats, flipped=False):
        self.case, self.feats, self.flipped = case, feats, flipped
        self.dev_tokens = None   # (W, T', H', W', D) float16 on the training device
        self.rep = None          # the head's prepared representation for the current fold

    def to_device(self, device):
        if self.dev_tokens is None:
            self.dev_tokens = torch.from_numpy(self.feats.tokens).half().to(device)
        return self.dev_tokens

    def window_labels(self, fps, tubelet):
        """Per-window targets, or None when only a clip-level label exists."""
        c = self.case
        if not c["contact"] or c["start"] is None or c["end"] is None:
            return None
        labels = np.zeros(self.feats.n_windows, np.float32)
        for w in range(self.feats.n_windows):
            s, e = self.feats.window_seconds(w, fps, tubelet)
            if s < c["end"] and e > c["start"]:
                labels[w] = 1.0
        return labels


def load_examples(cache_dir, cases, use_flips):
    examples = []
    for c in cases:
        p = feature_path(cache_dir, c["id"], c["clip"])
        if not os.path.exists(p):
            raise FileNotFoundError(f"{p}: run `encode` first")
        examples.append(Example(c, ClipFeatures(p)))
        fp = feature_path(cache_dir, c["id"], c["clip"], flip=True)
        if use_flips and os.path.exists(fp):
            examples.append(Example(c, ClipFeatures(fp), flipped=True))
    return examples


@torch.no_grad()
def token_stats(train, device, max_windows=64, sample_tokens=20_000):
    """Per-dimension mean/std over a subsample of the training windows, plus a
    random sample of raw tokens (CPU) for heads that fit a projection."""
    rng = np.random.default_rng(0)
    per = max(1, max_windows // len(train))
    acc_sum = acc_sq = None
    n = 0
    samples = []
    per_sample = max(1, sample_tokens // len(train))
    for ex in train:
        toks = ex.to_device(device)
        idx = rng.choice(toks.shape[0], size=min(per, toks.shape[0]), replace=False)
        flat = toks[torch.as_tensor(idx, device=device)].float().reshape(-1, toks.shape[-1])
        s, q = flat.sum(0), (flat * flat).sum(0)
        acc_sum = s if acc_sum is None else acc_sum + s
        acc_sq = q if acc_sq is None else acc_sq + q
        n += flat.shape[0]
        pick = torch.as_tensor(rng.choice(flat.shape[0], size=min(per_sample, flat.shape[0]), replace=False), device=device)
        samples.append(flat[pick].cpu())
    mu = acc_sum / n
    var = (acc_sq / n - mu * mu).clamp_min(0)
    return mu, var.sqrt(), torch.cat(samples)


@torch.no_grad()
def prepare_examples(head, examples, device):
    for ex in examples:
        ex.rep = head.prepare(head.standardize(ex.to_device(device)))


def clip_loss(head, ex, fps, tubelet, device, rng, hp):
    rep = head.augment(ex.rep, rng, hp)
    win_logits, _ = head.forward_rep(rep)
    wl = ex.window_labels(fps, tubelet)
    if wl is not None:
        target = torch.from_numpy(wl).to(device)
        weight = torch.where(target > 0, torch.tensor(hp["pos_weight"], device=device), torch.tensor(1.0, device=device))
        return F.binary_cross_entropy_with_logits(win_logits, target, weight=weight)
    # Multiple-instance: the clip is positive iff its most suspicious window is.
    clip_logit = torch.logsumexp(win_logits * hp["mil_temp"], dim=0) / hp["mil_temp"]
    target = torch.tensor(1.0 if ex.case["contact"] else 0.0, device=device)
    weight = hp["pos_weight"] if ex.case["contact"] else 1.0
    return F.binary_cross_entropy_with_logits(clip_logit, target) * weight


def fit(head_kind, dim, train, fps, tubelet, device, hp, seed, stats=None):
    """Train one head on `train`. Standardization statistics come from the
    training examples unless `stats` is given (so seeds of one fold share them
    and the reduction in `prepare` is done once per fold, not per seed)."""
    torch.manual_seed(seed)
    rng = np.random.default_rng(seed)
    head = build_head(head_kind, dim).to(device)
    mu, sigma, sample = stats if stats is not None else token_stats(train, device)
    head.set_stats(mu, sigma)
    if hasattr(head, "fit_projection"):
        head.fit_projection((sample.to(device) - mu) / sigma.clamp_min(1e-6))
    if stats is None or any(ex.rep is None for ex in train):
        prepare_examples(head, train, device)
    opt = torch.optim.AdamW(head.parameters(), lr=hp["lr"], weight_decay=hp["weight_decay"])
    steps = hp["epochs"] * len(train)
    sched = torch.optim.lr_scheduler.OneCycleLR(opt, max_lr=hp["lr"], total_steps=max(1, steps))
    head.train()
    order = np.arange(len(train))
    for _ in range(hp["epochs"]):
        rng.shuffle(order)
        for i in order:
            loss = clip_loss(head, train[i], fps, tubelet, device, rng, hp)
            opt.zero_grad()
            loss.backward()
            torch.nn.utils.clip_grad_norm_(head.parameters(), 1.0)
            opt.step()
            sched.step()
    head.eval()
    return head


@torch.no_grad()
def score_clip(head, feats, fps, tubelet, device):
    """Returns (clip_prob, (start_s, end_s)) for one clip's features."""
    tokens = torch.from_numpy(feats.tokens).to(device)
    win_logits, step_logits = head(tokens)
    probs = torch.sigmoid(win_logits).float().cpu().numpy()
    w = int(probs.argmax())
    if step_logits is not None:
        sp = torch.sigmoid(step_logits[w]).float().cpu().numpy()
        t = int(sp.argmax())
        # The run of consecutive timesteps above half the peak bounds the event.
        lo = t
        while lo > 0 and sp[lo - 1] >= sp[t] / 2:
            lo -= 1
        hi = t
        while hi + 1 < len(sp) and sp[hi + 1] >= sp[t] / 2:
            hi += 1
        loc = (feats.timestep_seconds(w, lo, fps, tubelet), feats.timestep_seconds(w, hi, fps, tubelet))
    else:
        loc = feats.window_seconds(w, fps, tubelet)
    return float(probs.max()), loc


def auc(scores, labels):
    """Rank AUC (Mann-Whitney), None when one class is absent."""
    pos = [s for s, l in zip(scores, labels) if l]
    neg = [s for s, l in zip(scores, labels) if not l]
    if not pos or not neg:
        return None
    wins = sum(1.0 if p > n else 0.5 if p == n else 0.0 for p in pos for n in neg)
    return wins / (len(pos) * len(neg))


def cross_validate(cache_dir, cases, meta, head_kind, hp, seeds, device, use_flips, log=sys.stderr):
    """Leave-one-case-out. Flipped copies of the held-out case never enter its
    training fold. Returns per-case out-of-fold scores averaged over seeds."""
    fps, tubelet, dim = meta["fps"], meta["encoder"]["tubelet"], meta["encoder"]["hidden"]
    examples = load_examples(cache_dir, cases, use_flips)
    per_case = {c["id"]: [] for c in cases}
    locs = {}
    t0 = time.time()
    for ex in examples:
        ex.to_device(device)
    print(f"  {len(examples)} examples on {device} ({time.time() - t0:.0f}s)", file=log)
    for held in cases:
        train = [e for e in examples if e.case["id"] != held["id"]]
        test = next(e for e in examples if e.case["id"] == held["id"] and not e.flipped)
        stats = token_stats(train, device)
        for ex in train:
            ex.rep = None
        for seed in seeds:
            head = fit(head_kind, dim, train, fps, tubelet, device, hp, seed, stats=stats)
            p, loc = score_clip(head, test.feats, fps, tubelet, device)
            per_case[held["id"]].append(p)
            locs[held["id"]] = loc
        print(f"  fold {held['id']:<32} mean p={np.mean(per_case[held['id']]):.3f}  "
              f"label={'contact' if held['contact'] else 'none'}  ({time.time() - t0:.0f}s)", file=log)
    return per_case, locs


def summarize(cases, per_case, locs, threshold):
    ids = [c["id"] for c in cases]
    labels = [c["contact"] for c in cases]
    mean_scores = [float(np.mean(per_case[i])) for i in ids]
    rows = []
    missed = phantom = 0
    for c, s in zip(cases, mean_scores):
        pred = s >= threshold
        if c["contact"] and not pred:
            missed += 1
        if not c["contact"] and pred:
            phantom += 1
        spread = float(np.std(per_case[c["id"]]))
        row = {"case": c["id"], "label": c["contact"], "score": round(s, 4), "seed_std": round(spread, 4),
               "pred": bool(pred), "loc_seconds": [round(x, 1) for x in locs[c["id"]]]}
        if c["contact"] and c["start"] is not None:
            # Did the held-out head point at the labeled touch (within 1 s)? A
            # head that scores a clip on style alone tends to peak anywhere.
            lo, hi = locs[c["id"]]
            row["loc_hit"] = bool(lo <= c["end"] + 1.0 and hi >= c["start"] - 1.0)
        rows.append(row)
    # The threshold that catches every positive, and what it costs in phantoms.
    pos_scores = [s for s, l in zip(mean_scores, labels) if l]
    zero_miss_thr = min(pos_scores) if pos_scores else None
    zero_miss_phantoms = sum(1 for s, l in zip(mean_scores, labels) if not l and zero_miss_thr is not None and s >= zero_miss_thr)
    hits = [r["loc_hit"] for r in rows if "loc_hit" in r]
    return {
        "cases": rows,
        "threshold": threshold,
        "loc_hits": f"{sum(hits)}/{len(hits)}" if hits else None,
        "contact_missed": missed,
        "contact_phantom": phantom,
        "positives": sum(labels),
        "negatives": len(labels) - sum(labels),
        "auc": auc(mean_scores, labels),
        "zero_miss_threshold": zero_miss_thr,
        "phantoms_at_zero_miss": zero_miss_phantoms,
    }


def fit_final(cache_dir, cases, meta, head_kind, hp, seed, device, use_flips):
    """Train on every case for the analyzer to ship."""
    fps, tubelet, dim = meta["fps"], meta["encoder"]["tubelet"], meta["encoder"]["hidden"]
    examples = load_examples(cache_dir, cases, use_flips)
    return fit(head_kind, dim, examples, fps, tubelet, device, hp, seed)


def save_head(path, head, head_kind, meta, hp, threshold):
    torch.save({"state": head.state_dict(), "head": head_kind, "meta": meta, "hp": hp,
                "threshold": threshold}, path)


def load_head(path, device):
    ck = torch.load(path, map_location=device, weights_only=False)
    head = build_head(ck["head"], ck["meta"]["encoder"]["hidden"]).to(device)
    head.load_state_dict(ck["state"])
    head.eval()
    return head, ck
