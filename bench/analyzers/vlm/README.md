# Qwen3.8 contact detection on the bench

Fine-tuning a vision-language model on the labeled sentry clips, scored on the
same cases as every other analyzer in `bench/`. The question it answers: the
VLM is bad at this out of the box — is that because the task is genuinely
invisible to it, or because nobody has ever shown it what contact looks like?

This is an exploration, not a production candidate. With 42 labeled cases the
result is a direction, not a number to ship.

## Why this and not the V-JEPA probe

`bench/analyzers/vjepa/` freezes a video encoder and trains a small head on
its features. That path keeps the encoder's notion of "what happened" fixed
and only learns a readout. Fine-tuning moves the model itself, which is a
different bet: a LoRA adapter can change what the model attends to, not just
how its existing features are combined. The two are run against the same
labels and the same scorer so they can be compared directly.

## The model

`Qwen/Qwen3.8-27B` — 27.8 B dense, natively multimodal, Apache-2.0, with a
`video_preprocessor_config.json`, so video is in scope. Two hardware facts
decide everything downstream:

* bf16 weights are ~55 GB. Neither machine here can hold that.
* The RTX 4070 has **12 GB**, so even the 4-bit build (~15 GB) will not fit.
  The GPU box is out for this model regardless of what it is doing.

So the work runs on the M5 MacBook Air's 32 GB of unified memory, against
`lmstudio-community/Qwen3.8-27B-MLX-4bit`, with QLoRA through `mlx-vlm`.
That is not a workaround — it is the only machine in the house that can hold
this model at all.

## What the model is asked

One question, one word:

> Did anything physically touch this car during the clip? [...]
> Answer with exactly one word: yes or no.

The answer is never sampled. `vlmb/infer.py` captures the logits of the first
answer token through a `logits_processors` hook and reduces them to P(yes),
softmaxed over the yes and no token families alone. A sampled word would be a
bare boolean; a probability can be re-thresholded after the fact, which is
what makes the precision/recall trade-off adjustable without re-running the
model.

