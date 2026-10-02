#!/usr/bin/env python3
"""V-JEPA 2 contact-detection experiment for teslcam-bench.

    encode   sample and encode every labeled bench clip into a feature cache
    train    leave-one-case-out cross-validation of a head on that cache,
             then fit a final head on all cases for `analyze`
    finetune train the last K encoder blocks jointly with a head, from frames,
             for when a head on frozen features has plateaued
    analyze  the bench's -analyzer contract: clip paths in, JSON verdict out

Every flag that changes what the encoder sees is required and recorded in the
cache's meta.json, so a cache always states how it was made. See README.md.
"""

import argparse
import json
import os
import shutil
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

from vjb import analyze as an  # noqa: E402
from vjb import frames as fr, store, train as tr  # noqa: E402
from vjb.encoder import build_encoder  # noqa: E402
from vjb.heads import HEADS  # noqa: E402
from vjb.progress import Writer  # noqa: E402

MIN_FREE_BYTES = 2 * 1024**3  # refuse to grow a cache when the drive is nearly full
DEFAULT_DATASET = os.path.normpath(os.path.join(HERE, "..", "..", "dataset.json"))


def pick_device(name):
    import torch

    if name != "auto":
        return name
    if torch.cuda.is_available():
        return "cuda"
    if torch.backends.mps.is_available():
        return "mps"
    return "cpu"


def add_encoder_flags(p):
    p.add_argument("-encoder", required=True, choices=["vjepa2", "vjepa21", "fake"],
                   help="vjepa2 = the Hugging Face checkpoint in -model; vjepa21 = Meta's V-JEPA 2.1 checkpoint "
                        "file in -model through the official code in -src; fake = pixel statistics, for plumbing")
    p.add_argument("-model", default="", help="vjepa2: hub id, e.g. facebook/vjepa2-vitl-fpc64-256; "
                                             "vjepa21: path to the .pt from dl.fbaipublicfiles.com/vjepa2")
    p.add_argument("-arch", default="", help="vjepa21 only: vjepa2_1_vit_base_384 | vjepa2_1_vit_large_384 | "
                                            "vjepa2_1_vit_giant_384 | vjepa2_1_vit_gigantic_384")
    p.add_argument("-src", default="", help="vjepa21 only: path to a facebookresearch/vjepa2 checkout "
                                           "(third_party/vjepa2)")
    p.add_argument("-device", default="auto", choices=["auto", "cuda", "mps", "cpu"])
    p.add_argument("-dtype", default="float32", choices=["float32", "float16", "bfloat16"],
                   help="encoder weights/activations; float16 or bfloat16 on cuda, float32 on cpu/mps")


def cmd_encode(a):
    cases = store.dataset_cases(a.dataset) if a.labeled_only else store.encoding_cases(a.dataset)
    if a.case:
        keep = set(a.case.split(","))
        cases = [c for c in cases if c["id"] in keep or keep & set(c["tags"])]
        if not cases:
            raise SystemExit(f"-case {a.case} matched nothing")
    device = pick_device(a.device) if a.encoder != "fake" else "cpu"
    enc = build_encoder(a.encoder, a.model, device, a.dtype, a.size, arch=a.arch, src=a.src)
    if enc.info.size != a.size:
        raise SystemExit(f"-size {a.size} does not match the checkpoint's crop_size {enc.info.size}")
    meta = {"encoder": enc.info.as_dict(), "fps": a.fps, "size": a.size, "crop": fr.normalize_crop(a.crop),
            "frames": a.frames, "stride": a.stride, "match": a.match}
    if os.path.exists(os.path.join(a.cache, store.META)):
        old = store.read_meta(a.cache)
        if fr.normalize_crop(old["crop"]) == meta["crop"]:
            meta["crop"] = old["crop"]   # a legacy cache keeps its own spelling and stays extendable
    store.write_meta(a.cache, meta)
    variants = 2 if a.flip else 1
    progress = Writer(a.progress, "encode", total=len(cases) * variants, completed=0,
                      cache=os.path.abspath(a.cache), encoder=meta["encoder"], sampling=meta)
    print(f"encoding {len(cases)} cases -> {a.cache}\n  {json.dumps(meta)}", file=sys.stderr)
    done = 0
    for c in cases:
        for flip in ([False, True] if a.flip else [False]):
            p = store.feature_path(a.cache, c["id"], c["clip"], flip=flip)
            if os.path.exists(p) and not a.force:
                print(f"  {c['id']}{' flip' if flip else ''}: cached", file=sys.stderr)
            else:
                free = shutil.disk_usage(a.cache).free
                if free < MIN_FREE_BYTES:
                    raise SystemExit(f"refusing to encode: only {free/1e9:.1f} GB free on the cache drive "
                                     f"(< {MIN_FREE_BYTES/1e9:.0f} GB); free space or use -labeled-only")
                match_dir = os.path.join(a.cache, "_matched", c["id"])
                tokens, starts, valid, n = an.encode_clip(enc, c["clip"], meta, match_dir, flip=flip)
                store.save_features(p, tokens, starts, valid, n)
            done += 1
            progress.write(completed=done, current_case=c["id"], variant="flip" if flip else "original")
    progress.complete(completed=done)
    print("done", file=sys.stderr)


