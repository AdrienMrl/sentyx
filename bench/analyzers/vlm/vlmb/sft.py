"""Building LoRA training data and driving `mlx_vlm.lora`.

The trainer reads a Hugging Face dataset with an `images` column and a
`messages` column, so a fold is written out as JSONL holding the frame paths
and the same prompt used at inference, answered with the label. Training and
scoring therefore see byte-identical prompts; if they drifted, the adapter
would be learning to answer a question the benchmark never asks.

Only the assistant's one-word answer carries loss (`--train-on-completions`),
so the model is never rewarded for reproducing the prompt.
"""

import json
import os
import random
import subprocess
import sys

from . import prompt as P
from .frames import frames_for


def write_fold(cases, indices, frames_root, n_frames, size, out_dir,
               balance=False, seed=0):
    """One fold's training rows as a JSONL dataset directory.

    With `balance`, the minority class is oversampled to the size of the
    majority. The set is 26 contact against 7 no-contact once the shared
    holdout is removed, and an unbalanced run at that ratio already collapsed
    once: the model learned to answer "yes" unconditionally and scored every
    held-out clip 0.997. Oversampling rather than downsampling because
    discarding positives would leave ~14 training rows in total.
    """
    os.makedirs(out_dir, exist_ok=True)
    rows = list(indices)
    if balance:
        pos = [i for i in indices if cases[i].contact]
        neg = [i for i in indices if not cases[i].contact]
        minority, majority = (neg, pos) if len(neg) < len(pos) else (pos, neg)
        if minority:
            rng = random.Random(seed)
            grown = list(minority)
            while len(grown) < len(majority):
                grown.append(rng.choice(minority))
            rows = majority + grown
            rng.shuffle(rows)
    path = os.path.join(out_dir, "train.jsonl")
    with open(path, "w") as fh:
        for i in rows:
            case = cases[i]
            paths = frames_for(case, frames_root, n_frames, size)
            fh.write(json.dumps({
                "images": [os.path.abspath(p) for p in paths],
                "messages": P.training_messages(len(paths), case.contact),
            }) + "\n")
    return path


def train_adapter(python, model_path, dataset_dir, out_dir, *,
                  epochs, batch_size, learning_rate, lora_rank, lora_alpha,
                  max_seq_length, log_path, grad_checkpoint=True,
                  gradient_accumulation_steps=1):
    """Run mlx_vlm.lora as a subprocess and tee its log.

    A subprocess rather than an in-process call because the trainer allocates
    the whole 27B model; letting it exit reclaims the memory cleanly before
    the next fold loads its own copy.

    The adapter is written as `<out_dir>/adapters.safetensors`, and that exact
    layout matters: `save_adapter` drops `adapter_config.json` beside the
    weights, and `apply_lora_layers` — the loader behind `load(adapter_path=)`
    — takes the *directory* and reads both files by hard-coded name. Passing a
    bare `.safetensors` file to either side silently produces a training run
    whose result cannot be loaded back.
    """
    os.makedirs(out_dir, exist_ok=True)
    out_adapter = os.path.join(out_dir, "adapters.safetensors")
    cmd = [
        python, "-m", "mlx_vlm.lora",
        "--model-path", model_path,
        "--dataset", dataset_dir,
        "--epochs", str(epochs),
        "--batch-size", str(batch_size),
        "--learning-rate", str(learning_rate),
        "--lora-rank", str(lora_rank),
        "--lora-alpha", str(lora_alpha),
        "--max-seq-length", str(max_seq_length),
        "--gradient-accumulation-steps", str(gradient_accumulation_steps),
        "--train-on-completions",
        "--steps-per-report", "1",
        "--output-path", out_adapter,
    ]
    if grad_checkpoint:
        cmd.append("--grad-checkpoint")
    with open(log_path, "w") as log:
        log.write(" ".join(cmd) + "\n\n")
        log.flush()
        proc = subprocess.run(cmd, stdout=log, stderr=subprocess.STDOUT, text=True)
    if proc.returncode == 0:
        missing = [f for f in ("adapters.safetensors", "adapter_config.json")
                   if not os.path.exists(os.path.join(out_dir, f))]
        if missing:
            raise RuntimeError(
                f"training reported success but {out_dir} is missing {missing}; "
                "the adapter would not load back"
            )
    return proc.returncode, cmd
