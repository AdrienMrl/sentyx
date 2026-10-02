#!/usr/bin/env python3
"""Emit one dashboard snapshot. Runs locally on the Windows training host."""

import argparse
import glob
import json
import os
import subprocess
import time


def read(path):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def result_row(path):
    value = read(path)
    if not value or "cases" not in value:
        return None
    return {
        "file": os.path.relpath(path, os.path.dirname(os.path.dirname(path))),
        "updated_unix": os.path.getmtime(path),
        "head": value.get("head"),
        "auc": value.get("auc"),
        "missed": value.get("contact_missed"),
        "phantoms": value.get("contact_phantom"),
        "positives": value.get("positives"),
        "negatives": value.get("negatives"),
        "loc_hits": value.get("loc_hits"),
        "sampling": value.get("sampling", {}),
        "notes": value.get("notes", ""),
    }


def main():
    p = argparse.ArgumentParser()
    p.add_argument("root", help="bench/features directory")
    p.add_argument("--progress", default="vjepa-dashboard/progress.json")
    a = p.parse_args()
    root = os.path.abspath(a.root)
    results = []
    for pattern in ("**/train-*.json", "**/finetune-*.json", "**/evaluate-*.json"):
        for path in glob.glob(os.path.join(root, pattern), recursive=True):
            row = result_row(path)
            if row:
                results.append(row)
    results.sort(key=lambda x: x["updated_unix"], reverse=True)
    gpu = None
    try:
        line = subprocess.check_output([
            "nvidia-smi", "--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
            "--format=csv,noheader,nounits"], text=True, timeout=3).strip().splitlines()[0]
        name, util, used, total, temp, power = [x.strip() for x in line.split(",")]
        gpu = {"name": name, "util": float(util), "memory_used_mb": float(used),
               "memory_total_mb": float(total), "temperature_c": float(temp), "power_w": float(power)}
    except Exception:
        pass
    print(json.dumps({"time_unix": time.time(), "progress": read(os.path.join(root, a.progress)),
                      "gpu": gpu, "results": results[:30]}))


if __name__ == "__main__":
    main()
