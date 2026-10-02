from pathlib import Path
import sys
from unittest.mock import patch
import numpy as np
import pytest
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from vjb.store import save_features


def test_failed_write_keeps_previous_cache(tmp_path):
    path=tmp_path/"clip.npz"
    tokens=np.zeros((1,2,2,2,3))
    save_features(str(path),tokens,[0],[4],4)
    original=path.read_bytes()
    def fail(f,**kwargs):
        f.write(b"partial archive")
        raise OSError("simulated interruption")
    with patch("vjb.store.np.savez",fail),pytest.raises(OSError):
        save_features(str(path),tokens,[0],[4],4)
    assert path.read_bytes()==original
    assert list(tmp_path.iterdir())==[path]


def test_successful_atomic_write_loads(tmp_path):
    path=tmp_path/"clip.npz"
    save_features(str(path),np.ones((1,2,2,2,3)),[0],[4],4)
    with np.load(path) as data:
        assert data["tokens"].dtype==np.float16
        assert data["tokens"].sum()==24
