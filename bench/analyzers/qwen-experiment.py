#!/usr/bin/env python3
"""Qwen3.8-27B experiment analyzer for teslcam-bench.

Same -analyzer contract as openrouter-analyze.py (clip paths as final args,
one JSON verdict on stdout), but with selectable strategies:

  --strategy single   one video call with the prompt in --prompt-file
  --strategy twostep  video -> motion timeline JSON, then a text-only judge
                      applies the contact rules to the timeline
  --strategy bbox     frames at --frame-fps -> per-frame bounding boxes of the
                      nearest object + visible ego-car body, contact decided
                      deterministically in code from the gap series, then a
                      final verdict assembled without another model opinion

Every strategy prints the summed usage of all calls it made, so the bench's
cost accounting covers multi-step runs.
"""

import argparse
import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

API_URL = "https://openrouter.ai/api/v1/chat/completions"
# Gemini's OpenAI-compatible endpoint, selectable via --api-url so the same
# strategies/prompts can be A/B'd across providers with nothing else changed.
GEMINI_COMPAT_URL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"

VERDICT_SCHEMA = {
    "type": "object",
    "properties": {
        "description": {
            "type": "string",
            "description": "What happened in the clip, one or two sentences.",
        },
        "evidence": {
            "type": "string",
            "description": "The exact second and observable event that proves contact, or the strongest near-miss and why it fails the contact test.",
        },
        "contact": {
            "type": "boolean",
            "description": "Did anything physically touch the car — a person, a door, a cart, an animal, anything?",
        },
        "start_seconds": {"type": "integer"},
        "end_seconds": {"type": "integer"},
        "threat": {"type": "string", "enum": ["none", "low", "high"]},
    },
    "required": ["description", "evidence", "contact", "start_seconds", "end_seconds", "threat"],
    "additionalProperties": False,
}

PROB_SCHEMA = {
    "type": "object",
    "properties": {
        "description": VERDICT_SCHEMA["properties"]["description"],
        "evidence": VERDICT_SCHEMA["properties"]["evidence"],
        "contact_probability": {
            "type": "number",
            "description": "Honest probability in [0,1] that something physically touched the car. Use the full range; 0.5 means truly uncertain. Do not round to 0 or 1 unless the evidence is decisive.",
        },
        "start_seconds": {"type": "integer"},
        "end_seconds": {"type": "integer"},
        "threat": {"type": "string", "enum": ["none", "low", "high"]},
    },
    "required": ["description", "evidence", "contact_probability", "start_seconds", "end_seconds", "threat"],
    "additionalProperties": False,
}


class Usage:
    def __init__(self):
        self.prompt = 0
        self.output = 0
        self.total = 0
        self.cost = 0.0
        self.model = ""

    def add(self, resp):
        u = resp.get("usage") or {}
        self.prompt += u.get("prompt_tokens", 0)
        self.output += u.get("completion_tokens", 0)
        self.total += u.get("total_tokens", 0)
        self.cost += u.get("cost", 0) or 0
        self.model = resp.get("model") or self.model