**Phantom alerts are the expensive error** (Adrien's call), so the reported
operating point is the highest-precision threshold that still reaches a
required recall, and the raw tp/fp/fn/tn counts are always printed with it.

## Layout

| path | what |
| --- | --- |
| `vlmbench.py` | the CLI: `zeroshot`, `cv`, `report` |
| `vlmb/data.py` | reads `bench/dataset.json` — **read-only**, another session owns that file |
| `vlmb/frames.py` | ffmpeg sampling into fixed-count JPEG frames |
| `vlmb/prompt.py` | the question, and the LoRA training rows |
| `vlmb/infer.py` | P(contact) from the yes/no logit |
| `vlmb/sft.py` | fold JSONL + driving `mlx_vlm.lora` |
| `vlmb/cv.py` | stratified folds |
| `vlmb/metrics.py` | AUC, AP, precision-first operating point |
| `work/` | frames, weights, adapters, runs (all gitignored) |

## Two ffmpeg traps

Both cost real time and are worth not rediscovering:

* Tesla repeater clips carry **non-full-range YUV**, which the mjpeg encoder
  refuses outright ("Non full-range YUV is non-standard"). The filter chain
  ends in an explicit `format=yuvj420p`.
* Some clips have **no keyframe in their second half**, so input-side `-ss`
  past that point silently writes no file at all — not an error, just a
  missing frame. Sampling is therefore one linear decode, with a per-frame
  fallback that seeks on the *output* side. 10 of 42 clips need the fallback.

## Running it

```
.venv/bin/python vlmbench.py zeroshot \
  -model work/model/Qwen3.8-27B-MLX-4bit \
  -frames work/frames -n-frames 16 -size 448 \
  -out work/runs/zeroshot.json -min-recall 0.5
```

```
.venv/bin/python vlmbench.py cv \
  -model work/model/Qwen3.8-27B-MLX-4bit \
  -frames work/frames -n-frames 16 -size 448 \
  -work work/cv -out work/runs/cv-lora.json \
  -folds 5 -seed 0 -epochs 3 -batch-size 1 \
  -learning-rate 2e-4 -lora-rank 16 -lora-alpha 32 \
  -max-seq-length 4096 -min-recall 0.5
```

Every flag that changes what the model sees is required rather than
defaulted, so a saved run is never ambiguous about how it was produced.

## Sizing the context

`patch_size` 16 with `merge_size` 2 means one frame costs `H*W/1024` tokens.
Across the 42 clips, at 448 px on the long edge, that is 105-196 tokens per
frame and **1680-3136 image tokens per clip** at 16 frames, plus 93 prompt
tokens and the template's own overhead. `-max-seq-length 4096` clears the
worst case (3336) without reserving memory for a context nothing uses.

## Downloading the weights

Both candidate repos are xet-backed, and **the xet transfer hangs outright for
an unauthenticated client** — the process stays alive burning CPU while the
chunk cache sits at 2 MB for minutes. `HF_HUB_DISABLE_XET=1` falls back to
plain HTTPS, which pulls at 5-8 MB/s. `work/download.py` sets it and enforces
a hard deadline, because macOS ships no `timeout(1)`.

One further fact worth knowing before restarting a download: `hf download`
picks a fresh temp suffix per run, so **killing it discards the partial file**
rather than resuming it.

Not the cause, despite looking like it: the anonymous rate limit. Neither
machine here has an HF token, and the CLI warns about it on every run, but an
unauthenticated pull sustains 8.2 MB/s and a ranged request returns 206 with
no 429 and no retry-after. Both this session and the video-gen session lost
time to xet while blaming the missing token; the warning is loud and the real
fault is silent.

## Results so far (2026-09-04)

| run | AUC | AP | precision-first operating point |
| --- | --- | --- | --- |
| zero-shot, 16 frames @ 448 px | 0.679 | 0.787 | P 0.800 / R 0.571 (tp 16, fp 4, fn 12, tn 10) |
| zero-shot, 8 frames @ 448 px | 0.654 | 0.782 | P 0.781 / R 0.893 (tp 25, fp 7, fn 3, tn 7) |
| LoRA fold 0, held out | 0.450 | - | collapsed, see below |

Halving the frames costs nothing measurable, which is what makes the
fine-tune affordable: 8 frames is 1132 tokens and 83 s/step against 16
frames' 2156 tokens and 167 s/step. The 16-frame trainer peaked at 29.56 GB
of 32 GB and drove the machine into swap; 8 frames peaks at 23.5 GB and does
not.

### The first fine-tune mode-collapsed

Fold 0 trained for 73 min on 27 cases at lr 2e-4, rank 16, 2 epochs. Every
one of its 15 held-out clips then scored between 0.997 and 0.998 — a spread
of 0.001 — with mean P(contact) 0.9971 on contact clips and 0.9972 on
no-contact clips. It had learned to answer "yes" unconditionally. Held-out
AUC 0.450, below chance.

The training set is 67% contact and the target is a single token, so
"always yes" is a strong local optimum and lr 2e-4 over 54 steps walks
straight into it. A training clip went 0.915 -> 0.9903 after **two** steps,
so this model memorises what it is shown almost immediately: any number
measured on training clips is worthless here, and only the held-out folds
mean anything.

Before resuming, in order of suspicion: drop the learning rate to 1e-5-5e-5;
balance or class-weight the 28/14 split; and note that
`--train-on-completions` scores the whole assistant turn, so the trivially
predictable template tokens around the one-word answer dilute the gradient on
the only token that matters.

### Resuming

`cv` reuses any fold whose adapter is already on disk and any fold whose
`scores.json` is already written, so a restart does not redo finished work.
This matters more than it looks: `--steps-per-save` defaults to 100 and a
fold is only 54 steps, so **the trainer never checkpoints mid-fold** — a fold
is all-or-nothing, and the safe places to stop are between folds.

## Final result (2026-09-05): fine-tuning did not help

The collapse was a hyperparameter failure, not the answer. Fixed by dropping
the learning rate 10x to 2e-5 and oversampling the minority class to 17/17 per
fold; held-out scores then spread 0.005-0.991 instead of sitting in a
0.001-wide band. But the corrected run does not beat the base model.

Out-of-fold over the 33 non-holdout cases (26 contact / 7 none), 3 folds,
2 epochs, rank 16, every score from an adapter that never saw that clip:

| model | AUC | AP | precision-first operating point |
| --- | --- | --- | --- |
| LoRA fine-tuned, 8 frames | 0.690 | 0.902 | P 0.938 R 0.577 (tp 15, fp 1, fn 11, tn 6) |
| zero-shot, 8 frames | 0.687 | 0.879 | P 0.889 R 0.615 (tp 16, fp 2, fn 10, tn 5) |
| zero-shot, 16 frames | 0.723 | 0.904 | P 0.900 R 0.692 (tp 18, fp 2, fn 8, tn 5) |

**+0.003 AUC over its matched baseline.** That is noise at n=33, where one
case moves AUC by ~0.03. Four and a half hours of training bought nothing,
and the fine-tuned model still loses to the base model given twice the
frames — which costs nothing but inference time.

### What it actually learned

Mean score over the same cases went from 0.477 zero-shot to 0.760 tuned. It
shifted almost everything upward by 0.5-0.9 without reordering much, and AUC
is rank-based, so it barely moved. The largest single shift is the tell:

    pi-2026-09-02_05-48-07   0.085 -> 0.967   label: NO CONTACT

A clean, confident rejection became a confident false positive. Real contacts
moved the same way (`rd-1hozob2` 0.202 -> 0.999, `ghost-cart` 0.294 -> 0.971),
and so did the phantoms (`rd-g1dq6a` 0.407 -> 0.924, `rd-hmaafr` 0.349 ->
0.835). The adapter learned to answer "yes" more loudly, not to see contact.

That also destroys the one thing this model was good for. Its only edge over
the V-JEPA probe was confidently rejecting close-bystander clips; the tuned
model rejects almost nothing (mean 0.760), so it cannot serve as a veto.

### The honest conclusion

LoRA on 33 clips with a one-word target moves the model's confidence around
without teaching it anything new about contact. This is what a 27B model
memorising 34 training rows looks like — a training clip went 0.915 -> 0.9903
after **two** steps. The bottleneck is labeled data, not adapter
hyperparameters, and the next thing worth trying is not another sweep.

If the line is picked up again: 16 frames beat 8 by 0.036 AUC zero-shot, so a
fine-tune should run at 16 frames — which needs a machine with more than
32 GB, since 16 frames peaks at 29.56 GB here and swaps.

## Motion-guided sampling (built, not conclusively tested)

The post-mortem on why fine-tuning failed points at the input, not the model.
Uniform sampling places frames by clock time, but contact is a ~2 s event in a
median 28.7 s clip, so 8 uniform frames land inside a labeled contact window
**0.56 times on average**. Measured on the four cases that carry
`start_seconds`/`end_seconds`:

| sampling | frames inside the contact window | clips with zero coverage |
| --- | --- | --- |
| uniform, 8 frames | 4 / 32 | 1 (`sedan-door-swing`) |
| motion, 8 frames | 8 / 32 | 0 |
| uniform, 16 frames | 9 / 64 | 0 |
| motion, 16 frames | 12 / 64 | 0 |

`vlmb/motion.py` decodes each clip once at 96x64 gray, takes the absolute
difference between consecutive frames, discards hard scene cuts (a cut in
reposted footage dwarfs real motion and would otherwise take the whole frame
budget), and places frames at equal quantiles of cumulative motion with 30% of
the mass held uniform so a still clip still gets coverage. `-sampling motion`
selects it.

**Status: inconclusive, and stopped deliberately.** The validating zero-shot
run was killed at Adrien's request 28 of 48 cases in. On those 28 — paired,
same cases both ways — motion scored AUC 0.953 against uniform's 0.938. That
subset holds only 4 negatives, so both numbers are inflated and a 0.015 gap is
noise; it is not evidence. The per-case movement is the more interesting part
and points the right way: the three clips that rose most are hand-scale
contacts, the exact class both this model and the V-JEPA probe miss
(`rd-1baaqtc` 0.269 -> 0.593, `rd-1j18tgo` 0.593 -> 0.818, `rd-1jep0z1`
Ventura 0.119 -> 0.294). Three others fell.

To settle it, run the full 48 both ways and compare. Frames are already cached
in `work/frames/16f-448px-motion/`.

## Reading the numbers honestly

42 cases, 28 of them contact. Fourteen negatives is few enough that **one
false positive moves precision by several points**, so:

* folds are stratified and each fold is scored only by an adapter that never
  saw it;
* AUC and AP are the headline because they need no threshold;
* the operating point is reported with its raw counts, never as a bare
  percentage.

A difference of a couple of points between two configurations at this sample
size is noise. Only a large gap means anything yet.
