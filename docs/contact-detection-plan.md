# Contact detection: decisions, objectives, and next-session handoff

Updated 2026-09-05. Read this first after clearing conversation context.

## User decision and immediate task

**We are starting with generating an Unreal Engine synthetic dataset.** The next session should inspect the existing simulator, make its contact labels correspond to rendered interactions, and produce a small, reviewable dataset pilot. Do not restart architecture research or a broad model sweep before doing that work.

The user has only built simulator boilerplate and initial rendering experiments. **No synthetic dataset has been used to train the models discussed below.** Simulator defects cannot explain any previous model failures. They matter only as prerequisites to generating useful training data now.

This file records the agreed direction and recommended implementation sequence. Specific pilot counts, GPU spending, asset purchases, and final model architecture have not been agreed. Do not invent a budget or interpret this plan as permission to purchase assets or rent compute.

## Appearance iteration completed after this handoff

The user subsequently requested a photorealism pass on the existing scene.
See [the simulator realism review](../sim/unreal/realism-review.md) for code
changes, comparison renders and limits. Asset package paths/material masks,
missing car components, persistent sky illumination, lot surfaces and fixtures,
and the door hinge hierarchy were improved. The door now has a panel/window
assembly but remains separate from the neighbour's fixed bodywork.

**This did not complete the contact-valid pilot.** Ego narrowing, rendered-mesh
contact checks, articulated production-quality vehicles/humans, camera evidence
and matched contact/near-miss validation remain open. Use `--review-only` to
write unvalidated appearance sidecars without importable benchmark cases.
The simulator now has 31 passing pure-Python tests; these do not certify labels.

## Objective

Detect whether anything physically contacted a parked Tesla in Sentry footage: door dings, hands, bags, carts, another vehicle, and similar interactions. Distinguish actual contact from a person or object passing or stopping very close to the car.

False contact alerts are particularly costly to the user. Measure contact recall at a separately selected false-positive operating point, with raw counts. A detector that calls everything contact is unusable, even with perfect recall. Threat severity and descriptions are secondary; contact does not automatically mean damage.

The system receives long event clips. The eventual detector needs to find brief contact within them, not require the user to supply a manually selected contact interval at inference. Timed training labels are supervision, not an inference dependency.

Distinguish three things:

- Physical contact occurred in the scene.
- A particular camera and sampled input provide visible evidence of it.
- That evidence justifies a contact decision with enough confidence to alert.

Some physical contacts are occluded or disappear after sampling/compression. Do not force every ambiguous camera view to be an unequivocal positive. An uncertain/abstain outcome may be necessary.

## What has already been tried

The user tried Gemini and Qwen 3.8 out of the box, local Qwen LoRA training, frozen V-JEPA 2/2.1 encoders with trained heads, and partial V-JEPA encoder fine-tuning. None has produced reliable contact alerts. Recurring errors are missed subtle touches and invented contact around close bystanders.

The current local dataset, `bench/dataset.json`, contains 85 cases: 48 labeled (37 contact, 11 no-contact) and 37 unlabeled. Only four positives have non-null contact intervals. Three labeled own-camera cases are all negative; labeled positives come from external footage. The user described their labeling approach as a yes/no answer for contact anywhere in a one- or two-minute clip; actual local clips also include shorter excerpts and trims. Inspect durations rather than assuming every clip is two minutes.

Clip-level supervision is valid multiple-instance learning, but difficult at this dataset size. In particular, randomly selecting part of a positive clip and retaining its positive label can supply a training example containing no contact. Timing enables contact-preserving sampling and nearby negative windows from the same scene. It does not guarantee success by itself.

Saved results worth knowing:

- V-JEPA 2.1 ViT-g, frozen temporal head: 33/37 contacts found, 5/11 false positives, AUC 0.811 overall and 0.787 within external footage.
- Partial V-JEPA fine-tune, last four blocks, longer run: 11/11 contacts found but 3/4 false positives on a 15-case holdout, AUC 0.864. This small, repeatedly inspected holdout is development validation, not a clean final test.
- Qwen balanced LoRA: AUC 0.690 versus 0.687 for its matched eight-frame zero-shot baseline. This is not convincing improvement.
- Qwen motion-guided sampling was stopped before completing evaluation. Its partial results are inconclusive.

Production still uses Gemini; do not remove `internal/gemini` or deploy an experimental replacement. User focus is improving detection, not renewed prompt tuning.

## Architecture decisions

