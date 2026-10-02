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

import math
import os
import sys
import time

import numpy as np
import torch
import torch.nn.functional as F

from .heads import build_head
from .store import ClipFeatures, feature_path

TEMPORAL_CROP_POLICY = "negative-only-preserve-positive-support-v1"


class Example:
    def __init__(self, case, feats, flipped=False):
        self.case, self.feats, self.flipped = case, feats, flipped
        self.dev_tokens = None   # (W, T', H', W', D) float16 on the training device
        self.raw = None          # label-free reduction of the grid (heads with prepare_raw)
        self.stat_sample = None  # (n, D) float16 CPU subsample of tokens for standardization statistics
        self.rep = None          # the head's prepared representation for the current fold

    def to_device(self, device):
        if self.dev_tokens is None:
            self.dev_tokens = torch.from_numpy(self.feats.tokens).half().to(device)
        return self.dev_tokens

    def tokens_transient(self, device):
        """The grid on the device without keeping it there."""
        if self.dev_tokens is not None:
            return self.dev_tokens
        return torch.from_numpy(self.feats.tokens).half().to(device)

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


def load_examples(cache_dir, cases, use_flips, lazy=False):
    examples = []
    for c in cases:
        p = feature_path(cache_dir, c["id"], c["clip"])
        if not os.path.exists(p):
            raise FileNotFoundError(f"{p}: run `encode` first")
        examples.append(Example(c, ClipFeatures(p, lazy=lazy)))
        fp = feature_path(cache_dir, c["id"], c["clip"], flip=True)
        if use_flips and os.path.exists(fp):
            examples.append(Example(c, ClipFeatures(fp, lazy=lazy), flipped=True))
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
        n_w = ex.feats.n_windows
        idx = np.sort(rng.choice(n_w, size=min(per, n_w), replace=False))
        if ex.stat_sample is not None:
            flat = ex.stat_sample.to(device).float()                    # staged subsample; grid is not in memory
        elif ex.dev_tokens is not None:
            toks = ex.dev_tokens[torch.as_tensor(idx, device=device)]
            flat = toks.float().reshape(-1, toks.shape[-1])
        else:
            toks = torch.from_numpy(ex.feats.tokens[idx]).to(device)   # only the sampled windows cross the bus
            flat = toks.float().reshape(-1, toks.shape[-1])
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
        if head.prepare_raw is not None:
            if ex.raw is None:
                ex.raw = head.prepare_raw(ex.tokens_transient(device))
            ex.rep = head.standardize_rep(ex.raw)
        else:
            ex.rep = head.prepare(head.standardize(ex.to_device(device)))


@torch.no_grad()
def stage_examples(head_kind, dim, examples, device):
    """Put on the device what training needs: the reduced grid for heads with
    `prepare_raw` (megabytes), the full grid otherwise (gigabytes)."""
    head = build_head(head_kind, dim)
    rng = np.random.default_rng(0)
    for ex in examples:
        if head.prepare_raw is not None:
            if ex.raw is not None:
                continue
            grid = torch.from_numpy(ex.feats.tokens).half().to(device)   # loaded once, reduced, dropped
            ex.raw = head.prepare_raw(grid)
            flat = grid.reshape(-1, grid.shape[-1])
            pick = torch.as_tensor(rng.choice(flat.shape[0], size=min(4096, flat.shape[0]), replace=False), device=device)
            ex.stat_sample = flat[pick].cpu()
            del grid, flat
        else:
            ex.to_device(device)


