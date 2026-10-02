#!/usr/bin/env python
"""Qwen3.8 contact detection on the sentry bench.

    zeroshot   score every labeled case with the base model
    report     re-score a saved run (re-threshold without re-running the model)

Every flag that changes what the model sees is required rather than defaulted,
so a saved run can never be ambiguous about how it was produced.
"""

import argparse
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from vlmb import metrics
from vlmb.data import load_cases, summary
from vlmb.frames import frames_for


def tag_filter(value):
    """`-exclude-tag none` means no filtering. Spelled explicitly rather than
    defaulted, so a run can never quietly train on the shared holdout."""
    return None if value == "none" else value


def cmd_zeroshot(args):
    from vlmb.infer import ContactScorer

    cases = load_cases(exclude_tag=tag_filter(args.exclude_tag))
    print(summary(cases), flush=True)
    print(f"model: {args.model}", flush=True)
    if args.adapter:
        print(f"adapter: {args.adapter}", flush=True)

    t0 = time.time()
    scorer = ContactScorer(args.model, adapter_path=args.adapter)
    print(f"loaded in {time.time() - t0:.0f}s", flush=True)

    rows, t0 = [], time.time()
    for i, case in enumerate(cases, 1):
        frames = frames_for(case, args.frames, args.n_frames, args.size,
                            sampling=args.sampling)
        t1 = time.time()
        p = scorer.p_contact(frames)
        rows.append({"id": case.id, "contact": case.contact,
                     "threat": case.threat, "p_contact": p})
        print(f"  {i:2d}/{len(cases)}  {case.id[:38]:38s} "
              f"p={p:.3f}  label={'contact' if case.contact else 'none':7s} "
              f"{time.time() - t1:.1f}s", flush=True)

    run = {
        "model": args.model, "adapter": args.adapter,
        "exclude_tag": args.exclude_tag, "sampling": args.sampling,
        "n_frames": args.n_frames, "size": args.size,
        "seconds": time.time() - t0, "rows": rows,
    }
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w") as fh:
        json.dump(run, fh, indent=1)
    print(f"\nwrote {args.out} ({run['seconds']:.0f}s)", flush=True)
    show(run, args.min_recall)


def show(run, min_recall):
    y = [r["contact"] for r in run["rows"]]
    s = [r["p_contact"] for r in run["rows"]]
    rep = metrics.report(y, s, min_recall=min_recall)
    print("\n=== scored ===")
    print(f"  n={rep['n']} ({rep['n_pos']} contact)")
    print(f"  AUC {rep['auc']:.3f}   AP {rep['ap']:.3f}")
    print(f"  at threshold {rep['threshold']:.3f} (max precision at recall>={min_recall}):")
    print(f"    precision {rep['precision']:.3f}  recall {rep['recall']:.3f}")
    print(f"    tp={rep['tp']} fp={rep['fp']} fn={rep['fn']} tn={rep['tn']}")
    lo = sorted(run["rows"], key=lambda r: r["p_contact"])
    print("\n  most confident 'no contact':")
    for r in lo[:5]:
        print(f"    {r['p_contact']:.3f} {r['id'][:40]:40s} label={r['contact']}")
    print("  most confident 'contact':")
    for r in lo[-5:][::-1]:
        print(f"    {r['p_contact']:.3f} {r['id'][:40]:40s} label={r['contact']}")


def cmd_report(args):
    with open(args.run) as fh:
        show(json.load(fh), args.min_recall)


