# Synthetic dataset pipeline — Claude handoff

## Assignment and ownership

Implement the engineering needed to turn the existing Unreal door-ding and
door-near-miss scenes into a reproducible, validated dataset pilot. Use the
existing simulator; do not create a second pipeline.

**Codex owns all vision-related work:** visual inspection and acceptance,
photorealism, lighting/color grading, materials, assets, human animation,
camera appearance/calibration, and judgments about whether contact is visually
discernible. Leave these to Codex. Claude may implement numerical geometry
checks and review tooling, but must not equate those checks with visual approval.

Read first:

- `docs/contact-detection-plan.md`
- `sim/unreal/interaction-review.md`
- `sim/unreal/README.md`
- Applicable `AGENTS.md` instructions

Current starting point: five new interaction kinds exist, with Model 3 mesh
staging and review renders. They deliberately require `--review-only` and do
not emit benchmark cases. Existing tests and video checks do not certify
contact labels. Preserve unrelated uncommitted changes throughout the repo.

## 1. Audit the current implementation

- [x] Trace `generate.py`, `sentrysim/interactions.py`, `scene.py`, `review.py`,
  `blender/solve_contacts.py`, `validate_review.py`, and `import_cases.py`.
- [x] Document which fields are staged intentions, measured geometry, or
  human-reviewed evidence; avoid silently changing existing field semantics.
- [x] Limit the first usable pilot to `door_ding` and `door_near_miss` using
  `specs/interactions.json`. Keep keying/backing outputs in review status.

## 2. Establish numerical contact supervision

- [x] Measure against the actual prepared door/ego mesh geometry and the
  transforms evaluated for rendering, including Unreal interpolation and
  frame timing. Do not use the old rectangular ego/probe-disc labels.
- [x] Define and record units, distance/intersection tolerances, and the
  method's limitations. Distinguish numerical tolerance from physical contact.
- [x] Export frame-indexed measurements and contact intervals, with explicit
  start/end conventions, first contact, separation, and any unresolved frames.
- [x] Detect unacceptable penetration and unintended contact in near misses;
  retain diagnostics sufficient to investigate failures.
- [x] Check the pair's shared setup and motion for unintended differences.
  Report scene or animation fixes needed to Codex rather than making visual
  changes as part of this task.
- [x] Add focused tests for frame quantization, tolerance boundaries,
  disconnected intervals, transform/scale mismatches, and preserved gaps.

Completion: machine checks can substantiate a scene-level physical-contact
claim, or explicitly reject/mark it unresolved. A staged positive flag alone
never passes validation.

## 3. Create a versioned dataset manifest and safe export

- [x] Define a documented schema containing unique output/variant IDs,
  shared scene/pair group IDs, seed, full configuration, source/asset identity,
  geometry method/tolerances, frame measurements, contact intervals, camera
  parameters, and raw/derived video identities and encoding settings.
- [x] Include separate physical-contact and per-camera evidence fields.
  Evidence starts unreviewed; support visible, occluded/ambiguous, and
  out-of-frame outcomes without turning hidden physical contact into a
  no-contact label. Record reviewer and reviewed artifact hash.
- [x] Make IDs distinguish lighting, camera, configuration, and derived
  variants. Define safe rerun/resume behavior that prevents accidental overwrite.
- [x] Keep all cameras, near-miss/contact siblings, trims, and compression
  variants of an underlying scene in one split group. Group identity must not
  fragment merely because an output filename or encoding changes.
- [x] Implement explicit promotion/export gates: geometry validation,
  media validation, and Codex visual review must refer to the same artifacts.
  Changes to relevant inputs invalidate previous approval.
- [x] Keep outputs in a separate versioned synthetic dataset location.
  Preserve `--review-only`; do not simply remove its guard to enable export.
- [x] Test ID collisions, stale approval, incomplete output, unsafe overwrite,
  split leakage, and rejection of unreviewed/invalid cases.

## 4. Build the pilot and review workflow

- [x] Add a reproducible batch configuration for a small matched door pilot;
  make size configurable and report an estimate before an expensive batch.
- [x] Retain native originals and describe derived model inputs explicitly.
  Inspect actual configuration before choosing compression/sampling settings;
  do not assume native, production, and V-JEPA inputs are equivalent.
- [x] Preserve timestamp/frame mappings through derived encodings, including
  whether a sampled window contains any measured contact frames.
