import json
from pathlib import Path
import sys
import pytest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from vjb import store


def test_unknown_can_encode_but_cannot_train(tmp_path):
    p=tmp_path/"dataset.json"
    p.write_text(json.dumps({"version":1,"cases":[
        {"id":"unknown","clips":["a.mp4"],"label":None},
        {"id":"negative","clips":["b.mp4"],"label":{"contact":False}}]}))
    encoded=store.encoding_cases(p)
    assert [c["contact"] for c in encoded]==[None,False]
    assert [c["id"] for c in store.dataset_cases(p)]==["negative"]


def test_all_unknown_training_fails(tmp_path):
    p=tmp_path/"dataset.json"
    p.write_text(json.dumps({"version":1,"cases":[{"id":"u","clips":["a.mp4"],"label":None}]}))
    assert store.encoding_cases(p)[0]["contact"] is None
    with pytest.raises(ValueError,match="no labeled cases"):
        store.dataset_cases(p)
