
You're right that motion magnitude is the wrong axis — the discriminator is what, where, and whether the car felt it, not how many pixels changed. Here's the full idea list, ordered by how much intelligence (and cost) each layer adds.

Tier 0 — free signals we already have and ignore

1. Tesla's reason codes, especially accelerometer events. event.json reasons include sentry_aware_object_detection and sentry_aware_accel_<g-value> — the car's shock sensor reports physical contact with magnitude. A door ding is exactly this: near-zero motion, nonzero accel. Policy: any accel event → always analyze (maybe with the better model); object-detection-only events → enter the triage funnel. This single field solves your door-ding example for free.
2. Trigger camera + timestamp (already parsed): we know which camera and when — triage only needs to look hard at ±20s on 1–2 cameras, not 6×120s.
3. Event dedup/coalescing: the same loiterer re-triggering every 2 minutes shouldn't buy 10 analyses. Hash on time-proximity + same camera; analyze once, attach repeats.

Tier 1 — classical CV, used only as a scene prefilter (not a decision-maker)

4. Background subtraction with zone masks (MOG2/KNN, ~free on CPU): not "is there motion" but "is there motion in the collision zone" — the band of pixels adjacent to the car body. Leaves across the whole frame: ignored. Anything entering the near-car band: escalate. Still dumb, but dumb in the right coordinate system.
5. Encoded-bitrate spikes (ffprobe packet sizes) only as a keyframe selector — find the interesting seconds to feed the next tier, never to drop an event outright.

Tier 2 — small NNs: the "step below LLM" you're pointing at

6. Open-vocabulary detector per sampled frame (YOLO-World / OWLv2, or plain YOLO11-nano for person/car/bicycle): runs at 1–2 fps on the server's CPU, milliseconds per frame. Leaves have no bounding box; a person does. Prompt-able classes mean we can literally ask for person near car door, shopping cart, car door open adjacent.
7. Track + dwell + approach vector (ByteTrack/Norfair over the detections): a person walking past vs stopping beside the car for 8 seconds vs approaching the door line are trivially separable from track geometry. Dwell-near-car is the single best "suspicion" feature and costs nothing extra.
8. Adjacent-car-door-swing detection (door-ding cause #1): a car-class box parked beside us + its door edge crossing into the collision zone. Detector + geometry, no LLM.
9. Pose keypoints on escalated frames (MoveNet/RTMPose-tiny): raised arm / swinging motion / crouching at wheel-level near the car — strong pre-LLM "this is an attack/theft posture" signal.
10. Embedding classifier that learns from our own verdicts (SigLIP/CLIP embedding per keyframe + logistic head): every Gemini verdict we store becomes a training label. Over weeks, the cheap head learns "this parking-lot shadow pattern is always threat_level: none" and the funnel gets tighter for free. This is the compounding-returns idea.
11. Tiny local VLM as mid-tier judge (Moondream ~2B / SmolVLM2 — run on the server, not the Pi): for gray-zone events, ask a 2B model "is anyone interacting with the silver car? yes/no + one sentence" locally at $0, and only send to Gemini on yes/unsure. Full LLM judgment quality isn't needed to say "empty street."
12. Audio impact classifier if the clip has an audio track (YAMNet-class, tiny): a door ding sounds like a door ding even when it barely registers visually. Needs a check on whether our Sentry MP4s carry audio on current firmware — worth 10 minutes to verify before counting on it.

Tier 3 — make the Gemini calls themselves cheaper

13. Cap thinking / output tokens. Empirically our cost is ~70% output tokens (841 out of 2,901, billed 6×). A thinkingConfig budget cap plus terser schema fields is the single biggest lever on the existing bill — likely 2–3× cheaper with zero pipeline changes.
14. Send keyframes, not video: triage already found the 6 interesting frames; 6 images ≪ 120s of video tokens, and flash handles "what happens across these stills" well for this use case.
15. Trim + mosaic: send only the ±10s window around the triage hit, and composite multiple relevant cameras into one grid video (ffmpeg xstack) — one video stream's tokens instead of N.
16. Two-model escalation: flash-lite-tier first pass; full flash (or pro) only if first pass says medium+ or is uncertain.
17. Batch API (50% off) for accel-negative, low-triage-score events where a 10-minute verdict delay is fine; real-time calls reserved for accel hits and high triage scores.
18. Daily budget guard in the server: hard spend cap per day; past it, degrade to record + triage-only, never silently overrun (we now have the token accounting to enforce this).

Recommended stack (if you want my pick)

Accel events → straight to Gemini. Everything else → detector + track/dwell (ideas 6–8) → tiny-VLM tiebreak (11) → trimmed keyframes to Gemini (13–15). That keeps ~all real threats, drops the wind-and-shadows majority at $0, and cuts the per-escalated-event Gemini cost several-fold. All of it runs server-side; the Pi Zero stays dumb.

TL;DR: Use the car's accelerometer reason code for contact events like door dings (free, already in event.json), a person/vehicle detector with dwell-near-car logic — not motion magnitude — as the main gate, optionally a ~2B local VLM as a mid-tier judge, and cap Gemini's thinking tokens since output is 70% of today's bill.

Sources: TMC — event.json reason codes, Roboflow — local VLMs, LearnOpenCV — VLMs on edge