def post(url, body, api_key, timeout, attempts, backoff):
    data = json.dumps(body).encode("utf-8")
    last = None
    for attempt in range(1, attempts + 1):
        req = urllib.request.Request(
            url,
            data=data,
            headers={
                "Authorization": "Bearer " + api_key,
                "Content-Type": "application/json",
                "X-Title": "teslcam-bench-exp",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", "replace")[:2000]
            last = "HTTP %d: %s" % (e.code, detail)
            if e.code != 429 and e.code < 500:
                break
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            last = "%s" % e
        if attempt < attempts:
            print("attempt %d failed (%s), retrying" % (attempt, last), file=sys.stderr)
            time.sleep(backoff * attempt)
    raise SystemExit("openrouter request failed after %d attempt(s): %s" % (attempts, last))


def call(args, usage, content, schema=None, schema_name="out", max_tokens=None):
    body = {
        "model": args.model,
        "messages": [{"role": "user", "content": content}],
        "max_tokens": max_tokens or args.max_tokens,
        "temperature": args.temperature,
    }
    if "openrouter" in args.api_url:
        body["usage"] = {"include": True}
        # OpenRouter-specific extension; other OpenAI-compatible endpoints
        # (Gemini) reject or ignore it, and Gemini flash has no toggle anyway.
        if args.reasoning == "off":
            body["reasoning"] = {"enabled": False}
        else:
            body["reasoning"] = {"effort": args.reasoning}
    if schema is not None:
        body["response_format"] = {
            "type": "json_schema",
            "json_schema": {"name": schema_name, "strict": True, "schema": schema},
        }
    if args.provider:
        body["provider"] = {"order": [args.provider], "allow_fallbacks": False}
    resp = post(args.api_url, body, os.environ[args.key_env], args.timeout, args.attempts, args.backoff)
    usage.add(resp)
    choices = resp.get("choices") or []
    if not choices:
        raise SystemExit("no choices in response: " + json.dumps(resp)[:2000])
    text = (choices[0].get("message") or {}).get("content") or ""
    if not text.strip():
        raise SystemExit("empty content (finish_reason=%r)" % choices[0].get("finish_reason"))
    return text


def parse_json(text):
    text = text.strip()
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    m = re.search(r"\{.*\}", text, re.DOTALL)
    if not m:
        raise SystemExit("no JSON object in model output:\n" + text[:2000])
    return json.loads(m.group(0))


def video_part(path):
    with open(path, "rb") as f:
        b64 = base64.b64encode(f.read()).decode("ascii")
    return {"type": "video_url", "video_url": {"url": "data:video/mp4;base64," + b64}}


def image_part(path):
    with open(path, "rb") as f:
        b64 = base64.b64encode(f.read()).decode("ascii")
    return {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64," + b64}}


def extract_frames(clip, fps, max_width):
    """Decode the clip to timestamped JPEGs; returns [(seconds, path)]."""
    tmpdir = tempfile.mkdtemp(prefix="qwenexp-")
    pattern = os.path.join(tmpdir, "f%04d.jpg")
    subprocess.run(
        [
            "ffmpeg", "-v", "error", "-i", clip,
            "-vf", "fps=%g,scale='min(%d,iw)':-2" % (fps, max_width),
            "-q:v", "4", pattern,
        ],
        check=True,
    )
    frames = sorted(os.listdir(tmpdir))
    # ffmpeg's fps filter emits frame N at timestamp N/fps (first at 0).
    return [(i / fps, os.path.join(tmpdir, f)) for i, f in enumerate(frames)]


# ---------------------------------------------------------------- strategies


def run_single(args):
    usage = Usage()
    with open(args.prompt_file) as f:
        prompt = f.read()
    content = [{"type": "text", "text": prompt}]
    for clip in args.clips:
        content.append(video_part(clip))
    verdict = parse_json(call(args, usage, content, VERDICT_SCHEMA, "sentry_verdict"))
    return verdict, usage


def run_frames(args):
    """The single-shot prompt, but over self-extracted timestamped frames
    instead of the provider's video pipeline — the provider samples video too
    sparsely to catch a door-arc stop or a bumper kiss."""
    usage = Usage()
    with open(args.prompt_file) as f:
        prompt = f.read()
    frames = extract_frames(args.clips[0], args.frame_fps, args.frame_width)
    if len(frames) > args.max_frames:
        step = len(frames) / args.max_frames
        frames = [frames[int(i * step)] for i in range(args.max_frames)]
    content = [{"type": "text", "text": prompt + "\n\nThe clip is given as timestamped frames, %.1f per second, in order:" % args.frame_fps}]
    for t, path in frames:
        content.append({"type": "text", "text": "frame t=%.1fs:" % t})
        content.append(image_part(path))
    schema = PROB_SCHEMA if args.probability else VERDICT_SCHEMA
    verdict = parse_json(call(args, usage, content, schema, "sentry_verdict"))
    if args.probability:
        # The bench scores the boolean; keep the raw probability alongside so
        # stored runs can be re-thresholded without re-paying the API.
        verdict["contact"] = verdict.get("contact_probability", 0) >= args.contact_threshold
    verdict["frame_count"] = len(frames)
    return verdict, usage


TIMELINE_SCHEMA = {
    "type": "object",
    "properties": {
        "camera_position": {
            "type": "string",
            "description": "Where on the ego car this camera appears to be mounted, and which parts of the ego car's own body are visible at the frame edges.",
        },
        "objects": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "name": {"type": "string"},
                    "first_seen_seconds": {"type": "integer"},
                    "last_seen_seconds": {"type": "integer"},
                    "motion": {
                        "type": "string",
                        "description": "Second-by-second movement relative to the ego car: approach, direction, speed changes, where the motion ENDS.",
                    },
                    "closest_approach": {
                        "type": "string",
                        "description": "At its closest, how near was it and for how long? Did it keep moving through that closest point at constant speed, or did its motion stop/pause there?",
                    },
                    "max_frame_fill": {
                        "type": "string",
                        "description": "At its largest, roughly what fraction of the frame did it fill, and did it reach the bottom edge / corners with fisheye distortion?",
                    },
                    "arrived_or_static": {
                        "type": "string",
                        "enum": ["arrived_during_clip", "static_whole_clip", "departed_during_clip", "passed_through"],
                    },
                },
                "required": ["name", "first_seen_seconds", "last_seen_seconds", "motion", "closest_approach", "max_frame_fill", "arrived_or_static"],
                "additionalProperties": False,
            },
        },
        "camera_shake": {
            "type": "string",
            "description": "Any second where the image jolts, shakes, or shifts, and what coincided with it. 'none' if steady.",
        },
    },
    "required": ["camera_position", "objects", "camera_shake"],
    "additionalProperties": False,
}