def hp_from_args(a):
    return {"lr": a.lr, "weight_decay": a.weight_decay, "epochs": a.epochs, "pos_weight": a.pos_weight,
            "mil_temp": a.mil_temp, "batch": a.batch, "time_crop": a.time_crop, "token_drop": a.token_drop, "noise": a.noise}


def cmd_train(a):
    cases = store.dataset_cases(a.dataset)
    train_cases = [c for c in cases if "holdout" not in c["tags"]] if a.split == "holdout" else cases
    eval_cases = [c for c in cases if "holdout" in c["tags"]] if a.split == "holdout" else cases
    if not train_cases or not eval_cases:
        raise SystemExit(f"-split {a.split} needs non-empty training and evaluation sets")
    meta = store.read_meta(a.cache)
    device = pick_device(a.device)
    hp = hp_from_args(a)
    seeds = list(range(a.seed_start, a.seed_start + a.seeds))
    if a.split == "fit":
        if not a.save_head:
            raise SystemExit("-split fit needs -save-head")
        head = tr.fit_final(a.cache, cases, meta, a.head, hp, seeds[0], device, a.flips)
        tr.save_head(a.save_head, head, a.head, meta, hp, a.threshold)
        print(f"fitted {a.head} on {len(cases)} cases -> {a.save_head}", file=sys.stderr)
        return
    print(f"cache {a.cache}: {json.dumps(meta['encoder'])}\nhead={a.head} seeds={a.seeds} flips={a.flips} "
          f"device={device}\nhp={json.dumps(hp)}", file=sys.stderr)
    history = []
    progress = Writer(a.progress, "train", total=len(eval_cases), completed=0, head=a.head,
                      cache=os.path.abspath(a.cache), seeds=a.seeds, split=a.split,
                      train_cases=len(train_cases), eval_cases=len(eval_cases))
    def on_fold(index, total, held, score, elapsed):
        history.append({"case": held["id"], "label": held["contact"], "score": round(score, 4)})
        labels = [x["label"] for x in history]
        scores = [x["score"] for x in history]
        progress.write(completed=index, total=total, current_case=held["id"], elapsed_s=elapsed,
                       interim={"auc": tr.auc(scores, labels),
                                "missed": sum(l and s < a.threshold for s, l in zip(scores, labels)),
                                "phantoms": sum((not l) and s >= a.threshold for s, l in zip(scores, labels)),
                                "positives": sum(labels), "negatives": len(labels)-sum(labels)},
                       history=history[-40:])
    if a.split == "holdout":
        per_case, locs = tr.holdout_validate(a.cache, train_cases, eval_cases, meta, a.head, hp, seeds,
                                             device, a.flips, progress=on_fold)
        summary = tr.summarize(eval_cases, per_case, locs, a.threshold)
    elif a.split == "kfold":
        per_case, locs, fold_of, thresholds = tr.cross_validate_groups(
            a.cache, cases, meta, a.head, hp, seeds, device, a.flips, a.folds,
            progress=on_fold, calibrate=a.calibrate, target_fpr=a.target_fpr, aggregate=a.aggregate)
        main = [c for c in cases if "aux" not in c["tags"]]
        if a.calibrate:
            summary = tr.summarize_locked(main, per_case, locs, thresholds, fold_of)
        else:
            summary = tr.summarize_groups(main, per_case, locs, a.threshold, fold_of)
    else:
        per_case, locs = tr.cross_validate(a.cache, cases, meta, a.head, hp, seeds, device, a.flips,
                                           progress=on_fold)
        summary = tr.summarize(eval_cases, per_case, locs, a.threshold)
    summary["temporal_crop_policy"] = tr.TEMPORAL_CROP_POLICY
    summary.update({"head": a.head, "hp": hp, "seeds": a.seeds, "flips": a.flips, "cache": os.path.abspath(a.cache),
                    "encoder": meta["encoder"], "sampling": {k: meta[k] for k in ("fps", "size", "crop", "frames", "stride", "match")},
                    "notes": a.notes})
    print_report(summary)
    os.makedirs(os.path.dirname(os.path.abspath(a.out)) or ".", exist_ok=True)
    with open(a.out, "w") as f:
        json.dump(summary, f, indent=1)
    progress.complete(completed=len(eval_cases), result=os.path.abspath(a.out), metrics={
        k: summary.get(k) for k in ("auc", "contact_missed", "contact_phantom", "positives", "negatives",
                                    "loc_hits", "phantoms_at_zero_miss")})
    print(f"\nwrote {a.out}", file=sys.stderr)
    if a.save_head:
        head = tr.fit_final(a.cache, train_cases, meta, a.head, hp, seeds[0], device, a.flips)
        tr.save_head(a.save_head, head, a.head, meta, hp, a.threshold)
        print(f"wrote {a.save_head} (trained on {len(train_cases)} training cases)",
              file=sys.stderr)


