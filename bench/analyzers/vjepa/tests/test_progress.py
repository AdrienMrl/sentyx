import json

from vjb.progress import Writer


def test_progress_is_machine_readable_and_completes(tmp_path):
    path = tmp_path / "progress.json"
    progress = Writer(str(path), "train", total=3, completed=0)
    progress.write(completed=2, current_case="case-2")
    assert json.loads(path.read_text())["current_case"] == "case-2"
    progress.complete(completed=3, metrics={"auc": 0.9})
    value = json.loads(path.read_text())
    assert value["state"] == "complete"
    assert value["completed"] == value["total"] == 3
    assert value["metrics"]["auc"] == 0.9