OBSERVE_PROMPT = """This is a clip from a fisheye camera mounted ON the body of a parked car
(Tesla Sentry Mode). Parts of the car the camera is mounted on are visible at
the frame edges (typically the bottom edge or a dark strip along one side).
Neighboring parked cars routinely look very large in this lens while being a
normal parking gap away.

Report a precise motion timeline. Do not judge or interpret — only observe.
For every distinct moving object (person, vehicle, door, cart, animal) report
its motion second by second, exactly where that motion ends, its closest
approach (and whether it moved THROUGH the closest point at constant speed or
stopped/paused there), and how much of the frame it filled at its largest.
Note any second the image itself jolts or shakes. Report only what is visible;
if you are unsure whether something happened, say so in the field rather than
guessing."""

JUDGE_PROMPT = """You are judging whether anything physically TOUCHED a parked car, from a
motion timeline observed by a fisheye camera mounted on that car's own body.
The timeline is the only evidence; the exact touch point is essentially never
visible, so contact must be inferred from motion. Apply these rules strictly:

CONTACT happened only if at least one of these is in the timeline:
1. An object's motion STOPS at the ego car's position and it stays there —
   a door swings open and its arc stops while the occupant is still getting
   in/out (the door met this car); a person leans/rests/presses on it; a foot
   or hand is placed down on it and bears weight.
2. An object arrives during the clip and grows until it fills the frame to
   the bottom edge/corners with fisheye distortion — closer than any normal
   parking gap (a vehicle pressing against the car's body).
3. The image jolts or shakes at the moment an object arrives at or leaves the
   ego car.

NOT contact — answer contact=false if the timeline only shows:
- People or vehicles passing at constant, uninterrupted speed, however close.
- A neighboring parked car that is large in frame but static, or that
  arrives/leaves with a normal parking gap and no frame-fill and no jolt.
- People getting into or out of a NEIGHBORING car whose door never has its
  arc interrupted.
- Proximity, "probably brushed", or anything the timeline does not state.

threat: "none" if no contact and no threat; "low" for light contact without
damage risk (a touch, a lean, a resting door); "high" for forceful or
potentially damaging contact (a door swung into the car, a vehicle pressing
into it, a strike) or deliberate interference.

In "evidence", cite the timeline line and second that decided contact, or the
strongest near-miss and which rule it failed. start/end_seconds bound the main
event.

The timeline:

"""


