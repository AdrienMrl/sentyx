import importlib.util
import json
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "make_synthetic_real_dataset.py"
spec = importlib.util.spec_from_file_location("make_synthetic_real_dataset", SCRIPT)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


def write_dataset(root, cases):
    root.mkdir()
    (root / "clips").mkdir()
    for case in cases:
        d = root / "clips" / case["id"]
        d.mkdir()
        (d / case["clips"][0]).write_bytes(b"video")
    path = root / "dataset.json"
    path.write_text(json.dumps({"version": 1, "cases": cases}))
    return path


def test_synthetic_trains_and_every_real_case_is_holdout(tmp_path):
    syn = write_dataset(tmp_path / "syn", [
        {"id": "native", "clips": ["a.mp4"], "tags": ["synthetic", "h264crf20"], "label": {"contact": True}},
        {"id": "pi-copy", "clips": ["b.mp4"], "tags": ["synthetic", "pi3fps640"], "label": {"contact": True}},
    ])
    real = write_dataset(tmp_path / "real", [
        {"id": "field", "clips": ["c.mp4"], "tags": ["field"], "label": {"contact": False}},
        {"id": "web", "clips": ["d.mp4"], "tags": ["holdout"], "label": {"contact": True}},
    ])
    ds, counts = mod.build(str(syn), str(real))
    assert counts == {"synthetic": 1, "real": 2}
    rows = {c["id"]: c for c in ds["cases"]}
    assert set(rows) == {"native", "field", "web"}
    assert "holdout" not in rows["native"]["tags"]
    assert all("holdout" in rows[x]["tags"] for x in ("field", "web"))
    assert all(Path(c["clips"][0]).is_absolute() for c in rows.values())
