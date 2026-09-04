#!/usr/bin/env python3
"""V-JEPA 2 contact-detection experiment for teslcam-bench.

    encode   sample and encode every labeled bench clip into a feature cache
    train    leave-one-case-out cross-validation of a head on that cache,
             then fit a final head on all cases for `analyze`
    analyze  the bench's -analyzer contract: clip paths in, JSON verdict out

Every flag that changes what the encoder sees is required and recorded in the
cache's meta.json, so a cache always states how it was made. See README.md.
"""

import argparse
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

from vjb import analyze as an  # noqa: E402
from vjb import store, train as tr  # noqa: E402
from vjb.encoder import build_encoder  # noqa: E402

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
    cases = store.dataset_cases(a.dataset)
    if a.case:
        keep = set(a.case.split(","))
        cases = [c for c in cases if c["id"] in keep or keep & set(c["tags"])]
        if not cases:
            raise SystemExit(f"-case {a.case} matched nothing")
    device = pick_device(a.device) if a.encoder != "fake" else "cpu"
    enc = build_encoder(a.encoder, a.model, device, a.dtype, a.size, arch=a.arch, src=a.src)
    if enc.info.size != a.size:
        raise SystemExit(f"-size {a.size} does not match the checkpoint's crop_size {enc.info.size}")
    meta = {"encoder": enc.info.as_dict(), "fps": a.fps, "size": a.size, "crop": a.crop,
            "frames": a.frames, "stride": a.stride, "match": a.match}
    store.write_meta(a.cache, meta)
    print(f"encoding {len(cases)} cases -> {a.cache}\n  {json.dumps(meta)}", file=sys.stderr)
    for c in cases:
        for flip in ([False, True] if a.flip else [False]):
            p = store.feature_path(a.cache, c["id"], c["clip"], flip=flip)
            if os.path.exists(p) and not a.force:
                print(f"  {c['id']}{' flip' if flip else ''}: cached", file=sys.stderr)
                continue
            match_dir = os.path.join(a.cache, "_matched", c["id"])
            tokens, starts, valid, n = an.encode_clip(enc, c["clip"], meta, match_dir, flip=flip)
            store.save_features(p, tokens, starts, valid, n)
    print("done", file=sys.stderr)


def hp_from_args(a):
    return {"lr": a.lr, "weight_decay": a.weight_decay, "epochs": a.epochs, "pos_weight": a.pos_weight,
            "mil_temp": a.mil_temp, "time_crop": a.time_crop, "token_drop": a.token_drop, "noise": a.noise}


def cmd_train(a):
    cases = store.dataset_cases(a.dataset)
    meta = store.read_meta(a.cache)
    device = pick_device(a.device)
    hp = hp_from_args(a)
    seeds = list(range(a.seeds))
    print(f"cache {a.cache}: {json.dumps(meta['encoder'])}\nhead={a.head} seeds={a.seeds} flips={a.flips} "
          f"device={device}\nhp={json.dumps(hp)}", file=sys.stderr)
    per_case, locs = tr.cross_validate(a.cache, cases, meta, a.head, hp, seeds, device, a.flips)
    summary = tr.summarize(cases, per_case, locs, a.threshold)
    summary.update({"head": a.head, "hp": hp, "seeds": a.seeds, "flips": a.flips, "cache": os.path.abspath(a.cache),
                    "encoder": meta["encoder"], "sampling": {k: meta[k] for k in ("fps", "size", "crop", "frames", "stride", "match")},
                    "notes": a.notes})
    print_report(summary)
    os.makedirs(os.path.dirname(os.path.abspath(a.out)) or ".", exist_ok=True)
    with open(a.out, "w") as f:
        json.dump(summary, f, indent=1)
    print(f"\nwrote {a.out}", file=sys.stderr)
    if a.save_head:
        head = tr.fit_final(a.cache, cases, meta, a.head, hp, seeds[0], device, a.flips)
        tr.save_head(a.save_head, head, a.head, meta, hp, a.threshold)
        print(f"wrote {a.save_head} (trained on all {len(cases)} cases; only meaningful for clips outside them)",
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


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    e = sub.add_parser("encode", help="sample + encode bench clips into a feature cache")
    e.add_argument("-dataset", default=DEFAULT_DATASET)
    e.add_argument("-cache", required=True, help="feature cache directory (one per configuration)")
    add_encoder_flags(e)
    e.add_argument("-fps", type=float, required=True, help="sampled frames per second (8 is a sane first value)")
    e.add_argument("-size", type=int, required=True, help="square frame size; must equal the checkpoint's crop_size (256 or 384)")
    e.add_argument("-crop", required=True, help="full | center | band:A,B  (see vjb/frames.py)")
    e.add_argument("-match", required=True, help="domain matching: none | pi (push every clip through the in-car "
                   "upload transcode, 3 fps / 640 px / low bitrate, so downloaded and field clips look alike)")
    e.add_argument("-frames", type=int, required=True, help="frames per encoder window (16..64, even)")
    e.add_argument("-stride", type=int, required=True, help="window stride in frames")
    e.add_argument("-flip", action="store_true", help="also cache a horizontally flipped copy (train-time augmentation)")
    e.add_argument("-case", default="", help="comma-separated case ids or tags to encode; default all labeled")
    e.add_argument("-force", action="store_true", help="re-encode clips already in the cache")
    e.set_defaults(fn=cmd_encode)

    t = sub.add_parser("train", help="leave-one-case-out CV on a cache; optionally fit a head to ship")
    t.add_argument("-dataset", default=DEFAULT_DATASET)
    t.add_argument("-cache", required=True)
    t.add_argument("-head", required=True, choices=["probe", "temporal", "meanpool"])
    t.add_argument("-out", required=True, help="where to write the JSON summary")
    t.add_argument("-save-head", default="", help="also fit on all cases and save the head here (.pt)")
    t.add_argument("-seeds", type=int, default=3, help="repeat each fold with this many seeds; report the mean")
    t.add_argument("-flips", action="store_true", help="use flipped copies from the cache for training")
    t.add_argument("-threshold", type=float, default=0.5)
    t.add_argument("-device", default="auto", choices=["auto", "cuda", "mps", "cpu"])
    t.add_argument("-lr", type=float, default=3e-4)
    t.add_argument("-weight-decay", type=float, default=0.05)
    t.add_argument("-epochs", type=int, default=40)
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

    a = ap.parse_args(argv)
    a.fn(a)


if __name__ == "__main__":
    main()