def clip_loss(head, ex, fps, tubelet, device, rng, hp):
    # Cropping positive windows can remove the only contact while retaining
    # their original positive target. The augmentation API does not return
    # its temporal offset, so it cannot safely relabel timed positives (or
    # guarantee that a weakly labeled positive retains any contact). Preserve
    # their temporal support; other augmentations and negative crops remain.
    augmentation_hp = dict(hp, time_crop=0.0) if ex.case["contact"] else hp
    rep = head.augment(ex.rep, rng, augmentation_hp)
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
    steps = hp["epochs"] * ((len(train) + hp["batch"] - 1) // hp["batch"])
    sched = torch.optim.lr_scheduler.OneCycleLR(opt, max_lr=hp["lr"], total_steps=max(1, steps))
    head.train()
    order = np.arange(len(train))
    batch = hp["batch"]
    for _ in range(hp["epochs"]):
        rng.shuffle(order)
        # Clips have different window counts, so the loss is per clip; a step
        # averages `batch` of them. This is what makes a fold launch-bound
        # rather than step-bound on a GPU.
        for b in range(0, len(order), batch):
            opt.zero_grad()
            idx = order[b : b + batch]
            for i in idx:
                (clip_loss(head, train[i], fps, tubelet, device, rng, hp) / len(idx)).backward()
            torch.nn.utils.clip_grad_norm_(head.parameters(), 1.0)
            opt.step()
            sched.step()
    head.eval()
    return head


@torch.no_grad()
def score_clip(head, feats, fps, tubelet, device, aggregate="max"):
    """Returns (clip_prob, (start_s, end_s)) for one clip's features. `aggregate`
    is how window probabilities become the clip score: max (default), mean, or
    top2 (mean of the two highest windows, a sparse-event rule)."""
    tokens = torch.from_numpy(feats.tokens).to(device)
    win_logits, step_logits = head(tokens)
    probs = torch.sigmoid(win_logits).float().cpu().numpy()
    w = int(probs.argmax())
    if aggregate == "mean":
        clip_prob = float(probs.mean())
    elif aggregate == "top2" and len(probs) >= 2:
        clip_prob = float(np.sort(probs)[-2:].mean())
    else:
        clip_prob = float(probs.max())
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
    return clip_prob, loc


def auc(scores, labels):
    """Rank AUC (Mann-Whitney), None when one class is absent."""
    pos = [s for s, l in zip(scores, labels) if l]
    neg = [s for s, l in zip(scores, labels) if not l]
    if not pos or not neg:
        return None
    wins = sum(1.0 if p > n else 0.5 if p == n else 0.0 for p in pos for n in neg)
    return wins / (len(pos) * len(neg))


def assert_split_disjoint(train_cases, held_cases):
    """Reject known recording overlap; missing provenance is not certified safe."""
    def identities(cases):
        found = set()
        for case in cases:
            found.add(("case", case["id"]))
            for tag in case.get("tags", []):
                if tag.startswith(("real-group:", "group:")):
                    found.add(("group", tag))
        return found
    overlap = identities(train_cases) & identities(held_cases)
    if overlap:
        raise ValueError("training/evaluation recording overlap: " + repr(sorted(overlap)) +
                         "; keep all related cases in a group-held-out split")


def cross_validate(cache_dir, cases, meta, head_kind, hp, seeds, device, use_flips, log=sys.stderr,
                   progress=None):
    """Leave-one-case-out. Flipped copies of the held-out case never enter its
    training fold. Returns per-case out-of-fold scores averaged over seeds."""
    for held in cases:
        assert_split_disjoint([c for c in cases if c["id"] != held["id"]], [held])
    fps, tubelet, dim = meta["fps"], meta["encoder"]["tubelet"], meta["encoder"]["hidden"]
    examples = load_examples(cache_dir, cases, use_flips, lazy=build_head(head_kind, dim).prepare_raw is not None)
    per_case = {c["id"]: [] for c in cases}
    locs = {}
    t0 = time.time()
    stage_examples(head_kind, dim, examples, device)
    print(f"  {len(examples)} examples on {device} ({time.time() - t0:.0f}s)", file=log)
    for fold_index, held in enumerate(cases, 1):
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
        if progress:
            progress(fold_index, len(cases), held, float(np.mean(per_case[held["id"]])),
                     time.time() - t0)
    return per_case, locs


def holdout_validate(cache_dir, train_cases, held_cases, meta, head_kind, hp, seeds, device, use_flips,
                     log=sys.stderr, progress=None):
    """Fit once per seed on train_cases and score every held-out case."""
    assert_split_disjoint(train_cases, held_cases)
    fps, tubelet, dim = meta["fps"], meta["encoder"]["tubelet"], meta["encoder"]["hidden"]
    train_examples = load_examples(cache_dir, train_cases, use_flips,
                                   lazy=build_head(head_kind, dim).prepare_raw is not None)
    held_examples = load_examples(cache_dir, held_cases, False,
                                  lazy=build_head(head_kind, dim).prepare_raw is not None)
    all_examples = train_examples + held_examples
    per_case = {c["id"]: [] for c in held_cases}
    locs = {}
    t0 = time.time()
    stage_examples(head_kind, dim, all_examples, device)
    print(f"  {len(train_examples)} train + {len(held_examples)} holdout examples on {device} "
          f"({time.time() - t0:.0f}s)", file=log)
    stats = token_stats(train_examples, device)
    for seed in seeds:
        for ex in train_examples:
            ex.rep = None
        head = fit(head_kind, dim, train_examples, fps, tubelet, device, hp, seed, stats=stats)
        for index, ex in enumerate(held_examples, 1):
            p, loc = score_clip(head, ex.feats, fps, tubelet, device)
            per_case[ex.case["id"]].append(p)
            locs[ex.case["id"]] = loc
            if progress:
                progress(index, len(held_examples), ex.case, float(np.mean(per_case[ex.case["id"]])),
                         time.time() - t0)
    return per_case, locs


def group_of(case):
    """The recording identity a fold must not split. Falls back to the case."""
    for tag in case.get("tags", []):
        if tag.startswith(("real-group:", "group:")):
            return tag
    return "case:" + case["id"]


def grouped_folds(cases, k, seed=0):
    """Partition cases into k folds without splitting a recording group.

    Groups are dealt largest-first to the fold with the fewest positives, then
    the fewest cases, so class balance is as even as the groups allow.
    """
    if k < 2:
        raise ValueError("k must be at least 2")
    groups = {}
    for c in cases:
        groups.setdefault(group_of(c), []).append(c)
    ordered = sorted(groups.items(), key=lambda kv: (-len(kv[1]), kv[0]))
    folds = [[] for _ in range(k)]
    positives = [0] * k
    sizes = [0] * k
    for _, members in ordered:
        i = min(range(k), key=lambda j: (positives[j], sizes[j], j))
        folds[i].extend(members)
        positives[i] += sum(1 for m in members if m["contact"])
        sizes[i] += len(members)
    return folds


def binom_cdf_at_most(x, n, p):
    return sum(math.comb(n, k) * p ** k * (1.0 - p) ** (n - k) for k in range(x + 1))


def upper_rate_bound(errors, n, alpha=0.05):
    """Exact one-sided (1-alpha) Clopper-Pearson upper bound on a rate from
    `errors` failures in `n` independent trials. None when n == 0."""
    if n == 0:
        return None
    if errors >= n:
        return 1.0
    lo, hi = errors / n, 1.0
    for _ in range(80):
        mid = (lo + hi) / 2.0
        if binom_cdf_at_most(errors, n, mid) > alpha:
            lo = mid
        else:
            hi = mid
    return hi


def summarize_groups(cases, per_case, locs, threshold, fold_of):
    """summarize() plus the numbers a locked-operating-point claim needs: the
    error rates, exact one-sided upper bounds, and the per-fold breakdown."""
    s = summarize(cases, per_case, locs, threshold)
    n_pos, n_neg = s["positives"], s["negatives"]
    s["false_positive_rate"] = (s["contact_phantom"] / n_neg) if n_neg else None
    s["false_negative_rate"] = (s["contact_missed"] / n_pos) if n_pos else None
    s["fpr_upper95"] = upper_rate_bound(s["contact_phantom"], n_neg)
    s["fnr_upper95"] = upper_rate_bound(s["contact_missed"], n_pos)
    by_fold = {}
    for c in cases:
        fold = fold_of.get(c["id"])
        d = by_fold.setdefault(fold, {"cases": 0, "positives": 0, "negatives": 0,
                                      "missed": 0, "phantoms": 0})
        pred = float(np.mean(per_case[c["id"]])) >= threshold
        d["cases"] += 1
        if c["contact"]:
            d["positives"] += 1
            d["missed"] += 0 if pred else 1
        else:
            d["negatives"] += 1
            d["phantoms"] += 1 if pred else 0
    s["by_fold"] = by_fold
    # Same-model per-source AUC: pooled out-of-fold scores come from different
    # fold models, so only positive-negative comparisons inside one fold are
    # comparable. Reports eligible pair counts and None when a source lacks a
    # class in every fold.
    within = {}
    for src in sorted({c["source"] for c in cases}):
        wins = pairs = 0
        per_fold = {}
        for c in cases:
            if c["source"] != src:
                continue
            g = per_fold.setdefault(fold_of.get(c["id"]), {"pos": [], "neg": []})
            g["pos" if c["contact"] else "neg"].append(float(np.mean(per_case[c["id"]])))
        for g in per_fold.values():
            for p in g["pos"]:
                for x in g["neg"]:
                    wins += 1.0 if p > x else 0.5 if p == x else 0.0
                    pairs += 1
        within[src] = {"auc": (wins / pairs if pairs else None), "pairs": pairs}
    s["auc_by_source_within_fold"] = within
    return s


def choose_threshold(scores, labels, target_fpr=0.0):
    """Pick a threshold on a calibration set: maximise the number of positives
    caught subject to the empirical false-positive rate not exceeding
    target_fpr. Ties break toward the higher threshold (fewer false alarms).
    Returns a threshold above every score (predict nothing) when no candidate
    satisfies the budget."""
    if not scores:
        return 1.0
    pos = [s for s, l in zip(scores, labels) if l]
    neg = [s for s, l in zip(scores, labels) if not l]
    n_neg = len(neg)
    best_t, best_tp = None, -1
    for t in sorted(set(scores)):
        if n_neg and sum(1 for s in neg if s >= t) > target_fpr * n_neg + 1e-9:
            continue
        tp = sum(1 for s in pos if s >= t)
        if tp > best_tp or (tp == best_tp and best_t is not None and t > best_t):
            best_t, best_tp = t, tp
    if best_t is None:
        return max(scores) + 1e-6
    return best_t


def summarize_locked(cases, per_case, locs, thresholds, fold_of):
    """summarize() with a per-case threshold chosen on a separate calibration
    set, so the outer errors are not an oracle diagnostic."""
    rows = []
    mean = {c["id"]: float(np.mean(per_case[c["id"]])) for c in cases}
    for c in cases:
        s = mean[c["id"]]
        t = thresholds.get(c["id"], 0.5)
        rows.append({"case": c["id"], "label": c["contact"], "score": round(s, 4),
                     "seed_std": round(float(np.std(per_case[c["id"]])), 4),
                     "threshold": round(t, 4), "pred": bool(s >= t),
                     "loc_seconds": [round(x, 1) for x in locs.get(c["id"], (0.0, 0.0))]})
    missed = sum(1 for c, r in zip(cases, rows) if c["contact"] and not r["pred"])
    phantom = sum(1 for c, r in zip(cases, rows) if not c["contact"] and r["pred"])
    n_pos = sum(1 for c in cases if c["contact"])
    n_neg = len(cases) - n_pos
    labels = [c["contact"] for c in cases]
    scores = [mean[c["id"]] for c in cases]
    pos_scores = [s for s, l in zip(scores, labels) if l]
    zero_miss = min(pos_scores) if pos_scores else None
    by_source = {}
    for src in sorted({c["source"] for c in cases}):
        sel = [(mean[c["id"]], c["contact"]) for c in cases if c["source"] == src]
        by_source[src] = {"n": len(sel), "positives": sum(l for _, l in sel),
                          "auc": auc([s for s, _ in sel], [l for _, l in sel])}
    by_fold = {}
    for c, r in zip(cases, rows):
        d = by_fold.setdefault(fold_of.get(c["id"]), {"cases": 0, "positives": 0, "negatives": 0,
                                                      "missed": 0, "phantoms": 0, "threshold": r["threshold"]})
        d["cases"] += 1
        if c["contact"]:
            d["positives"] += 1
            d["missed"] += 0 if r["pred"] else 1
        else:
            d["negatives"] += 1
            d["phantoms"] += 1 if r["pred"] else 0
    return {
        "cases": rows,
        "threshold": None,
        "thresholds_by_fold": {f: d["threshold"] for f, d in by_fold.items()},
        "by_fold": by_fold,
        "auc": auc(scores, labels),
        "auc_by_source": by_source,
        "contact_missed": missed,
        "contact_phantom": phantom,
        "positives": n_pos,
        "negatives": n_neg,
        "false_positive_rate": (phantom / n_neg) if n_neg else None,
        "false_negative_rate": (missed / n_pos) if n_pos else None,
        "fpr_upper95": upper_rate_bound(phantom, n_neg),
        "fnr_upper95": upper_rate_bound(missed, n_pos),
        "zero_miss_threshold": zero_miss,
        "phantoms_at_zero_miss": sum(1 for s, l in zip(scores, labels) if not l and zero_miss is not None and s >= zero_miss),
    }


def cross_validate_groups(cache_dir, cases, meta, head_kind, hp, seeds, device, use_flips, k,
                          log=sys.stderr, progress=None, calibrate=False, target_fpr=0.0, aggregate="max"):
    """Recording-group k-fold. Every case is scored by a head that never saw
    that fold or any recording sibling of it, so a group cannot leak across the
    split. Cases tagged `aux` are auxiliary training data: they join every
    training fold but are never held out or scored. Returns per-case out-of-fold
    scores, locations, and the fold map, all for the non-auxiliary cases."""
    aux = [c for c in cases if "aux" in c.get("tags", [])]
    main = [c for c in cases if "aux" not in c.get("tags", [])]
    folds = grouped_folds(main, k)
    for i, held in enumerate(folds, 1):
        if not held:
            continue
        train_cases = [c for j, members in enumerate(folds, 1) if j != i for c in members] + aux
        assert_split_disjoint(train_cases, held)
    fps, tubelet, dim = meta["fps"], meta["encoder"]["tubelet"], meta["encoder"]["hidden"]
    lazy = build_head(head_kind, dim).prepare_raw is not None
    examples = load_examples(cache_dir, cases, use_flips, lazy=lazy)
    per_case = {c["id"]: [] for c in main}
    locs, fold_of, thresholds = {}, {}, {}
    t0 = time.time()
    stage_examples(head_kind, dim, examples, device)
    print(f"  {len(examples)} examples ({len(main)} evaluated + {len(aux)} aux) on {device} "
          f"({time.time() - t0:.0f}s); {k} folds {[len(f) for f in folds]}", file=log)
    for fi, held in enumerate(folds, 1):
        if not held:
            continue
        held_ids = {c["id"] for c in held}
        # The held fold is scored by the model fitted on the FULL training
        # partition, so the locked and hindsight diagnostics share one model and
        # differ only in how the threshold was set. The threshold itself comes
        # from a separate inner fit that never saw the calibration fold.
        calib_fold = folds[fi % len(folds)] if calibrate else []
        calib_ids = {c["id"] for c in calib_fold}
        train = [e for e in examples if e.case["id"] not in held_ids]
        train_fit = [e for e in train if e.case["id"] not in calib_ids]
        stats = token_stats(train, device)
        fold_threshold = 0.5
        if calibrate and calib_fold:
            stats_fit = token_stats(train_fit, device)
            for ex in train_fit:
                ex.rep = None
            head_c = fit(head_kind, dim, train_fit, fps, tubelet, device, hp, seeds[0], stats=stats_fit)
            cs = []
            for c in calib_fold:
                test = next(e for e in examples if e.case["id"] == c["id"] and not e.flipped)
                p, _ = score_clip(head_c, test.feats, fps, tubelet, device, aggregate=aggregate)
                cs.append(p)
            fold_threshold = choose_threshold(cs, [c["contact"] for c in calib_fold], target_fpr)
        for ex in train:
            ex.rep = None
        for seed in seeds:
            head = fit(head_kind, dim, train, fps, tubelet, device, hp, seed, stats=stats)
            for c in held:
                test = next(e for e in examples if e.case["id"] == c["id"] and not e.flipped)
                p, loc = score_clip(head, test.feats, fps, tubelet, device, aggregate=aggregate)
                per_case[c["id"]].append(p)
                locs[c["id"]] = loc
        for c in held:
            thresholds[c["id"]] = fold_threshold
            fold_of[c["id"]] = fi
        n_pos = sum(1 for c in held if c["contact"])
        print(f"  fold {fi}/{len(folds)}: {len(held)} held ({n_pos} contact) "
              f"threshold={fold_threshold:.3f} ({time.time() - t0:.0f}s)", file=log)
        if progress:
            progress(fi, len(folds), held[0],
                     float(np.mean([np.mean(per_case[c["id"]]) for c in held])), time.time() - t0)
    return per_case, locs, fold_of, thresholds


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
    # AUC within each provenance. A head that only ranks across sources has
    # learned the camera/compression, not contact.
    by_source = {}
    for src in sorted({c["source"] for c in cases}):
        sel = [(s, l) for c, s, l in zip(cases, mean_scores, labels) if c["source"] == src]
        by_source[src] = {"n": len(sel), "positives": sum(l for _, l in sel),
                          "auc": auc([s for s, _ in sel], [l for _, l in sel])}
    return {
        "auc_by_source": by_source,
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
    examples = load_examples(cache_dir, cases, use_flips, lazy=build_head(head_kind, dim).prepare_raw is not None)
    stage_examples(head_kind, dim, examples, device)
    return fit(head_kind, dim, examples, fps, tubelet, device, hp, seed)


def save_head(path, head, head_kind, meta, hp, threshold):
    torch.save({"state": head.state_dict(), "head": head_kind, "meta": meta, "hp": hp,
                "temporal_crop_policy": TEMPORAL_CROP_POLICY,
                "threshold": threshold}, path)


def load_head(path, device):
    ck = torch.load(path, map_location=device, weights_only=False)
    head = build_head(ck["head"], ck["meta"]["encoder"]["hidden"]).to(device)
    head.load_state_dict(ck["state"])
    head.eval()
    return head, ck
