# V-JEPA 2 contact detection on the bench

An experiment that replaces the VLM with a frozen V-JEPA 2 video encoder and
a small trainable head, scored on the same labeled clips as every other
analyzer in `bench/`. The question it answers: can a latent-prediction video
encoder plus a head trained on a handful of clips see the touches the language
models miss, and at what phantom rate.

The design and the reasoning behind it are in the "Seeing Contact" notes
(chapter 4). In short: the encoder never trains, every clip is encoded once
into a token cache, and heads with well under a million parameters train in
seconds to minutes on the cache, so cross-validation over every case is cheap.

## Layout

| path | what |
| --- | --- |
| `vjepa_bench.py` | the CLI: `encode`, `train`, `finetune`, `analyze` |
| `vjb/frames.py` | ffmpeg sampling into square uint8 frames; window/stride logic |
| `vjb/encoder.py` | `VJEPA2Encoder` (Hugging Face) and `FakeEncoder` (pixel statistics, for plumbing) |
| `vjb/store.py` | the feature cache format and `bench/dataset.json` loading |
| `vjb/heads.py` | `meanpool`, `probe` (attentive probe), `temporal`, `motion`, `region` heads |
| `vjb/train.py` | leave-one-case-out CV, loss, metrics, head save/load |
| `vjb/finetune.py` | fine-tuning the last K encoder blocks with the head, from frames |
| `vjb/analyze.py` | the bench `-analyzer` verdict |
| `tests/` | end-to-end plumbing on synthetic clips, no weights needed |
| `bench/features/<cache>/` | token caches and training summaries (gitignored) |

## Live dashboard

The dashboard runs on the Mac and reads progress, completed results, and GPU
telemetry from the Windows training box over SSH:

```
python dashboard.py
open http://127.0.0.1:8765
```

Pass `-progress ../../features/vjepa-dashboard/progress.json` to `encode` and
`train`, or set `VJEPA_PROGRESS` to that path once in the training shell. Both
commands replace the status file atomically, so the dashboard never observes
a partly written update. The default host is the LAN render/training PC in
`AGENTS.md`; `--target` and `--remote-repo` override it.

## Setup

torch has no Python 3.14 wheels yet, so the environment pins 3.12:

```
cd bench/analyzers/vjepa
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python -r requirements.txt
.venv/bin/python -m pytest -q tests        # ~15 s, fake encoder, no downloads
```

ffmpeg is taken from PATH, falling back to the binary bundled with
imageio-ffmpeg. The first `encode` with a real checkpoint downloads it from the
hub (ViT-L is 1.3 GB).

## V-JEPA 2.1

Meta released V-JEPA 2.1 on 2026-03-16 (arXiv 2603.14482): same ViT
family, trained at 384 px with a dense predictive loss and deep supervision
aimed at "temporally consistent dense features". Checkpoints are only on
`dl.fbaipublicfiles.com/vjepa2/` (no official transformers port), so the
`vjepa21` encoder loads them through Meta's own code, vendored under
`third_party/vjepa2` (gitignored; `git clone --depth 1
https://github.com/facebookresearch/vjepa2 third_party/vjepa2`, plus
`timm` and `einops` in the venv):

```
curl -L -o ~/.cache/vjepa2/vjepa2_1_vitl_dist_vitG_384.pt \
  https://dl.fbaipublicfiles.com/vjepa2/vjepa2_1_vitl_dist_vitG_384.pt
.venv/bin/python vjepa_bench.py encode \
  -cache ../../features/vjepa21-vitl-384-3fps-pi-f16s4 \
  -encoder vjepa21 -model ~/.cache/vjepa2/vjepa2_1_vitl_dist_vitG_384.pt \
  -arch vjepa2_1_vit_large_384 -src third_party/vjepa2 -device mps -dtype float32 \
  -fps 3 -size 384 -crop squash -frames 16 -stride 4 -match pi -flip
```

