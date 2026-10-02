"""Tabulate train-*.json outputs: one row per run, with the numbers the bench
cares about and the within-source AUC. Usage: python report.py <json>..."""
import json
import os
import sys


def row(path):
    s = json.load(open(path))
    web = s.get("auc_by_source", {}).get("web", {})
    samp = s.get("sampling", {})
    return (os.path.relpath(path), s.get("head"), f"{samp.get('fps')}fps/f{samp.get('frames')}", s["positives"] + s["negatives"],
            f"{s['positives'] - s['contact_missed']}/{s['positives']}", f"{s['contact_phantom']}/{s['negatives']}",
            "n/a" if s["auc"] is None else f"{s['auc']:.2f}", "n/a" if web.get("auc") is None else f"{web['auc']:.2f}",
            s.get("loc_hits") or "-", s.get("phantoms_at_zero_miss"))


hdr = ("file", "head", "sampling", "n", "found", "phantom", "AUC", "AUC web", "loc", "ph@0miss")
rows = [row(p) for p in sys.argv[1:]]
w = [max(len(str(r[i])) for r in rows + [hdr]) for i in range(len(hdr))]
for r in [hdr] + rows:
    print("  ".join(str(v).ljust(w[i]) for i, v in enumerate(r)))