def print_report(s):
    print(f"\n{'case':<32} {'label':<8} {'score':>6} {'±seed':>6}  pred     loc")
    for r in s["cases"]:
        flag = ""
        if r["label"] and not r["pred"]:
            flag = "  <- MISSED"
        elif not r["label"] and r["pred"]:
            flag = "  <- PHANTOM"
        print(f"{r['case']:<32} {'contact' if r['label'] else 'none':<8} {r['score']:>6.3f} {r['seed_std']:>6.3f}  "
              f"{'contact' if r['pred'] else 'none':<8} {r['loc_seconds'][0]:>5.1f}-{r['loc_seconds'][1]:<5.1f}{flag}")
    auc = "n/a" if s["auc"] is None else f"{s['auc']:.3f}"
    print(f"\ncontact missed {s['contact_missed']}/{s['positives']}   contact phantom {s['contact_phantom']}/{s['negatives']}"
          f"   threshold {s['threshold']}   AUC {auc}")
    for src, v in s["auc_by_source"].items():
        a = "n/a" if v["auc"] is None else f"{v['auc']:.3f}"
        print(f"  within {src:<4} n={v['n']:<3} positives={v['positives']:<3} AUC {a}")
    if s["zero_miss_threshold"] is not None:
        print(f"zero-miss threshold {s['zero_miss_threshold']:.3f} would cost {s['phantoms_at_zero_miss']}/{s['negatives']} phantoms")