Archs: `vjepa2_1_vit_base_384` (80M), `vjepa2_1_vit_large_384` (300M,
distilled from the 2B model), `vjepa2_1_vit_giant_384` (1B),
`vjepa2_1_vit_gigantic_384` (2B). Meta's rotary attention upcasts q/k and
refused half precision until `third_party/vjepa2/.../utils/modules.py` got a
two-line cast before both SDPA calls (see `windows-gpu.md`); with it,
float16 on mps matches float32 to cosine 0.9998 at 3x the speed (0.9 s vs
2.6 s per 16-frame window on a base M5, which then throttles when fanless).
Use bfloat16 on cuda. The GPU-box recipe is in `windows-gpu.md`. `analyze` on a
2.1 head needs `-model` and `-src` again to rebuild the encoder.

## 1. Encode

Every flag that changes what the encoder sees is required and recorded in the
cache's `meta.json`; a cache refuses to be extended with a different
configuration. One cache per configuration, named for it:

```
.venv/bin/python vjepa_bench.py encode \
  -cache ../../features/vjepa2-vitl-256-8fps-full-f16 \
  -encoder vjepa2 -model facebook/vjepa2-vitl-fpc64-256 -device cuda -dtype bfloat16 \
  -fps 3 -size 256 -crop squash -frames 16 -stride 4 -match pi -flip
```

- `-match pi`: push every clip through the transcode the in-car unit applies
  before upload (`internal/videocompress`: 3 fps, 640 px wide, 64-300 kb/s).
  Field clips already arrive like that and are left alone; downloaded clips
  are 30 fps at a higher bitrate, and without this step frame rate and
  compression alone separate the two sources. `none` disables it, and is only
  honest when every clip has the same provenance.
- `-fps` should equal the matched frame rate (3 with `pi`); sampling faster
  than the source duplicates frames.

- `-model`: `facebook/vjepa2-vitl-fpc64-256` (300M), `vjepa2-vith-fpc64-256`,
  `vjepa2-vitg-fpc64-256`, `vjepa2-vitg-fpc64-384` (1B, 384 px). `-size` must
  equal the checkpoint's crop size. V-JEPA 2.1 checkpoints were not on the hub
  under the `vjepa2` search as of 2026-09-04; when they appear they should
  load through the same class.
- `-crop`: `squash` squashes the whole frame to a square and keeps every pixel
  (the ego body sits at the frame edge, which `center` would cut off);
  `bottom` keeps the bottom 55% of the height at full width and squashes that
  to the square instead, doubling the pixels on the contact zone -- in Sentry
  footage the car's own body edge runs along the bottom of every camera view,
  so hand-scale contact is always in the lower band; `band:A,B` is the same
  idea with the fractions chosen by hand (`band:0.45,1.0` is `bottom`).
  `full` is the deprecated name of `squash` and still works, with a note on
  stderr, so caches whose `meta.json` says `full` keep loading.
- `-frames`/`-stride`: a window of 16 frames at 8 fps is 2 s; stride 8 gives
  half-overlap. The checkpoint's `fpc64` is what it was pretrained with, not a
  constraint; 64-frame windows cost 4x the tokens per window.
- `-flip`: also caches a horizontally flipped copy, used only for training.
- `-case id,id` narrows to some cases; `-force` re-encodes.

Cost on an M-series Mac with `-device mps`: ViT-L at 256 px, 16-frame
windows, about 1 s per window; the 11 bench cases (421 windows) took under
10 minutes. Each 16-frame window is 8 x 16 x 16 x 1024 float16 = 4 MB, so the
bench cache is 1.7 GB without flips. Encoding is the only expensive step.

## 2. Train and cross-validate

```
.venv/bin/python vjepa_bench.py train \
  -cache ../../features/vjepa2-vitl-256-8fps-full-f16 \
  -head temporal -seeds 3 -flips \
  -out ../../features/vjepa2-vitl-256-8fps-full-f16/train-temporal.json \
  -save-head ../../features/vjepa2-vitl-256-8fps-full-f16/temporal.pt \
  -notes "what this run is testing"
```

