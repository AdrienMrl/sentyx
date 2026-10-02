"""Loading the labeled bench cases.

`bench/dataset.json` is Adrien's hand-labeled ground truth and another session
is actively editing it, so this module only ever reads it.
"""

import json
import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Case:
    id: str
    clip: str
    contact: bool
    threat: str
    start_seconds: float | None
    end_seconds: float | None
    tags: tuple[str, ...] = ()


def repo_root() -> str:
    here = os.path.dirname(os.path.abspath(__file__))
    while here != "/":
        if os.path.isdir(os.path.join(here, ".git")):
            return here
        here = os.path.dirname(here)
    raise RuntimeError("not inside the repo")


def load_cases(dataset_path=None, clips_root=None, require_files=True,
               exclude_tag=None):
    """Every case carrying a contact label, with its clip resolved on disk.

    A case with several clips contributes only its first: the dataset's clip
    list is ordered the way production ranked the cameras, so the first entry
    is the angle the analyzer would actually have been shown.

    `exclude_tag` drops every case carrying that tag. It exists for the shared
    `holdout` tag: the V-JEPA session and this one agreed a locked split, and
    a fine-tune that trains on a holdout case silently destroys both sessions'
    ability to compare anything on it.
    """
    root = repo_root()
    dataset_path = dataset_path or os.path.join(root, "bench", "dataset.json")
    clips_root = clips_root or os.path.join(root, "bench", "clips")

    with open(dataset_path) as fh:
        raw = json.load(fh)

    cases, missing = [], []
    for c in raw["cases"]:
        label = c.get("label") or {}
        if label.get("contact") is None:
            continue
        if not c.get("clips"):
            continue
        tags = tuple(c.get("tags") or ())
        if exclude_tag is not None and exclude_tag in tags:
            continue
        clip = os.path.join(clips_root, c["id"], c["clips"][0])
        if not os.path.exists(clip):
            missing.append(clip)
            continue
        cases.append(
            Case(
                id=c["id"],
                clip=clip,
                contact=bool(label["contact"]),
                threat=label.get("threat") or "none",
                start_seconds=label.get("start_seconds"),
                end_seconds=label.get("end_seconds"),
                tags=tags,
            )
        )
    if missing and require_files:
        raise FileNotFoundError(
            f"{len(missing)} labeled clips are not on disk, first: {missing[0]}"
        )
    return cases


def summary(cases):
    pos = sum(1 for c in cases if c.contact)
    return f"{len(cases)} labeled cases: {pos} contact, {len(cases) - pos} no-contact"
