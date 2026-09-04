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
| `vjepa_bench.py` | the CLI: `encode`, `train`, `analyze` |
| `vjb/frames.py` | ffmpeg sampling into square uint8 frames; window/stride logic |
| `vjb/encoder.py` | `VJEPA2Encoder` (Hugging Face) and `FakeEncoder` (pixel statistics, for plumbing) |
| `vjb/store.py` | the feature cache format and `bench/dataset.json` loading |
| `vjb/heads.py` | `meanpool`, `probe` (attentive probe), `temporal` (per-timestep) heads |
| `vjb/train.py` | leave-one-case-out CV, loss, metrics, head save/load |
| `vjb/analyze.py` | the bench `-analyzer` verdict |
| `tests/` | end-to-end plumbing on synthetic clips, no weights needed |
| `bench/features/<cache>/` | token caches and training summaries (gitignored) |

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
  -fps 3 -size 384 -crop full -frames 16 -stride 4 -match pi -flip
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
  -fps 3 -size 256 -crop full -frames 16 -stride 4 -match pi -flip
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
- `-crop`: `full` squashes the whole frame to a square and keeps every pixel
  (the ego body sits at the frame edge, which `center` would cut off);
  `band:0.35,1.0` keeps only the lower band before squashing, which is the
  near-ego crop from the notes and worth an A/B once the staged clips exist.
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
2. The same with `-crop band:0.35,1.0`. If the near-ego crop helps, resolution
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

## Status (2026-09-04)

Plumbing verified end to end: synthetic tests, the fake encoder on the real
clips, and a Go bench run through the `-analyzer` contract. The real ViT-L
encoder was run on all 11 bench cases on a Mac GPU (token grid and order
verified against the transformers source), and heads were cross-validated on
those features. First numbers, leave-one-case-out, 3 seeds, no flips,
threshold 0.5:

| head | contact missed | contact phantom | AUC | CV time on M-series GPU |
| --- | --- | --- | --- | --- |
| meanpool | 0/5 | 4/6 | 0.83 | 15 s |
| temporal | 1/5 (night-handtruck-boxes) | 1/6 | 0.93 | 1 min |
| probe | 1/5 (night-kia-parks-close, 0.43 with seed spread 0.27) | 1/6 | 0.93 | 17 min |

Read these as "the pipeline works and the features carry signal", not as a
result: with five positives one case moves the miss rate by 20 points, and
the positives are downloaded compilations while every negative is a real
field clip, so a head can separate them on style alone. The confound goes
away only with staged positives from the real cameras.

Memory: training holds the whole token cache on the device in float16
(1.7 GB for the bench) plus a float32 transient per clip while standardizing;
the encoder is never loaded during `train`. Running several `train`
processes at once multiplies that.