def run_twostep(args):
    usage = Usage()
    content = [{"type": "text", "text": OBSERVE_PROMPT}]
    for clip in args.clips:
        content.append(video_part(clip))
    timeline = parse_json(call(args, usage, content, TIMELINE_SCHEMA, "timeline"))
    judge_content = [
        {"type": "text", "text": JUDGE_PROMPT + json.dumps(timeline, indent=1)}
    ]
    verdict = parse_json(call(args, usage, judge_content, VERDICT_SCHEMA, "sentry_verdict"))
    verdict["timeline"] = timeline
    return verdict, usage


BBOX_SCHEMA = {
    "type": "object",
    "properties": {
        "frames": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "t": {"type": "number", "description": "the frame's timestamp in seconds, copied from its label"},
                    "ego_bbox": {
                        "type": "array", "items": {"type": "integer"},
                        "description": "bbox_2d [x1,y1,x2,y2] in 0-1000 coords of the visible body of the car the camerara is mounted on (the near-field strip at the frame edge); [0,0,0,0] if none visible",
                    },
                    "objects": {
                        "type": "array",
                        "items": {
                            "type": "object",
                            "properties": {
                                "label": {"type": "string", "description": "stable name for this object, identical across frames (e.g. 'suv door', 'person A')"},
                                "bbox_2d": {"type": "array", "items": {"type": "integer"}},
                                "moving": {"type": "boolean", "description": "is it in motion at this timestamp (vs static/parked)"},
                            },
                            "required": ["label", "bbox_2d", "moving"],
                            "additionalProperties": False,
                        },
                        "description": "every person, vehicle, door, cart or animal within ~2 meters of the ego car; empty if none",
                    },
                },
                "required": ["t", "ego_bbox", "objects"],
                "additionalProperties": False,
            },
        },
        "camera_shake_seconds": {
            "type": "array", "items": {"type": "number"},
            "description": "timestamps where the image jolts or shifts abruptly; empty if steady",
        },
    },
    "required": ["frames", "camera_shake_seconds"],
    "additionalProperties": False,
}

BBOX_PROMPT = """These are timestamped frames from a fisheye camera mounted ON the body of a
parked car (Tesla Sentry Mode). The car's own body is visible as a near-field
strip along a frame edge (often the bottom, sometimes a dark strip on one
side). For EVERY frame, in order, report:
- ego_bbox: the visible ego-car body strip, [x1,y1,x2,y2] in 0-1000 coords
- objects: every person, vehicle, vehicle DOOR (track an opening door as its
  own object), cart or animal within about 2 meters of the ego car, each with
  a stable label reused across frames, its bbox_2d, and whether it is moving
  at that instant.
Also list timestamps where the whole image jolts or shifts (camera knock).
Be precise about bbox edges — decisions are computed from pixel gaps between
your boxes. Output one frames[] entry per input frame, same order."""


def bbox_gap(a, b):
    """Min axis-aligned gap between two [x1,y1,x2,y2] boxes; 0 if overlapping."""
    ax1, ay1, ax2, ay2 = a
    bx1, by1, bx2, by2 = b
    dx = max(bx1 - ax2, ax1 - bx2, 0)
    dy = max(by1 - ay2, ay1 - by2, 0)
    return max(dx, dy) if (dx == 0 or dy == 0) else (dx * dx + dy * dy) ** 0.5


def area(b):
    return max(0, b[2] - b[0]) * max(0, b[3] - b[1])


