# Video-understanding audit — 2026-09-05

Scope: current working tree, including uncommitted experiments; saved local training summaries; installed MLX trainer and saved adapter configuration; dataset metadata and selected cached input frames; simulator geometry and rendering code. No model was retrained, no production service was queried, and footage was not exhaustively relabeled. Findings distinguish demonstrated implementation problems from experimental limitations. No training code or labels were changed.

The evidence supports “these configurations do not produce usable contact alerts.” It does not establish that VLMs or V-JEPA representations cannot learn contact. Several experiments restrict the input or supervision in ways that make that broader conclusion premature.

## 1. Synthetic labels can disagree with rendered contact — demonstrated

`sim/unreal/Content/Python/sentrysim/scenario.py:127` computes distance between a 2-D probe disc and a rectangular ego footprint. That is not collision between the visible meshes. Sharing trajectories does not make those geometries equivalent.

Concrete reproduction, using `specs/teslacam-hw4.json`, duration 20 seconds, seed 1, kind `cart`: the scenario returns contact=true, with minimum probe gap −0.203 m. The actual front of the rectangular cart is still +0.097 m from the ego footprint. The cart probe is already at its front tip but is additionally given a 0.30 m radius (`scenario.py:311`). This creates contact across a visible gap, even before considering the ego mesh's different shape.

`scene.py:313` also narrows the rendered placeholder ego while labeling retains the original dimensions: the current spec is 1.849 m wide, but the renderer's rule makes it 1.740 m wide. That moves each side inward by 5.45 cm. Even equal bounding-box dimensions would not make a curved car mesh equal to a solid rectangular collision footprint.

`brush` and `lean` manipulate a pedestrian's root trajectory and a disc; `scene.py:429` supplies only walk/idle animation. There is no contact-constrained hand or leaning pose. Camera inclusion is only a horizontal-angle test (`labels.py:23`, `cameras.py`), without vertical visibility or occlusion checks, and can include a camera because an actor corner is visible even when contact is hidden.

Consequence: do not use current synthetic labels as verified visible-contact truth. This is a more immediate problem than texture realism or calibration precision. Label actual rendered geometry, distinguish physical contact from visibility, and check paired touch/near-miss renders before scaling generation. Multiple cameras of the same synthetic event must remain in the same evaluation group; the current V-JEPA loader also requires exactly one clip per case, whereas the simulator emits multiple clips.

## 2. The Qwen fine-tune did not update the vision encoder — verified

`bench/analyzers/vlm/vlmb/sft.py` invokes MLX LoRA without `--train-vision`. The installed `mlx_vlm/lora.py:114` discovers linear layers inside `model.language_model`; `trainer/utils.py:186` freezes the model and installs language adapters. The saved balanced fold-0 adapter configuration lists 496 module keys, all beginning with `language_model`.

Language adapters can change how visual tokens are interpreted, so this is not an inherently invalid fine-tune. But it does not test updating the visual feature extractor to preserve small contact cues. The experiment's stated contrast with a frozen visual representation is therefore overstated.

Inference supplies `image=list(frames)` and the training rows have an `images` column: this is a chronological multi-image classification experiment, not a call through a native video preprocessing path. No timestamps are supplied with individual frames.

## 3. Temporal evidence and labels are mismatched — confirmed mechanisms

Only 4 of the 37 labeled positive cases have non-null contact start/end times. The other 33 positives provide only an event-level answer.

Qwen's eight-frame sampler spreads frames across the entire clip. For `sedan-door-swing`, the source duration is 19.333 seconds and the labeled interval is 4.5–6.0 seconds. The nominal samples on either side are about 3.625 and 6.042 seconds: none lies inside the interval. I inspected the cached eight-frame contact sheet: it includes closed/open-door states, but those are weaker evidence than the motion through the labeled contact moment. Other frames may still show resting contact; missing an annotated interval does not prove all contact evidence is absent.

V-JEPA fine-tuning has a related problem: `vjb/finetune.py:302` randomly selects a limited number of windows. For a positive case without timing, a sampled subset can contain no contact, but `clip_step` still trains it as positive. Only interval-labeled positives get contact-aware sampling. This directly creates pressure to classify incidental proximity or scene content as contact.

Frozen-head training also applies `_time_crop` before the loss without changing window labels (`vjb/heads.py:69`, `vjb/train.py:136`). A reproduction with 32 temporal positions and a signal at the final position removed that position in 733/1,000 seeded crops at the recorded `time_crop=0.3`. Encoder tokens are contextual, so this does not prove all signal disappears; it does establish that the augmentation does not preserve the supervised moment. Any-overlap positive window labels make edge contacts especially relevant here.

## 4. V-JEPA's objective and operating point need separating

The recorded strong runs use `pos_weight=2.0` despite a positive-heavy dataset, then classify at 0.5. Positive errors receive extra weight; this is not a calibrated precision-first operating point. It can be an intentional recall choice, but it conflicts with interpreting false-alert behavior at 0.5 as an inherent model limit.

Training aggregates windows using unnormalized log-sum-exp; inference uses the maximum (`train.py:145`, `train.py:188`; the fine-tune uses the same pattern). With temperature 4 and every window logit equal to −0.5:

| Windows | Training aggregate probability | Inference probability |
| --- | --- | --- |
| 1 | 0.378 | 0.378 |
| 16 | 0.548 | 0.378 |
| 64 | 0.632 | 0.378 |