def cmd_analyze(a):
    device = pick_device(a.device)
    head, ck = tr.load_head(a.head_file, device)
    meta = ck["meta"]
    threshold = a.threshold if a.threshold is not None else ck["threshold"]
    encoder = None
    if not a.cache_only:
        e = meta["encoder"]
        enc_kind = "fake" if e["name"] == "fake" else e.get("kind", "vjepa2")
        if enc_kind == "vjepa21" and not (a.model and a.src):
            raise SystemExit("this head was trained on V-JEPA 2.1 features: pass -model <checkpoint.pt> and -src <vjepa2 checkout>")
        encoder = build_encoder(enc_kind, a.model if enc_kind == "vjepa21" else e["name"],
                                device if enc_kind != "fake" else "cpu", a.dtype, meta["size"],
                                arch=e.get("arch"), src=a.src)
    # The bench passes cameras best-ranked first; this analyzer scores each and
    # reports the most suspicious, which is the max-over-cameras rule the
    # combiner will use anyway.
    best = (-1.0, None)
    for clip in a.clips:
        feats = an.features_for(encoder, clip, meta, a.cache)
        p, loc = tr.score_clip(head, feats, meta["fps"], meta["encoder"]["tubelet"], device)
        if p > best[0]:
            best = (p, loc)
    print(an.verdict(best[0], best[1], threshold, ck["head"], meta["encoder"]["name"], len(a.clips)))


def cmd_evaluate(a):
    """Score a saved head on a labeled cache without fitting anything."""
    cases = store.dataset_cases(a.dataset)
    device = pick_device(a.device)
    head, ck = tr.load_head(a.head_file, device)
    meta = store.read_meta(a.cache)
    if ({**meta, "crop": fr.normalize_crop(meta["crop"])}
            != {**ck["meta"], "crop": fr.normalize_crop(ck["meta"]["crop"])}):
        raise SystemExit("evaluation cache metadata does not match the saved head")
    progress = Writer(a.progress, "evaluate", total=len(cases), completed=0,
                      cache=os.path.abspath(a.cache), head=ck["head"], dataset=os.path.abspath(a.dataset))
    per_case, locs, history = {}, {}, []
    for index, case in enumerate(cases, 1):
        feats = store.ClipFeatures(store.feature_path(a.cache, case["id"], case["clip"]))
        score, loc = tr.score_clip(head, feats, meta["fps"], meta["encoder"]["tubelet"], device)
        per_case[case["id"]] = [score]
        locs[case["id"]] = loc
        history.append({"case": case["id"], "label": case["contact"], "score": round(score, 4)})
        progress.write(completed=index, current_case=case["id"], history=history[-40:])
    summary = tr.summarize(cases, per_case, locs, a.threshold if a.threshold is not None else ck["threshold"])
    summary.update({"head": ck["head"], "cache": os.path.abspath(a.cache), "encoder": meta["encoder"],
                    "sampling": {k: meta[k] for k in ("fps", "size", "crop", "frames", "stride", "match")},
                    "notes": a.notes})
    print_report(summary)
    os.makedirs(os.path.dirname(os.path.abspath(a.out)) or ".", exist_ok=True)
    with open(a.out, "w") as f:
        json.dump(summary, f, indent=1)
    progress.complete(completed=len(cases), result=os.path.abspath(a.out), metrics={
        k: summary.get(k) for k in ("auc", "contact_missed", "contact_phantom", "positives", "negatives",
                                    "loc_hits", "phantoms_at_zero_miss")})


def add_sampling_flags(p):
    """The flags that decide what the encoder sees. `encode` and `finetune`
    must agree on them exactly, so they are declared once."""
    p.add_argument("-fps", type=float, required=True, help="sampled frames per second")
    p.add_argument("-size", type=int, required=True, help="square frame size; must equal the checkpoint's crop_size")
    p.add_argument("-crop", required=True, help="squash | center | bottom | band:A,B  (see vjb/frames.py; "
                   "'full' is the deprecated name of 'squash')")
    p.add_argument("-match", required=True, help="domain matching: none | pi")
    p.add_argument("-frames", type=int, required=True, help="frames per encoder window (16..64, even)")
    p.add_argument("-stride", type=int, required=True, help="window stride in frames")