| Option | Direction and reasoning |
| --- | --- |
| Frozen V-JEPA 2.1 plus a trained contact head | Keep as the main baseline. Feature caching makes head experiments relatively cheap. Improve supervision and preserve spatial detail before declaring the representation inadequate. |
| Partial V-JEPA fine-tuning | Preferred subsequent model experiment. Compare a frozen head and unfrozen final blocks on the same improved data, sampling, and evaluation. Previous fine-tuning does not conclusively rule it out. |
| Qwen fine-tuning | Pause for now. It is expensive on available hardware and has not shown useful improvement. The saved adapters updated only language-model layers, not the visual encoder; this was not a test of visual-encoder fine-tuning. |
| Smaller pretrained video model | A useful later cost/performance control. A pretrained 3D ResNet is one possible baseline, not a selected replacement. |
| New task-specific detector using pretrained weights | Sensible. We can own the contact head, sampling, localization and alert decision without pretraining visual understanding from scratch. |
| Entire visual backbone trained from random initialization | Not recommended with the current budget and real dataset. A large synthetic set does not automatically solve real-world transfer. |

A promising model input is a short temporal window with both context and detail near the potential contact area. This is a proposal, not an implemented or validated architecture. During evaluation, the crop/window selection must work without ground-truth test timestamps or contact locations. Oracle crops are useful only as explicitly labeled diagnostic experiments.

## Why Unreal is worth trying

The strongest benefit is controlled, matched examples, not simply a large number of positive clips. Render the same car, door, camera, person, lighting and background twice: once with contact and once stopping just short. This directly tests whether the learned distinction is contact rather than proximity or scene appearance.

Simulation can supply timestamps, contacting object, contact position, and camera visibility without manually timestamping every generated event. Those labels must derive from the geometry and motion actually rendered. The prior simulator README's claim that shared paths make label/render disagreement impossible is incorrect.

Synthetic examples should supplement real evidence. Their value is measured by improvement on real held-out clips, not synthetic accuracy. Randomizing appearance and capture conditions can help transfer, but success on door-ding detection is not established.

## Immediate work: door-ding dataset pilot

Start with **door contact versus door near-miss**, rather than implementing all six existing scenario types to completion. Door dings are the first recommended pilot scope; extend to hands, bags and carts after demonstrating real-world benefit.

### 1. Inspect and run the existing project

- Project: `sim/unreal/SentrySim.uproject`.
- Entry points: `sim/unreal/generate.sh`, `Content/Python/generate.py`.
- Generator modules: `Content/Python/sentrysim/`.
- Spec: `sim/unreal/specs/teslacam-hw4.json` currently specifies 30 fps, 20 seconds and six cameras.
- Rig: `calib/rig.json`; supporting derivation and limitations in `calib/README.md`.
- Unreal README documents UE 5.8, Xcode, ffmpeg, engine-template cars/mannequins, and initial day/dusk/night renders. Verify installed tools/assets locally before relying on these notes.
- Existing output includes lighting checks under `sim/unreal/out/quick-day`, `quick-dusk`, and `quick-night`. These are not a validated training dataset.

Use the existing project. Avoid rebuilding a second simulator from scratch.

### 2. Align visible geometry, contact labels and timing

For the door pilot, ensure the door and ego contact surfaces used by labeling match their rendered transforms and dimensions. Use explicit collision/contact tolerances and record them. The implementation can be deterministic and keyframed; it does not require a full physics simulation if the contact geometry and motion are correct.

An impact should stop against the surface rather than pass through it. The negative counterpart should stop short without inadvertently providing a label shortcut such as systematically different lighting, dwell time, camera shake, or person animation. Include plausible overlap in non-contact motion cues between classes.

Verify labels after frame quantization, and distinguish first contact, sustained contact and separation where relevant. Avoid collapsing disconnected contacts into one uninterrupted interval in future multi-contact scenes.

Known boilerplate issues to inspect:

- `scenario.py:127` labels probe discs against a 2-D ego rectangle, not the visible meshes.
- `scene.py:313` narrows the current rendered ego from 1.849 m to 1.740 m without updating label geometry. Resolve this before using side-contact labels.
- `visible_cameras` checks horizontal angles and actor corners, without vertical visibility or occlusion. Camera selection does not establish visible contact.
- The door is a slab and the ego a scaled sports car. Assess contact-relevant shape and motion before investing in cosmetic realism.
- The cart has a separately reproduced label error: seed 1, duration 20 seconds, current full spec reports contact while its front is approximately 9.7 cm short of the rectangular ego boundary. Fix before adding carts; this need not distract from the door pilot.
- `brush`/`lean` currently use root trajectories and walk/idle animation rather than contact-constrained hands/body poses. These are later scenario work.
- Fisheye remapping is unfinished; current rendering is rectilinear. Check camera fidelity for the chosen pilot views. Side-camera pilot work need not block on completing the entire six-camera rig.