def cmd_cv(args):
    """Cross-validated LoRA fine-tuning.

    Each fold trains an adapter on the other folds and scores only its own
    held-out cases, so every reported score comes from a model that never saw
    that clip. Scores are pooled across folds before any threshold is chosen.
    """
    from vlmb.cv import splits
    from vlmb.infer import ContactScorer
    from vlmb.sft import train_adapter, write_fold

    cases = load_cases(exclude_tag=tag_filter(args.exclude_tag))
    y = [c.contact for c in cases]
    print(summary(cases), flush=True)
    print(f"{args.folds}-fold, seed {args.seed}, {args.epochs} epochs, "
          f"rank {args.lora_rank}, lr {args.learning_rate}, "
          f"balance={args.balance}, exclude={args.exclude_tag}", flush=True)

    os.makedirs(args.work, exist_ok=True)
    pooled = [None] * len(cases)
    fold_logs = []

    for fold, (train_idx, test_idx) in enumerate(splits(y, args.folds, args.seed)):
        tag = f"fold{fold}"
        fold_dir = os.path.join(args.work, tag)
        os.makedirs(fold_dir, exist_ok=True)
        adapter_dir = os.path.join(fold_dir, "adapter")
        log = os.path.join(fold_dir, "train.log")

        print(f"\n=== {tag}: train {len(train_idx)}, test {len(test_idx)} ===",
              flush=True)

        # Resume: a fold whose adapter is already on disk is not retrained.
        # A fold costs over an hour and the trainer only checkpoints every
        # --steps-per-save steps, which is more steps than a fold has — so a
        # fold is all-or-nothing and the finished ones must be reusable.
        trained = os.path.join(adapter_dir, "adapters.safetensors")
        if os.path.exists(trained) and os.path.exists(
                os.path.join(adapter_dir, "adapter_config.json")):
            print(f"  reusing adapter already in {adapter_dir}", flush=True)
            train_s, rc = 0.0, 0
        else:
            write_fold(cases, train_idx, args.frames, args.n_frames, args.size,
                       fold_dir, balance=args.balance == "yes", seed=args.seed)
            t0 = time.time()
            rc, cmd = train_adapter(
                sys.executable, args.model, fold_dir, adapter_dir,
                epochs=args.epochs, batch_size=args.batch_size,
                learning_rate=args.learning_rate, lora_rank=args.lora_rank,
                lora_alpha=args.lora_alpha, max_seq_length=args.max_seq_length,
                log_path=log,
            )
            train_s = time.time() - t0
        if rc != 0:
            print(f"  training FAILED rc={rc}; see {log}", flush=True)
            print("  last lines:", flush=True)
            with open(log) as fh:
                for line in fh.readlines()[-15:]:
                    print("   ", line.rstrip(), flush=True)
            raise SystemExit(f"{tag} training failed")
        print(f"  trained in {train_s / 60:.1f} min -> {adapter_dir}", flush=True)

        scores_path = os.path.join(fold_dir, "scores.json")
        if os.path.exists(scores_path):
            with open(scores_path) as fh:
                cached = json.load(fh)
            for i in test_idx:
                pooled[i] = cached[cases[i].id]
            print(f"  reusing {len(test_idx)} scores from {scores_path}",
                  flush=True)
        else:
            scorer = ContactScorer(args.model, adapter_path=adapter_dir)
            got = {}
            for i in test_idx:
                frames = frames_for(cases[i], args.frames, args.n_frames,
                                    args.size)
                p = scorer.p_contact(frames)
                pooled[i] = p
                got[cases[i].id] = p
                print(f"    {cases[i].id[:38]:38s} p={p:.3f} "
                      f"label={'contact' if y[i] else 'none'}", flush=True)
                # written every case, so an interrupted fold keeps its work
                with open(scores_path, "w") as fh:
                    json.dump(got, fh, indent=1)
            del scorer
        fold_logs.append({"fold": fold, "train_seconds": train_s,
                          "n_train": len(train_idx), "n_test": len(test_idx)})

    run = {
        "model": args.model, "mode": "cv-lora",
        "folds": args.folds, "seed": args.seed, "epochs": args.epochs,
        "lora_rank": args.lora_rank, "lora_alpha": args.lora_alpha,
        "learning_rate": args.learning_rate,
        "n_frames": args.n_frames, "size": args.size,
        "balance": args.balance, "exclude_tag": args.exclude_tag,
        "fold_logs": fold_logs,
        "rows": [{"id": c.id, "contact": c.contact, "threat": c.threat,
                  "p_contact": p} for c, p in zip(cases, pooled)],
    }
    with open(args.out, "w") as fh:
        json.dump(run, fh, indent=1)
    print(f"\nwrote {args.out}", flush=True)
    show(run, args.min_recall)


def main():
    ap = argparse.ArgumentParser(prog="vlmbench")
    sub = ap.add_subparsers(dest="cmd", required=True)

    z = sub.add_parser("zeroshot")
    z.add_argument("-model", required=True, help="path to the MLX model")
    z.add_argument("-adapter", default=None, help="LoRA adapter to load")
    z.add_argument("-frames", required=True, help="frame cache root")
    z.add_argument("-n-frames", type=int, required=True)
    z.add_argument("-size", type=int, required=True, help="long-edge pixels")
    z.add_argument("-out", required=True)
    z.add_argument("-min-recall", type=float, required=True,
                   help="lowest recall the reported operating point may have")
    z.add_argument("-exclude-tag", required=True,
                   help="drop cases carrying this tag; 'none' keeps all")
    z.add_argument("-sampling", required=True, choices=("uniform", "motion"),
                   help="how frames are placed in time")
    z.set_defaults(func=cmd_zeroshot)

    r = sub.add_parser("report")
    r.add_argument("-run", required=True)
    r.add_argument("-min-recall", type=float, required=True)
    r.set_defaults(func=cmd_report)

    c = sub.add_parser("cv")
    c.add_argument("-model", required=True)
    c.add_argument("-frames", required=True)
    c.add_argument("-n-frames", type=int, required=True)
    c.add_argument("-size", type=int, required=True)
    c.add_argument("-work", required=True, help="per-fold scratch directory")
    c.add_argument("-out", required=True)
    c.add_argument("-folds", type=int, required=True)
    c.add_argument("-seed", type=int, required=True)
    c.add_argument("-epochs", type=int, required=True)
    c.add_argument("-batch-size", type=int, required=True)
    c.add_argument("-learning-rate", type=float, required=True)
    c.add_argument("-lora-rank", type=int, required=True)
    c.add_argument("-lora-alpha", type=int, required=True)
    c.add_argument("-max-seq-length", type=int, required=True)
    c.add_argument("-min-recall", type=float, required=True)
    c.add_argument("-exclude-tag", required=True,
                   help="drop cases carrying this tag; 'none' keeps all")
    c.add_argument("-balance", required=True, choices=("yes", "no"),
                   help="oversample the minority class in each training fold")
    c.set_defaults(func=cmd_cv)

    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