def ft_hp_from_args(a):
    return {"lr": a.lr, "lr_encoder": a.lr_encoder, "weight_decay": a.weight_decay, "epochs": a.epochs,
            "pos_weight": a.pos_weight, "mil_temp": a.mil_temp, "accum": a.accum,
            "max_windows_per_clip": a.max_windows_per_clip, "unfreeze": a.unfreeze}


def cmd_finetune(a):
    from vjb import finetune as ft

    cases = store.dataset_cases(a.dataset)
    if a.case:
        keep = set(a.case.split(","))
        cases = [c for c in cases if c["id"] in keep or keep & set(c["tags"])]
        if not cases:
            raise SystemExit(f"-case {a.case} matched nothing")
    if a.encoder == "fake":
        raise SystemExit("the fake encoder has no transformer blocks to unfreeze; finetune needs vjepa2 or vjepa21")
    device = pick_device(a.device)
    enc = build_encoder(a.encoder, a.model, device, a.dtype, a.size, arch=a.arch, src=a.src)
    if enc.info.size != a.size:
        raise SystemExit(f"-size {a.size} does not match the checkpoint's crop_size {enc.info.size}")
    meta = {"encoder": enc.info.as_dict(), "fps": a.fps, "size": a.size, "crop": fr.normalize_crop(a.crop),
            "frames": a.frames, "stride": a.stride, "match": a.match}
    meta["dtype"] = a.dtype
    meta["checkpoint_sha256"] = (ft.checkpoint_digest(a.model) if os.path.isfile(a.model)
                                 else ft.model_digest(enc.model))
    split_enc = ft.SplitEncoder(enc, a.unfreeze, checkpointing=not a.no_checkpointing)
    hp = ft_hp_from_args(a)
    cache_root = "" if a.cache == "none" else a.cache
    print(f"finetune {a.arch or a.model}: {split_enc.depth} blocks, last {a.unfreeze} trainable "
          f"(split at {split_enc.split}), {sum(p.numel() for p in split_enc.trainable) / 1e6:.1f}M encoder params\n"
          f"device={device} dtype={a.dtype} split={a.split} cache={cache_root or 'disabled'} "
          f"key={ft.cache_key(meta, a.unfreeze)}\nhp={json.dumps(hp)}", file=sys.stderr)
    per_case, locs, evaluated, trainer = ft.run(
        cases, meta, split_enc, a.head, hp, device, cache_root, a.split, a.epochs, a.seed,
        flip_augment=a.flip, stats_windows=a.stats_windows)
    summary = tr.summarize(evaluated, per_case, locs, a.threshold)
    summary.update({"head": a.head, "hp": hp, "seeds": 1, "flips": a.flip, "cache": os.path.abspath(cache_root) if cache_root else "",
                    "encoder": meta["encoder"], "sampling": {k: meta[k] for k in ("fps", "size", "crop", "frames", "stride", "match")},
                    "finetune": {"unfreeze": a.unfreeze, "split": split_enc.split, "depth": split_enc.depth,
                                 "cv": a.split, "epochs": a.epochs, "seed": a.seed},
                    "notes": a.notes})
    print_report(summary)
    os.makedirs(os.path.dirname(os.path.abspath(a.out)) or ".", exist_ok=True)
    with open(a.out, "w") as f:
        json.dump(summary, f, indent=1)
    print(f"\nwrote {a.out}", file=sys.stderr)
    if a.save_encoder:
        ft.save_encoder(a.save_encoder, split_enc, trainer.head, a.head, meta, hp, a.threshold)
        print(f"wrote {a.save_encoder} (last {a.unfreeze} blocks + head)", file=sys.stderr)