### 3. Generate matched pairs with reproducible metadata

Keep raw renders and derived video inputs separate. Record enough to reproduce each output:

- Unique case/variant ID, generator revision, seed and explicit spec.
- Pair/group ID shared by contact and near-miss variants.
- Scene physical-contact label, contact interval(s), object identity and location.
- Per-camera evidence/visibility status, distinguishing unavailable or ambiguous evidence from a negative physical-contact label.
- Camera parameters, render fps, resolution and relevant asset/geometry identity.
- Encoding/compression settings and source/derived content identity.

The current `sim-<seed>-<kind>` IDs can collide across camera, lighting or contact variants; design variant identity before generating a paired set. Additional metadata is a proposed extension, not already supported by all benchmark consumers. Keep a simulation manifest/sidecar where needed rather than overloading existing fields silently.

Keep synthetic data in a separate, versioned location initially. Do not automatically merge a large generated corpus into the real benchmark. The current `import_cases.py` can merge into `bench/dataset.json`; use its dry-run mode when inspecting it.

### 4. Review a small batch before bulk generation

Inspect actual rendered sequences around contact and corresponding near misses, plus the exact frames/crops/compression the detector will receive. Review clear negatives as well as close negatives. Include a diagnostic overlay of contact geometry/time for review, but never expose label overlays or metadata to the training images.

Acceptance checks:

- A positive has geometrically correct contact and its visible-evidence status is accurate.
- A near-miss actually preserves a gap and has no incidental contact elsewhere.
- Labels and footage agree in time, including after derived encodings.
- Pairs differ in the intended interaction without systematic unrelated label cues.
- No accidental file overwrites; repeated seeds/specs reproduce the intended output.

Start with a modest pilot and measure render/storage costs before choosing a bulk count. There is no agreed number of clips or compute budget yet.

### 5. Add variation after correctness

Vary gap size, door speed, stopping behavior, camera/vehicle geometry, contact location, actor appearance, backgrounds, day/dusk/night, occlusion and compression. Randomize these across both labels. Preserve matched pairs while creating diverse groups.

Retain native-rate originals. The repository's production compression profile is 3 fps and 640 pixels wide, while latest V-JEPA runs used 15 fps without Pi matching. Treat native and production-like inputs as separate experiments. Do not silently replace one with the other or assume production configuration from a local default alone.

## Later training and evaluation gate

Once the synthetic pilot is trustworthy:

1. Freeze a real evaluation split and decision-threshold selection procedure. Existing repeatedly inspected cases remain useful development data; avoid claiming they are an untouched final test.
2. Add targeted timing/visibility annotations to a manageable subset of real contact examples. This supports comparison with real-only training and checks whether synthetic contacts resemble the real failure cases. The immediate user-selected work remains simulation, not an exhaustive manual-labeling project.
3. Compare real-only and real-plus-synthetic training using the same V-JEPA configuration and real test cases. A synthetic-only control may help diagnose transfer.
4. Compare a frozen head with partial encoder fine-tuning under identical supervision. Positive sampled windows must contain contact, or use a correctly formulated clip-level objective that accounts for unobserved contact.
5. Report raw true/false-positive/negative counts, recall at the preselected operating point, and timing/visibility breakdowns. Select thresholds on separate development data.
6. Expand the generator or model complexity only when the real-world comparison warrants it.

All cameras, trims, compression variants and paired synthetic variants belonging to the same underlying event/scene group must stay in the same partition. Do not let siblings leak into training and test. The V-JEPA dataset loader currently accepts exactly one clip per case, whereas the simulator can emit several; choose an explicit integration strategy while retaining group identity.

## Audit findings to preserve before reusing training pipelines

Full details: [video-understanding audit](video-understanding-audit-2026-09-05.md).

- Qwen reports choose their best precision threshold on the reported labels, including pooled out-of-fold labels. These are retrospective operating points, not independent deployed-performance estimates.
- V-JEPA fine-tuning can sample contact-free windows from an untimed positive and retain a positive target. Frozen-head time cropping does not update interval-based labels.
- V-JEPA recorded positive loss weighting of 2.0 and a 0.5 threshold; this is not inherently calibrated to avoid false alerts. Training uses log-sum-exp window aggregation; inference uses max.
- Fine-tuning cache identity omits crop, matching profile and checkpoint identity; claimed metadata validation is missing from that path. Qwen resumes adapters and scores without fully checking split/configuration identity. These are confirmed hazards, not evidence that past reported results were contaminated.
- Qwen's stored adapter updates only language layers, and its experiment supplies sampled images rather than a native video input path. Do not describe it as full visual adaptation.
- Do not conclude that the frozen representation fundamentally cannot encode contact: resizing, spatial pooling, weak supervision and optimization are not isolated by the experiments so far.

