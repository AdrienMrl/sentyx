# TeslaCam camera scorer

Pi-side helper that scores one camera's clip window with class-agnostic
pixel-change signals (motion, novelty, occlusion). There is deliberately no
neural network: the Pi's CPU/thermal budget is the binding constraint in a hot
parked car, so the scorer only decides which clips look interesting enough to
upload; the real relevance judgment happens server-side (Gemini).

Decoding samples H.264 keyframes only (~2/s in Tesla clips) via an ffmpeg
pipe, which cuts scoring CPU several-fold versus full decode. The Pi's
hardware H.264 decoder was measured slower and hotter for this workload and
cannot handle the front camera's resolution, so decode is software everywhere.

Build on the target Pi after installing OpenCV (ffmpeg is required at
runtime):

```sh
cmake -S tools/camera-scorer -B build/camera-scorer
cmake --build build/camera-scorer -j2
```

The executable emits one JSON score object for a video window. It is an
implementation detail of the Go agent and is not a stable user-facing CLI.