- [x] Extend validation to cover complete pairs, manifest consistency,
  media integrity, interval bounds, and derived-input mappings.
- [x] Generate a review queue with links to clips, contact-adjacent frames,
  and optional diagnostic overlays. Store overlays separately so they cannot
  enter training images. Codex performs the visual review.
- [x] Produce a run summary: accepted/rejected/pending counts and reasons,
  elapsed render time, storage cost, and reproducibility limitations.

Completion: one command produces a reviewable pilot and machine validation
report; a separate explicit promotion step exports only eligible reviewed data.
Pending Codex review is an expected state, not a reason to invent approval.

## 5. Prepare training integration without launching training

- [x] Inspect the V-JEPA loader's one-clip-per-case assumption and document an
  explicit mapping from synthetic views to training samples with group identity.
- [x] Ensure positive temporal samples contain contact or have an explicit
  ambiguous/weak-label treatment; never blindly retain a positive label on a
  contact-free crop.
- [x] Provide dry-run integration checks and counts without modifying the
  real benchmark or existing evaluation splits.
- [x] Document the later real-only versus real-plus-synthetic comparison on
  the same real evaluation set, with threshold selection on separate
  development data. Do not claim synthetic accuracy establishes real benefit.

## Constraints and final handoff

- No asset purchases, rented compute, training runs, production changes, or
  automatic imports into `bench/dataset.json` in this assignment.
- Do not change visual assets, grading, camera models, animation, or render
  appearance. Coordinate any necessary shared-file edits with Codex.
- Use appropriate focused tests; rendering success alone is not label validation.
- Return implementation paths, commands, test results, remaining engineering
  limitations, and a precise queue of clips/issues requiring Codex review.
- Codex will handle appearance improvements, visual evidence decisions,
  keying/backing refinements, and visual acceptance before dataset promotion.

## Status — 2026-09-06 (Claude)

All boxes above are implemented; visual acceptance is Codex's and remains
pending. Read `sim/unreal/synthetic-dataset.md` for the design, field
semantics, gates, commands and limitations.

Implementation paths:

- `sim/unreal/Content/Python/sentrysim/contact.py` — Sequencer-faithful
  per-frame transforms, tolerances, intervals, verdicts, pair-difference check.
- `sim/unreal/blender/measure_contacts.py` — BVH distance/overlap/penetration
  on the prepared Model 3 parts (Blender headless).
- `sim/unreal/measure_contacts.py` — driver: rebuilds the rendered scenario
  from `render-review.json`, detects generator drift, writes
  `contact-{transforms,measurements,assessment}.json` and `pair-report.json`.
- `sim/unreal/synth/` — `ids`, `media` (validation, pi transcode, measured
  frame maps), `manifest` (build/merge/check/reviews), `gates`, `review`
  (queue, frames, overlays, projection), `promote` (export), `training`
  (dry-run mapping), `estimate`.
- `sim/unreal/synth_pilot.py` — `estimate | run | refresh | status | promote |
  dryrun-training`; batch config `sim/unreal/specs/pilot-door-v1.json`.
- `sim/unreal/Content/Python/sentrysim/data/model3-unreal-bounds.json` —
  Unreal-logged import bounds used by the scale check.
- `sim/unreal/scene.py` now takes the omitted neighbour door from
  `contact.omitted_parts` (one-line shared-file change; no visual effect).
- Tests: `sim/unreal/tests/test_contact.py`, `tests/test_synth.py`
  (run: `bench/analyzers/vjepa/.venv/bin/python -m pytest -q sim/unreal/tests`).

Field semantics (audit result): `scenario.*`, `contact`, `start_s`, `end_s`,
`min_gap_m`, `closest_*` in the sidecar are **staged intentions** (for door
kinds, `min_gap_m` is a constant 0/0.15, not a measurement);
`contact-assessment.json` / manifest `physical_contact` is **measured
geometry**; manifest `outputs[*].evidence` is **reviewed evidence**.
`model3.json` mixes frames: `pivot_m` is Unreal (+Y right), `bounds_m` is
Blender (+Y left) — unchanged, but the checker reflects and says so.

Engineering limitations still open: rigid touch only (no rebound-in, flex or
marks); keying/backing unmeasured; MRQ sample time assumed at frame start;
rectilinear cameras; `gap_m` is an upper bound with an 8–10 mm sampling
error bound on this mesh (intersection is exact).