Prior audit validation: 32 V-JEPA tests and 30 simulator pure-Python tests passed. These tests do not establish rendered-contact correctness or real detection quality. No expensive training or production changes were made during the audit.

## Second Unreal appearance pass — Model 3 interactions

The new `sim/unreal/specs/interactions.json` uses a credited community Model 3
with real separate door parts and an interior, plus a clothed MakeHuman keying
actor. Blender preparation preserves UVs/normals and computes door/bumper
staging from the rendered vehicle triangles. The new kinds are `door_ding`,
`door_near_miss`, `keying`, `backing_impact` and `backing_near_miss`; they require
review-only output. Neutral grading, softer daylight and warm lot fixtures are
implemented. See [interaction review](../sim/unreal/interaction-review.md) and
[asset credits](../sim/unreal/ASSET-CREDITS.md).

This removes the stand-in car/door mismatch for the new spec, but does not solve
frame-level visible-contact supervision. The backing contact can be below the
front-camera frame; procedural human motion, occlusions, deformation, actor
variety and real camera/sensor matching still need work. Do not promote these
appearance reviews into training solely from the staged contact flag.

## Hardware, workflow and scope

- Development workspace: `/Users/adri/code/tesyx`.
- Existing notes describe a 32 GB M5 MacBook Air and a Windows RTX 4070 with 12 GB VRAM; verify remote availability before planning runs. `bench/analyzers/vjepa/windows-gpu.md` contains the GPU workflow.
- Avoid simultaneous GPU-heavy jobs on the 12 GB card; prior notes report severe spill/slowdown.
- Existing venv: `bench/analyzers/vjepa/.venv/bin/python`; simulator pure tests run with `-m pytest -q sim/unreal/tests` from repo root.
- There is substantial pre-existing uncommitted work in `bench/`, `calib/`, `sim/`, and other files. Preserve it. Read applicable `AGENTS.md` instructions before edits.
- Current task is dataset generation and validation, followed later by controlled training. No production replacement or deployment is authorized by this handoff.

## References and interpretation

- [Meta V-JEPA 2/2.1 code and documentation](https://github.com/facebookresearch/vjepa2): pretrained video representations; 2.1 emphasizes dense, temporally consistent features. This is architectural motivation, not evidence of Tesla contact accuracy.
- [Torchvision pretrained video models](https://github.com/pytorch/vision/blob/main/torchvision/models/video/resnet.py): a possible smaller pretrained comparison.
- [Domain randomization research](https://arxiv.org/abs/1703.06907): supports investigating variation for simulation-to-real transfer; does not guarantee success on subtle contacts.
- Local experiment state: `bench/README.md`, `bench/analyzers/vjepa/README.md`, `bench/analyzers/vlm/README.md`.
- Simulator implementation and calibration: `sim/unreal/README.md`, `calib/README.md`. Their factual claims should be checked against code; historical case counts and the label/render-equivalence claim are not authoritative.

## Door pilot tooling — 2026-09-06

Frame-level contact supervision for the door pair is now *measured* (Blender
BVH on the prepared meshes under the transforms Sequencer evaluates), with a
versioned manifest, media validation, promotion gates that require geometry,
media and Codex review to refer to the same sha256 and configuration, a
reviewer queue with contact-adjacent frames and separate overlays, a
measured (not modelled) frame map for the 3 fps production transcode, and a
dry-run mapping to V-JEPA windows. Read
[sim/unreal/synthetic-dataset.md](../sim/unreal/synthetic-dataset.md) first;
the operator entry point is `sim/unreal/synth_pilot.py`. Two findings to keep
in mind: the 3 fps upload profile can contain **no** contact frame of a
roughly 0.2–0.45 s door touch. Door opening/closing speeds, dwell, rebound,
near-miss clearance, vehicle paint, sun angle and lot lighting now vary by seed;
matched outcomes share the same sampled angular-rate and appearance profile.

**Resume here: review the new matched door clips and per-camera contact visibility, then add physically grounded response and broader actors/environments before promoting any scenes to training labels.**