Leave-one-case-out: for each case, a head is trained on all the others (and
their flipped copies, with `-flips`) and scored on the held-out case, repeated
per seed. The report is per case, then contact missed, contact phantom, and a
rank AUC, plus the threshold that would catch every positive and what it
costs in phantoms. With five positives the numbers are noisy; the seed spread
column says how much.

Heads:

- `meanpool`: logistic regression on the mean token of a window. The floor.
- `probe`: the attentive probe from the V-JEPA papers, one logit per window,
  clip logit is the max. The paper's own evaluation recipe.
- `temporal`: tokens pooled per timestep, dilated 1-D convolutions over time,
  one logit per timestep. Also reports where in the clip the contact is.
- `motion`: `temporal` with the same per-timestep pooling applied to the
  temporal difference of the token grid as well (mean, max, diff-mean,
  diff-max). Spatial mean/max over 24x24 tokens washes out a hand-sized
  change; the difference grid is near zero wherever the scene is still, so the
  motion discontinuity survives the pooling. Localizes.
- `region`: keeps coarse spatial structure. The token grid is averaged into
  4x4 regions, each squeezed by a shared `Linear(D, 64)`, and the flattened
  regions feed the same temporal stack, so a change at the car body is
  separable from a change elsewhere in the frame. Localizes.

Labels: a case with `start_seconds`/`end_seconds` trains window by window
(windows overlapping the interval are positive). A case with only `contact`
trains by multiple-instance learning, where the clip's logit is a soft max
over its windows. Staged clips should carry the interval; it is a much
stronger signal. Feature-space augmentation (temporal crop, token dropout,
noise) and the loss weights are flags with defaults that are recorded in the
summary JSON.

`-save-head` fits one more head on every case and writes it with the cache
metadata and threshold, for `analyze`. A head trained on all bench cases is
only meaningful on clips outside them.

## 2b. Fine-tune the last blocks (`finetune`)

`encode` + `train` keep the encoder frozen. When the head plateaus there — and
it does, because the frozen features encode *proximity* rather than *contact* —
`finetune` trains the last K transformer blocks jointly with the head, end to
end from frames:

```
.venv\Scripts\python.exe vjepa_bench.py finetune `
  -cache ..\..\features\ft-k4 `
  -encoder vjepa21 -model $env:USERPROFILE\.cache\vjepa2\vjepa2_1_vitl_dist_vitG_384.pt `
  -arch vjepa2_1_vit_large_384 -src third_party\vjepa2 -device cuda -dtype bfloat16 `
  -fps 15 -size 384 -crop squash -frames 64 -stride 16 -match none `
  -unfreeze 4 -epochs 8 -lr 1e-3 -lr-encoder 1e-5 -split holdout `
  -max-windows-per-clip 8 -accum 8 -flip `
  -out ..\..\features\ft-k4\finetune-k4.json `
  -save-encoder ..\..\features\ft-k4\ft-k4.pt `
  -notes "K=4, 64f windows, no domain matching"
