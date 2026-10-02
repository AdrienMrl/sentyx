#!/usr/bin/env python3
"""Matched-pair ordering from a `train` summary.

A matched pair is two clips of the same staged action that differ only in
whether contact occurs. The question is whether the model scores the contact
member above its near-miss partner. This reports the per-pair difference, the
fraction correctly ordered, and the breakdown by family and camera. It reads
the -out JSON from `vjepa_bench.py train`.
"""
import argparse
import json
import os
import sys


def pair_id(case_id, dataset):
    """The shared identity of the two members of a matched pair."""
    for c in dataset["cases"]:
        if c["id"] == case_id:
            for tag in c.get("tags", []):
                if tag.startswith("pair:"):
                    return tag.split(":", 1)[1]
            for tag in c.get("tags", []):
                if tag.startswith("real-group:"):
                    return tag.split(":", 1)[1]
    return None


def family_of(case_id, dataset):
    for c in dataset["cases"]:
        if c["id"] == case_id:
            for tag in c.get("tags", []):
                if tag.startswith("camera:"):
                    return tag.split(":", 1)[1]
    return "?"


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("summary", help="train -out JSON")
    p.add_argument("-dataset", required=True, help="the paired dataset.json used for training")
    p.add_argument("-out", default="", help="also write the pair table here")
    a = p.parse_args()

    summary = json.load(open(a.summary))
    dataset = json.load(open(a.dataset))
    scores = {r["case"]: float(r["score"]) for r in summary["cases"]}

    pairs = {}
    for case_id, score in scores.items():
        pid = pair_id(case_id, dataset)
        if pid is None:
            continue
        entry = pairs.setdefault(pid, {"camera": family_of(case_id, dataset)})
        if case_id.endswith("door_ding") or case_id.endswith("backing_impact"):
            entry["contact"] = score
            entry["kind"] = case_id.rsplit("-", 1)[-1]
        else:
            entry["near_miss"] = score

    complete = {p: v for p, v in pairs.items() if "contact" in v and "near_miss" in v}
    rows = []
    for pid, v in sorted(complete.items()):
        d = v["contact"] - v["near_miss"]
        rows.append({"pair": pid, "camera": v["camera"], "contact": round(v["contact"], 4),
                     "near_miss": round(v["near_miss"], 4), "diff": round(d, 4), "ordered": d > 0})
    correct = sum(r["ordered"] for r in rows)
    print(f"pairs evaluated: {len(rows)} / {len(pairs)}")
    if rows:
        print(f"contact scored above near-miss: {correct}/{len(rows)} ({correct/len(rows):.0%})")
        print(f"mean score difference (contact - near-miss): "
              f"{sum(r['diff'] for r in rows)/len(rows):+.4f}")
        by_cam = {}
        for r in rows:
            b = by_cam.setdefault(r["camera"], [0, 0])
            b[0] += 1
            b[1] += 1 if r["ordered"] else 0
        for cam, (n, c) in sorted(by_cam.items()):
            print(f"  camera {cam}: {c}/{n} ordered")
        print("per pair:")
        for r in rows:
            flag = "" if r["ordered"] else "  <- REVERSED"
            print(f"  {r['pair']:<14} contact={r['contact']:.3f} near_miss={r['near_miss']:.3f} "
                  f"diff={r['diff']:+.3f}{flag}")
    if a.out:
        json.dump(rows, open(a.out, "w"), indent=1)
    return 0 if rows else 1


if __name__ == "__main__":
    sys.exit(main())
