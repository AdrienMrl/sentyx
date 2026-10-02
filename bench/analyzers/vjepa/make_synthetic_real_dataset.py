#!/usr/bin/env python3
"""Build a V-JEPA manifest with synthetic training and real-only evaluation.

The source datasets and clips are never modified.  Clip paths in the derived
manifest are absolute so it can live beside an experiment cache without
copying what may be hundreds of videos.
"""
import argparse
import copy
import json
import os


def load(path):
    with open(path) as f:
        value = json.load(f)
    if value.get("version") != 1:
        raise ValueError(f"{path}: expected dataset version 1")
    return value


def clip_path(dataset_path, case):
    if len(case.get("clips", [])) != 1:
        raise ValueError(f"{case.get('id')}: expected exactly one clip")
    clip = case["clips"][0]
    if os.path.isabs(clip):
        return clip
    return os.path.join(os.path.dirname(os.path.abspath(dataset_path)), "clips", case["id"], clip)


def build(synthetic_path, real_path, derivation="h264crf20"):
    synthetic, real = load(synthetic_path), load(real_path)
    cases = []
    counts = {"synthetic": 0, "real": 0}
    seen = set()
    for source, path, dataset in (("synthetic", synthetic_path, synthetic), ("real", real_path, real)):
        for original in dataset["cases"]:
            tags = list(original.get("tags") or [])
            if source == "synthetic" and derivation not in tags:
                continue
            case = copy.deepcopy(original)
            if case["id"] in seen:
                raise ValueError(f"duplicate case id {case['id']!r}")
            seen.add(case["id"])
            clip = os.path.abspath(clip_path(path, case))
            if not os.path.isfile(clip):
                raise FileNotFoundError(clip)
            case["clips"] = [clip]
            tags = [t for t in tags if t != "holdout"]
            tags += ["synthetic-train"] if source == "synthetic" else ["holdout", "real-eval"]
            case["tags"] = list(dict.fromkeys(tags))
            cases.append(case)
            counts[source] += 1
    if not counts["synthetic"] or not counts["real"]:
        raise ValueError(f"need both synthetic and real cases, got {counts}")
    return {"version": 1, "cases": cases}, counts


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--synthetic", required=True, help="exported synthetic dataset.json")
    p.add_argument("--real", required=True, help="real benchmark dataset.json")
    p.add_argument("--out", required=True)
    p.add_argument("--derivation", default="h264crf20")
    a = p.parse_args()
    dataset, counts = build(a.synthetic, a.real, a.derivation)
    os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)
    with open(a.out, "w") as f:
        json.dump(dataset, f, indent=1)
        f.write("\n")
    print(json.dumps({"out": os.path.abspath(a.out), "counts": counts}, indent=1))


if __name__ == "__main__":
    main()
