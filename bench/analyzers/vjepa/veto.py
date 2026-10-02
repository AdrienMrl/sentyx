"""Pre-registered ensemble rule (written 2026-09-04 before the 48-case scores
were compared): alert iff vjepa >= t1 AND vlm >= t2. The VLM only vetoes.

Fitting: t1 and t2 chosen on non-holdout cases only, t2 constrained to
[0.02, 0.15] (range chosen after seeing the VLM's 42-case score curve, which is
declared as such), objective = fewest phantoms subject to at most one more
miss than vjepa alone at t1=0.5. Evaluation on holdout happens once.

Usage: python veto.py <train-json> <vlm-percase-json> <dataset.json>
"""
import json
import sys

import numpy as np

train, vlm, ds = (json.load(open(p)) for p in sys.argv[1:4])
holdout = {c["id"] for c in ds["cases"] if "holdout" in c.get("tags", [])}
rows = [(r["case"], bool(r["label"]), r["score"], vlm[r["case"]]) for r in train["cases"] if r["case"] in vlm]
dev = [r for r in rows if r[0] not in holdout]
test = [r for r in rows if r[0] in holdout]


def counts(rs, t1, t2):
    miss = sum(1 for _, l, a, b in rs if l and not (a >= t1 and b >= t2))
    ph = sum(1 for _, l, a, b in rs if not l and (a >= t1 and b >= t2))
    return miss, ph


base_miss, base_ph = counts(dev, 0.5, -1)
best = None
for t1 in np.arange(0.3, 0.96, 0.05):
    for t2 in np.arange(0.02, 0.151, 0.01):
        miss, ph = counts(dev, t1, t2)
        if miss <= base_miss + 1 and (best is None or (ph, miss) < (best[0], best[1])):
            best = (ph, miss, float(t1), float(t2))
ph, miss, t1, t2 = best
print(f"dev (n={len(dev)}): vjepa alone @0.5 miss {base_miss} phantom {base_ph}; veto t1={t1:.2f} t2={t2:.2f} -> miss {miss} phantom {ph}")
if test:
    print(f"holdout (n={len(test)}): vjepa alone @0.5 miss/phantom {counts(test, 0.5, -1)}; veto -> {counts(test, t1, t2)}")
    for cid, l, a, b in test:
        print(f"   {'C' if l else '-'} vjepa {a:.3f} vlm {b:.3f} alert={a >= t1 and b >= t2}  {cid[:50]}")