```

The design in one line: nothing below block `depth - K` ever changes, so the
first epoch writes each window's hidden state at the split to
`-cache/<case>/ft-fps..-size..-f..-s..-k<K>/<orig|flip>/wNNNNN.npy` as fp16 and
every later epoch reads it back and runs only the K trainable blocks. `-cache
none` disables the cache and recomputes the frozen trunk every epoch.

- `-unfreeze 0` is head-only and reproduces the frozen path exactly (asserted
  in `tests/test_finetune.py`), so it is the control the K > 0 runs are read
  against.
- `-split holdout` trains on the cases without the `holdout` tag and evaluates
  on the ones with it. `-split loo` is leave-one-case-out and retrains the tail
  once per case — supported, but it costs `len(cases)` times as much and the
  command warns.
- `-max-windows-per-clip` samples a random subset of each clip's windows per
  epoch, biasing half the budget toward the labeled interval when the case has
  `start_seconds`/`end_seconds`. Without it a 60 s clip with a 2 s touch trains
  almost entirely on its own negatives.
- The loss is `train`'s: per-window BCE for cases with an interval, MIL
  logsumexp over the sampled windows otherwise. The MIL branch runs one no-grad
  pass for the softmax coefficients and a second backward pass one window at a
  time, which is exact (the gradient of logsumexp *is* the softmax) and keeps
  activation memory at one window.
- `-out` has the same JSON shape as `train -out`, plus a `finetune` block, so
  the existing analysis reads it unchanged. `-save-encoder` writes the unfrozen
  block weights (keyed by absolute block index) and the head.

Cost on a 12 GB 4070, ViT-L at 384 px with 64-frame windows (18432 tokens x
1024 dims per window):

| | per window |
| --- | --- |
| frozen trunk, first pass (bf16, no_grad) | ~0.6 s, ~2 GB peak |
| cached hidden state | 37 MB on disk, 74 MB fp32 on the device |
| trainable tail, K=4, checkpointed fwd+bwd | ~0.5 s, ~1.5 GB above the 0.6 GB of weights |

So epoch 1 costs about 1.1 s per window and every epoch after it about 0.5 s.
The disk cost is the thing to watch: at stride 16 a 60 s clip at 15 fps is ~53
windows, i.e. ~2 GB of cache per clip variant. Cache only the cases a run
actually touches (`-case`), raise `-stride`, or use `-cache none` on a large
corpus.

## 3. Score through the bench

`analyze` honors the bench's `-analyzer` contract. Clips that are bench cases
are read from the cache when `-cache` is given (keyed on the clip's parent
directory, which is the case id); anything else is encoded on the fly unless
`-cache-only` is set.

```
scripts/run-bench.sh run -analyzer "bench/analyzers/vjepa/.venv/bin/python \
  bench/analyzers/vjepa/vjepa_bench.py analyze \
  -head-file bench/features/vjepa2-vitl-256-8fps-full-f16/temporal.pt \
  -cache bench/features/vjepa2-vitl-256-8fps-full-f16 -cache-only" \
  -notes "vjepa2 vitl temporal head"