def run_bbox(args):
    usage = Usage()
    frames = extract_frames(args.clips[0], args.frame_fps, args.frame_width)
    if len(frames) > args.max_frames:
        step = len(frames) / args.max_frames
        frames = [frames[int(i * step)] for i in range(args.max_frames)]
    content = [{"type": "text", "text": BBOX_PROMPT}]
    for t, path in frames:
        content.append({"type": "text", "text": "frame t=%.1fs:" % t})
        content.append(image_part(path))
    data = parse_json(call(args, usage, content, BBOX_SCHEMA, "bbox_track", max_tokens=8000))

    # Deterministic contact: an object track whose gap to the ego strip hits 0
    # while it was moving and had been >0 earlier (arrival), or that grows to
    # fill most of the frame, or a reported camera jolt.
    tracks = {}
    for fr in data.get("frames", []):
        ego = fr.get("ego_bbox") or [0, 0, 0, 0]
        for obj in fr.get("objects", []):
            tracks.setdefault(obj["label"], []).append(
                (fr.get("t", 0), obj["bbox_2d"], obj.get("moving", False), ego)
            )
    contact, reasons, window = False, [], []
    for label, points in tracks.items():
        gaps = []
        for t, bb, moving, ego in points:
            g = bbox_gap(bb, ego) if area(ego) > 0 else None
            gaps.append((t, g, moving, area(bb)))
        had_distance = any(g is not None and g > 30 for _, g, _, _ in gaps)
        touching = [(t, g) for t, g, moving, _ in gaps if g is not None and g <= 2]
        moving_touch = [
            (t, g) for t, g, moving, _ in gaps if g is not None and g <= 2 and moving
        ]
        big = [(t, a) for t, _, _, a in gaps if a >= 700 * 1000 * 0.8]
        if had_distance and moving_touch:
            contact = True
            reasons.append("%s: moved from a distance to gap 0 at t=%.0fs" % (label, moving_touch[0][0]))
            window += [t for t, _ in moving_touch]
        elif len(touching) >= 2 and any(m for _, g, m, _ in gaps):
            contact = True
            reasons.append("%s: dwells at gap 0 across %d frames" % (label, len(touching)))
            window += [t for t, _ in touching]
        elif big:
            contact = True
            reasons.append("%s: fills >80%% of the frame at t=%.0fs" % (label, big[0][0]))
            window += [t for t, _ in big]
    shakes = data.get("camera_shake_seconds") or []
    if shakes:
        contact = True
        reasons.append("camera jolt at %s" % ", ".join("%.0fs" % s for s in shakes))
        window += shakes

    threat = "none"
    if contact:
        # Vehicle-sized boxes or a jolt read as forceful; anything else light.
        forceful = bool(shakes) or any("fills" in r for r in reasons)
        threat = "high" if forceful else "low"
    start = int(min(window)) if window else 0
    end = int(max(window)) if window else int(frames[-1][0])
    verdict = {
        "description": "; ".join(reasons) if reasons else "No tracked object closed its gap to the ego car; no camera jolt.",
        "evidence": json.dumps({k: [(t, round(g) if g is not None else None) for t, g, _, _ in [(t, bbox_gap(bb, ego) if area(ego) > 0 else None, m, area(bb)) for t, bb, m, ego in v]] for k, v in tracks.items()})[:1500],
        "contact": contact,
        "start_seconds": start,
        "end_seconds": end,
        "threat": threat,
        "bbox_frames": len(frames),
    }
    return verdict, usage


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--strategy", required=True, choices=["single", "frames", "twostep", "bbox"])
    ap.add_argument("--model", required=True)
    ap.add_argument("--provider")
    ap.add_argument("--prompt-file", help="single strategy: the analysis prompt")
    ap.add_argument("--reasoning", required=True, choices=["off", "low", "medium", "high"])
    ap.add_argument("--max-tokens", type=int, required=True)
    ap.add_argument("--temperature", type=float, required=True)
    ap.add_argument("--timeout", type=float, required=True)
    ap.add_argument("--attempts", type=int, required=True)
    ap.add_argument("--backoff", type=float, required=True)
    ap.add_argument("--api-url", default=API_URL, help="OpenAI-compatible chat completions URL; 'gemini' selects the Gemini compat endpoint")
    ap.add_argument("--key-env", default="OPENROUTER_API_KEY", help="env var holding the API key for --api-url")
    ap.add_argument("--probability", action="store_true", help="frames strategy: ask for contact_probability instead of a boolean")
    ap.add_argument("--contact-threshold", type=float, default=0.5, help="probability at or above which contact is called")
    ap.add_argument("--votes", type=int, default=1, help="run the strategy N times and take the majority contact call")
    ap.add_argument("--frame-fps", type=float, default=2.0, help="bbox strategy: frames per second")
    ap.add_argument("--frame-width", type=int, default=640)
    ap.add_argument("--max-frames", type=int, default=40)
    ap.add_argument("clips", nargs="+")
    args = ap.parse_args()

    if args.api_url == "gemini":
        args.api_url = GEMINI_COMPAT_URL
        if args.key_env == "OPENROUTER_API_KEY":
            args.key_env = "GEMINI_API_KEY"
    if not os.environ.get(args.key_env):
        raise SystemExit("%s is not set" % args.key_env)
    if args.strategy in ("single", "frames") and not args.prompt_file:
        raise SystemExit("--strategy %s requires --prompt-file" % args.strategy)

    run = {"single": run_single, "frames": run_frames, "twostep": run_twostep, "bbox": run_bbox}[
        args.strategy
    ]
    if args.votes == 1:
        verdict, usage = run(args)
    else:
        # Majority vote: the fp8 endpoint is not deterministic even at
        # temperature 0, so single samples flip borderline cases run to run.
        # Contact is the majority call; threat is the worst level among the
        # winning side (an alert system should not average away severity);
        # description/evidence come from the first winning vote.
        usage = Usage()
        votes = []
        for _ in range(args.votes):
            v, u = run(args)
            votes.append(v)
            usage.prompt += u.prompt
            usage.output += u.output
            usage.total += u.total
            usage.cost += u.cost
            usage.model = u.model or usage.model
        rank = {"none": 0, "low": 1, "medium": 2, "high": 3}
        if args.probability:
            # FN-averse aggregation: a missed ding costs far more than a
            # needless alert, so the alerting probability is the MAX across
            # samples — any sample that saw the contact pattern raises it.
            # (Majority would let two blind samples outvote the one that saw.)
            best = max(votes, key=lambda v: v.get("contact_probability", 0))
            verdict = dict(best)
            verdict["contact"] = verdict.get("contact_probability", 0) >= args.contact_threshold
            if verdict["contact"]:
                touched = [v for v in votes if v.get("contact_probability", 0) >= args.contact_threshold]
                worst = max(touched, key=lambda v: rank.get(v.get("threat"), 0))
                verdict["threat"] = worst["threat"]
        else:
            yes = [v for v in votes if v.get("contact")]
            no = [v for v in votes if not v.get("contact")]
            winners = yes if len(yes) > len(no) else no
            worst = max(winners, key=lambda v: rank.get(v.get("threat"), 0))
            verdict = dict(winners[0])
            verdict["threat"] = worst["threat"]
        verdict["votes"] = [
            {"contact": v.get("contact"), "contact_probability": v.get("contact_probability"), "threat": v.get("threat")} for v in votes
        ]

    verdict["usage"] = {
        "model": usage.model or args.model,
        "prompt_tokens": usage.prompt,
        "output_tokens": usage.output,
        "total_tokens": usage.total,
    }
    verdict["openrouter_cost_usd"] = usage.cost
    json.dump(verdict, sys.stdout)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