def build_parser():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    e = sub.add_parser("encode", help="sample + encode bench clips into a feature cache")
    e.add_argument("-dataset", default=DEFAULT_DATASET)
    e.add_argument("-cache", required=True, help="feature cache directory (one per configuration)")
    add_encoder_flags(e)
    e.add_argument("-fps", type=float, required=True, help="sampled frames per second (8 is a sane first value)")
    e.add_argument("-size", type=int, required=True, help="square frame size; must equal the checkpoint's crop_size (256 or 384)")
    e.add_argument("-crop", required=True, help="squash | center | bottom | band:A,B  (see vjb/frames.py; "
                   "'full' is the deprecated name of 'squash')")
    e.add_argument("-match", required=True, help="domain matching: none | pi (push every clip through the in-car "
                   "upload transcode, 3 fps / 640 px / low bitrate, so downloaded and field clips look alike)")
    e.add_argument("-frames", type=int, required=True, help="frames per encoder window (16..64, even)")
    e.add_argument("-stride", type=int, required=True, help="window stride in frames")
    e.add_argument("-flip", action="store_true", help="also cache a horizontally flipped copy (train-time augmentation)")
    e.add_argument("-case", default="", help="comma-separated case ids or tags to encode; default all labeled")
    e.add_argument("-force", action="store_true", help="re-encode clips already in the cache")
    e.add_argument("-labeled-only", action="store_true", help="encode only labeled cases, not the unlabeled backlog")
    e.add_argument("-progress", default="", help="atomic JSON status file for the training dashboard")
    e.set_defaults(fn=cmd_encode)

    t = sub.add_parser("train", help="leave-one-case-out CV on a cache; optionally fit a head to ship")
    t.add_argument("-dataset", default=DEFAULT_DATASET)
    t.add_argument("-cache", required=True)
    t.add_argument("-head", required=True, choices=sorted(HEADS))
    t.add_argument("-out", required=True, help="where to write the JSON summary")
    t.add_argument("-save-head", default="", help="also fit on all cases and save the head here (.pt)")
    t.add_argument("-seeds", type=int, default=3, help="repeat each fold with this many seeds; report the mean")
    t.add_argument("-split", choices=["loo", "holdout", "kfold", "fit"], default="loo",
                   help="holdout trains on cases without the holdout tag and evaluates those with it; "
                        "kfold is recording-group k-fold with -folds folds; fit skips evaluation and "
                        "only fits a head for -save-head")
    t.add_argument("-folds", type=int, default=5, help="number of recording-group folds for -split kfold")
    t.add_argument("-calibrate", action="store_true", help="kfold: choose each fold's threshold on a held-out inner calibration fold")
    t.add_argument("-target-fpr", type=float, default=0.0, help="kfold -calibrate: max calibration false-positive rate allowed when choosing the threshold")
    t.add_argument("-aggregate", choices=["max","top2","mean"], default="max", help="how window probabilities become the clip score")
    t.add_argument("-flips", action="store_true", help="use flipped copies from the cache for training")
    t.add_argument("-seed-start", type=int, default=0,
                   help="first deterministic seed (permits experiment runners to retain per-seed results)")
    t.add_argument("-threshold", type=float, default=0.5)
    t.add_argument("-device", default="auto", choices=["auto", "cuda", "mps", "cpu"])
    t.add_argument("-progress", default="", help="atomic JSON status file for the training dashboard")
    t.add_argument("-lr", type=float, default=3e-4)
    t.add_argument("-weight-decay", type=float, default=0.05)
    t.add_argument("-epochs", type=int, default=40)
    t.add_argument("-batch", type=int, default=16, help="clips per optimizer step")
    t.add_argument("-pos-weight", type=float, default=2.0, help="loss weight on positives (few positives, many negatives)")
    t.add_argument("-mil-temp", type=float, default=4.0, help="softness of the max over windows for clip-level labels")
    t.add_argument("-time-crop", type=float, default=0.3, help="max fraction of timesteps to crop away (temporal jitter)")
    t.add_argument("-token-drop", type=float, default=0.1)
    t.add_argument("-noise", type=float, default=0.05, help="gaussian noise as a fraction of token std")
    t.add_argument("-notes", default="")
    t.set_defaults(fn=cmd_train)

    z = sub.add_parser("analyze", help="bench -analyzer contract")
    z.add_argument("-head-file", required=True, help=".pt written by train -save-head")
    z.add_argument("-cache", default="", help="feature cache to read bench clips from instead of encoding")
    z.add_argument("-cache-only", action="store_true", help="never load an encoder; fail if a clip is not cached")
    z.add_argument("-threshold", type=float, default=None, help="override the threshold stored with the head")
    z.add_argument("-device", default="auto", choices=["auto", "cuda", "mps", "cpu"])
    z.add_argument("-dtype", default="float32", choices=["float32", "float16", "bfloat16"])
    z.add_argument("-model", default="", help="vjepa21 heads: path to the checkpoint .pt used at encode time")
    z.add_argument("-src", default="", help="vjepa21 heads: path to the facebookresearch/vjepa2 checkout")
    z.add_argument("clips", nargs="+")
    z.set_defaults(fn=cmd_analyze)

    v = sub.add_parser("evaluate", help="score a saved head on a labeled feature cache without training")
    v.add_argument("-dataset", required=True)
    v.add_argument("-cache", required=True)
    v.add_argument("-head-file", required=True)
    v.add_argument("-out", required=True)
    v.add_argument("-threshold", type=float, default=None)
    v.add_argument("-device", default="auto", choices=["auto", "cuda", "mps", "cpu"])
    v.add_argument("-progress", default="")
    v.add_argument("-notes", default="")
    v.set_defaults(fn=cmd_evaluate)

    f = sub.add_parser("finetune", help="fine-tune the last K encoder blocks jointly with a head, from frames")
    f.add_argument("-dataset", default=DEFAULT_DATASET)
    f.add_argument("-cache", required=True, help="directory for the frozen-trunk cache, or 'none' to recompute "
                                                 "the frozen blocks every epoch")
    add_encoder_flags(f)
    add_sampling_flags(f)
    f.add_argument("-unfreeze", type=int, required=True,
                   help="number of final transformer blocks to train; 0 = head only (the frozen path)")
    f.add_argument("-epochs", type=int, required=True)
    f.add_argument("-lr", type=float, required=True, help="learning rate for the head")
    f.add_argument("-lr-encoder", type=float, required=True,
                   help="learning rate for the unfrozen blocks; 10-100x smaller than -lr is the usual choice")
    f.add_argument("-split", required=True, choices=["loo", "holdout"],
                   help="holdout: train on cases without the 'holdout' tag, evaluate on those with it. "
                        "loo: leave-one-case-out, which retrains once per case and costs that many times more")
    f.add_argument("-out", required=True, help="where to write the JSON summary (same shape as `train`)")
    f.add_argument("-head", default="temporal", choices=sorted(HEADS))
    f.add_argument("-save-encoder", default="", help="save the unfrozen block weights + head here (.pt)")
    f.add_argument("-accum", type=int, default=8, help="windows per optimizer step (gradient accumulation); "
                                                       "a step lands on a clip boundary once this many windows have accumulated")
    f.add_argument("-max-windows-per-clip", type=int, default=8,
                   help="windows sampled per clip per epoch; positives are biased toward the labeled interval. "
                        "0 = every window")
    f.add_argument("-stats-windows", type=int, default=2,
                   help="windows per clip used once, before training, for the head's standardization statistics")
    f.add_argument("-weight-decay", type=float, default=0.05)
    f.add_argument("-pos-weight", type=float, default=2.0)
    f.add_argument("-mil-temp", type=float, default=4.0)
    f.add_argument("-threshold", type=float, default=0.5)
    f.add_argument("-seed", type=int, default=0)
    f.add_argument("-flip", action="store_true", help="also train on a horizontally flipped copy of each clip")
    f.add_argument("-no-checkpointing", action="store_true",
                   help="disable gradient checkpointing on the trainable blocks (faster, much more memory)")
    f.add_argument("-case", default="", help="comma-separated case ids or tags to use; default all labeled")
    f.add_argument("-notes", default="")
    f.set_defaults(fn=cmd_finetune)

    return ap


def main(argv=None):
    a = build_parser().parse_args(argv)
    a.fn(a)


if __name__ == "__main__":
    main()