```

A bench run over the same cases the head was trained on measures fit, not
generalization; the cross-validated summary from `train` is the honest number
until there are held-out clips. The verdict carries `contact`,
`contact_probability`, `start_seconds`/`end_seconds` from the temporal head,
and `threat` derived from contact alone (`none`/`low`); this analyzer does not
rate severity, so the bench's threat columns say nothing about it.

## What to run first on the GPU box

1. `encode` ViT-L at 256 with `-flip`, then `train` all three heads with
   `-seeds 5`. That is the baseline number.
2. The same with `-crop bottom`. If the near-ego crop helps, resolution
   at the contact zone was a limiting factor.
3. ViT-g at 384 (`-size 384`), only if the ViT-L learning curve has not
   saturated.
4. Once staged clips land with intervals: rerun, and add a learning curve by
   training on random subsets of the positives.

## Confounds found in the bench clips (2026-09-04)

Two things in the data, not the model, inflated the first numbers:

- `garage-mustang-door` ended with a 2 s CapCut outro card. The temporal
  head placed its "contact" at 31.6 s, i.e. on the card. The clip is now
  trimmed (`m2-res_480p-notail.mp4`, original kept in `bench/clips-removed/`).
- Every field clip is 3 fps / 640 px because the Pi transcodes before upload,
  while every downloaded positive is 30 fps. `-match pi` closes that gap;
  see above.

Four positives now carry `start_seconds`/`end_seconds` (read off 10 fps
frame strips), so their windows outside the touch train as negatives from
the same footage, and the summary reports `loc_hits`: whether the held-out
head pointed at the labeled second. The garage clip has no interval because
the door rests on the car from the first frame.

## Status (2026-09-05, overnight run)

Protocol now: **native frame rate, 15 fps, 384 px, 64-frame windows (4.3 s)**,
no `-match` transcode. The six 3 fps Pi-uploaded cases were removed from
`dataset.json` (kept in `bench/clips-removed/`). 48 labeled cases: 37
contact / 11 no-contact (3 own-camera quiet clips, 8 downloaded near-miss);
15 carry the tag `holdout` (11 contact / 4 none, seed 20260904). No
own-camera positives exist yet. Leave-one-case-out unless noted, threshold
0.5, `report.py` tabulates the JSONs.

| encoder | head | windows | found | phantom | AUC | AUC within web |
| --- | --- | --- | --- | --- | --- | --- |
| 2.1 ViT-L 384 | temporal | f16 s8, flips | 23/28 | 4/8 | 0.67 | 0.71 |
| 2.1 ViT-L 384 | temporal | f64 s16, flips (36 cases) | 24/28 | 4/8 | 0.71 | 0.80 |
| 2.1 ViT-L 384 | temporal | f64 s16, flips | 31/37 | 6/11 | 0.74 | 0.79 |
| 2.1 ViT-L 384 | motion | f64 s16, flips | 32/37 | 7/11 | 0.76 | 0.79 |
| 2.1 ViT-L 384 | region | f64 s16, flips | 34/37 | 6/11 | 0.74 | 0.80 |
| **2.1 ViT-g 384 (1B)** | temporal | f64 s32, no flips | 33/37 | 5/11 | **0.81** | 0.79 |
| 2.1 ViT-L 384, `-crop bottom` | temporal | f64 s32, no flips | 29/37 | 5/11 | 0.70 | 0.76 |
| fine-tune K=0 (holdout 15) | temporal | f64 s32 | 10/11 | 3/4 | 0.80 | 0.79 |
| fine-tune K=4, 8 ep (holdout 15) | temporal | f64 s32 | 11/11 | 4/4 | 0.77 | 0.70 |
| fine-tune K=4, 30 ep, 16 win/clip (holdout 15) | temporal | f64 s32 | 11/11 | 3/4 | 0.86 | 0.88 |

What is stable across every row: the misses are hand-scale contacts
(charger unplug, door-handle puller, Ventura and YouTube vandalism, ghost
cart) and the phantoms are people handling something within arm's reach
without touching (both own-camera bystander clips, `rd-bo8byv`, `rd-bds11x`,
`rd-cxmrnf`, the shopping-cart clip). Heads, motion features, coarse spatial
regions, the lower-band crop and fine-tuning the last four blocks (training
loss 0.004, holdout scores saturated at 1.000 for 13 of 15 clips) do not
move that set; the 1B encoder moves
one or two clips. The frozen representation encodes proximity, not contact.

Ensemble with the Qwen 3.8 zero-shot scores (`analyzers/vlm`) was
pre-registered as a veto rule (`veto.py`) and gives no gain: the VLM scores
true contacts as low as 0.07, so no veto threshold removes a phantom without
removing a contact. The Qwen LoRA fine-tune (33 non-holdout clips) matched
its own zero-shot (AUC 0.69) and lost the confident rejections that made a
veto conceivable.

The 1 s windows (f16 at 15 fps) are worse than 4.3 s windows on every head;
the checkpoint was trained at 64 frames. Keep f64.

Training notes: the f64 cache is 76 GB with flips; `train` streams each
grid once and keeps only the pooled representation (`ClipFeatures(lazy=True)`,
`stage_examples`), so it runs in ~2 GB of GPU memory. Running two trainers or
a trainer next to an encode on the 12 GB card spills to host memory on
Windows and slows 30x. Detached jobs on the PC survive an ssh drop only when
launched as a single foreground ssh command; `start /b`, `Start-Process` and
`schtasks` did not start reliably.

Superseded (2026-09-04, matched 3 fps protocol, 11 cases): see git history
of this file; those numbers were within noise of 5 positives.
