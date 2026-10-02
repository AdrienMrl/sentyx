# Camera rig calibration (Tesla HW4 Sentry cameras)

Goal: per-camera position and orientation good enough to render synthetic Sentry
clips for training, for Model 3 and Model Y. Target accuracy agreed up front:
**±1° per rotation axis, ±5 cm in position (±3 cm height)**, intrinsics within ~1%.

Everything here is derived from the three field events in `bench/clips/pi-*`
(same car, HW4: front 2896×1876, the other five 1448×938). No calibration target
was available, so the scene itself had to supply the geometry.

## Method

1. **Line segments** are detected with LSD, including a pass on a large-kernel
   top-hat image so faded parking-lot paint is picked up (`tools/manhattan.py`).
2. **Camera model**: fisheye equidistant with a polynomial radial term
   (`tools/cammodel.py`). With k = (1/3, 2/15, …) it reproduces a pinhole exactly,
   so the same model covers the narrow front lens and the wide rear lens.
3. **Orientation**: a segment's two unprojected endpoints span a plane through the
   camera centre containing the world line, so short chords of a curved fisheye
   line are still exact constraints. Lines known to share a world direction
   (building edges, posts, painted lines) are fitted for focal length, distortion
   and rotation (`tools/fitlabeled.py`).
4. **Scale**: with the vertical direction known, two ground points whose real
   separation is known give the camera height. Adjacent parked cars supplied those
   baselines (wheelbase or track of an identified model).
5. **Proof**: `tools/render.py` projects a dimensionally accurate car wireframe and
   a 1 m ground grid back into the real frame.

## What is actually measured

| Quantity | Value | How |
|---|---|---|
| B-pillar camera field of view | 89.3° horizontal (f = 935 px) | Manhattan fit, two events agree (933 / 947) |
| B-pillar camera height | 1.32 m | adjacent Model 3's 2.875 m wheelbase |
| B-pillar yaw / pitch / roll | −71.0° / −6.4° / −0.8° | vertical posts + that car's wheel axis |
| Model 3 fender repeater mount | 0.41 m behind the front axle, 0.78 m high | measured on the adjacent Model 3's own camera |
| Model 3 B-pillar camera mount | 1.11 m ahead of the rear axle, ~1.24–1.32 m high | same |
| Repeater field of view | 85.1° horizontal (f = 934 px) | ground registration + 4 shared points |
| Repeater mount | 2.65 m ahead of the rear axle, 0.89 m out, 0.77 m high | same solve |
| Repeater yaw / pitch / roll | ∓149.2° / −3.0° / ~0° | same solve, reprojection 1–2 px |
| Rear camera pitch | ≈ −27° | painted ground lines + Trax bumper lines |
| Rear camera height | ≈ 0.88 m | Trax rear track, corrected by its roof height |

Independent check on the pillar solve: the adjacent car's beltline came out at
97.3 cm against a real 98 cm, and its roof at 140 cm against 144 cm.

The measured 1.32 m B-pillar height matches a Model 3, not a Model Y, and agrees
with the mounting height measured directly on the neighbouring Model 3.

## What is not measured

**Front camera.** A forward-looking, near-level, narrow lens makes the focal length
and the vertical vanishing point nearly degenerate: three different pairs of sign
posts gave pitches from -0.4 to +6.9 degrees. The front view also had no usable
metric baseline, because every parked car's wheels are hidden behind its own
bumper and wheel stop. Position comes from body geometry, FOV from Tesla's
published figure.

**Repeaters — now solved.** The first attempt leaned on a single cross-camera tie
and landed 8 degrees off. What fixed it was two things together. First, warping the
ground plane from both the pillar and the repeater into a common bird's-eye view and
maximising their correlation: the asphalt texture, cracks and shadows register at
NCC 0.77 over about 7 m². That alone leaves a slide along the narrow overlap band, so
second, four points shared between the two views were added — the neighbouring car's
tyre contact patch, its hub centre, and both ends of its rear door handle. Those span
0 to 0.97 m in height, which is what breaks the focal-length-versus-height degeneracy.
All four reproject within 2 px. One trap on the way: the rear door handle had to be
matched to the *rear* handle in both frames, and picking the front one in one view
put the solution 200 px out.

A useful check fell out of it. The door handle, located in 3D purely from the pillar
camera, came out 96 cm above the ground and 18 cm long. A Model 3's beltline sits at
about 98 cm and its flush handle is roughly 18 cm.

## Closing the gap

Two things would take the whole rig inside tolerance in about fifteen minutes:

1. Tape-measure each camera's height, its distance behind the front axle and its
   offset from the centreline. That beats any image-based estimate.
2. Record one Sentry event with chalk marks or a tape laid on the ground at
   measured offsets around the car, visible in every camera. With ground points at
   known positions, `cv2.fisheye` solves every extrinsic and the distortion
   properly, in one pass.

## Files

- `rig.json` — the rig for Model 3 (measured/derived) and Model Y (scaled).
- `out/proof_right_pillar.png` — CGI Model 3 + 1 m ground grid over the real frame.
- `tools/` — camera model, line fitting, plane fitting, renderer.