Thus the training decision changes with window count even when window evidence is identical. Smooth-max training is a legitimate surrogate, but these scores are not automatically calibrated and the train/evaluation discrepancy deserves a controlled comparison.

The temporal head spatially mean/max-pools the full token grid (`heads.py:170`); the region head preserves only a coarse 4×4 grid. Those operations remove explicit fine spatial arrangement from the head input. The encoder may retain that information in contextual features, but the experiments do not isolate whether the encoder, resizing, pooling, weak labels, or optimization causes a miss. “The representation encodes proximity, not contact” is a hypothesis, not a demonstrated representation limit.

## 5. Reported precision is optimistically selected — confirmed

`vlmbench.py:68` passes all reported labels and scores to `metrics.report`, which chooses the best threshold on those same labels. `cmd_cv` pools held-out predictions and then calls that function. Each score can correctly be out of fold while the selected operating point is still fitted to the evaluation labels.

The comments in `vlmb/cv.py` and `vlmb/metrics.py` claiming training-fold-only threshold selection do not describe the executed reporting path. Treat those precision/recall pairs as retrospective operating points, not independently evaluated deployment estimates. This finding does not by itself invalidate the threshold-free ranking calculation.

The 15-case holdout has also been inspected across multiple fine-tuning choices. It is now useful development validation; selecting the winner from it needs a fresh final test. Leave-one-case-out correctly excludes a case's flipped copy and computes feature statistics from training examples—those safeguards are present.

## 6. The dataset cannot yet establish production alert quality

Current dataset: 85 cases, 48 labeled (37 contact, 11 no-contact), 37 unlabeled. The three own-camera cases are all negative; every labeled positive comes from external footage. The 15-case holdout contains 11 positives and only 4 negatives. One false positive moves holdout false-positive rate by 25 percentage points.

The saved ViT-g run has 33/37 contacts found and 5/11 false positives. Its precision on this positive-heavy benchmark is 33/38 = 86.8%, but that does not predict precision in ordinary quiet field events. AUC is 0.811 overall and 0.787 within web footage. The long K=4 fine-tune has AUC 0.864 on 15 holdout cases but still falsely flags 3/4 negatives. Neither is presently usable evidence of a quiet alerting system.

The latest V-JEPA protocol samples native footage at 15 fps with no Pi matching, while the repository's production compression profile is 3 fps and 640 pixels wide. Testing native footage is useful for diagnosing an information ceiling; it does not show that the same performance survives production input. A local-code audit cannot establish whether a deployed unit overrides that profile.

Binary labels also lack an explicit visibility/uncertainty field. Some real contacts may be known from context or damage but not distinguishable from a near miss in the selected camera. That possibility needs a footage audit, not an assertion that current labels are wrong. Separate “contact happened” from “this model input visibly supports contact.”

## 7. Caches can silently invalidate comparisons — demonstrated vulnerabilities

Fine-tune `cache_key` includes fps, size, window length, stride and K, but excludes crop, matching profile, checkpoint identity and clip identity/content. `TrunkCache.path` uses case ID, variant and window number, without the clip filename. Despite its docstring, the fine-tune path does not write/validate the claimed cache metadata. Changing squash to bottom or none to Pi matching produces the identical key. Reusing a root can therefore supply old hidden states for new inputs.

Qwen's frame cache checks only that a stamp and JPEGs exist; it does not validate the stamp's source or current file contents. Its CV resume logic reuses adapters and scores based on file existence without checking the current split, training labels, hyperparameters or frame configuration. Reusing a work directory after changing the split could even turn held-out scoring into training-set scoring.

These are confirmed code hazards, not proof that a published run was contaminated. I checked 180 existing Qwen frame-cache stamps associated with currently labeled cases and found no source-basename mismatch. That check cannot establish byte-level freshness. Immutable run manifests and content-bound caches are needed before relying on resumed comparisons.

## 8. Gemini's current prompt hard-codes unreliable conclusions

`internal/gemini/gemini.go:46` tells the model that an opening door stopping while someone exits means it met the car, and that restricted door opening in a tight space means contact. Those observations also admit voluntary stopping or a door's own stopping mechanism. Conversely it says uninterrupted close passing is not contact, which can exclude a brush or scrape.

Those rules can encourage both of the reported failure modes. This is a finding about the prompt's logic, not a claim that changing it would solve Gemini, and not a recommendation to restart broad prompt tuning.

## What the work has established, and what to do before another model run

The shared benchmark, independent contact labels, provenance checks, removal of an outro confound, held-out predictions, saved per-case scores, and fake-encoder plumbing tests are useful foundations. The recorded failures are real for these configurations, and the weak fine-tuning gains should not be sold as success.

Before spending more training time: correct simulator contact/visibility truth and cache identity; audit contact visibility at the actual model input; add timed labels and closely matched touch/near-miss examples from the same cameras; preserve positive evidence during sampling; and freeze an evaluation protocol that selects thresholds separately and reports raw false-positive counts on field-representative negatives. Synthetic matched pairs could help, but the current generator would otherwise teach the exact “near means touching” shortcut it is meant to eliminate.

Validation performed: existing V-JEPA suite 32 passed; simulator pure-Python suite 30 passed; small deterministic reproductions for cart geometry, temporal cropping, cache-key collision, and aggregation mismatch. There is no `bench/analyzers/vlm/tests` directory, and its venv has no pytest. Passing plumbing/geometry tests does not validate real visual understanding or rendered contact alignment. No expensive inference, training, or Unreal render was launched.
