# TeslaCam camera scorer

This Pi-side helper combines NanoDet-Plus detections with class-agnostic frame
changes. The latter is required for unknown objects, impacts, occlusion, and
objects falling onto or near the car.

It links the Apache-2.0 NanoDet NCNN demo and BSD-3-Clause NCNN. Build it on the
target Pi after installing OpenCV and building NCNN:

```sh
cmake -S tools/camera-scorer -B build/camera-scorer \
  -DNANODET_DIR=$HOME/nanodet \
  -Dncnn_DIR=$HOME/ncnn/install/lib/cmake/ncnn
cmake --build build/camera-scorer -j2
```

The executable emits one JSON score object for a video window. It is an
implementation detail of the Go agent and is not a stable user-facing CLI.

