#!/usr/bin/env python3
"""External bench analyzer that scores a video model hosted on OpenRouter.

teslcam-bench's -analyzer contract: the clip paths are appended as the final
arguments (best-ranked first) and a single JSON verdict object goes to stdout.
A top-level "usage" key is lifted out by the Go side as TokenUsage.

    scripts/run-bench.sh run -analyzer \
        "python3 bench/analyzers/openrouter-analyze.py --model qwen/qwen3.8-27b"

The prompt is copied verbatim from analyzePrompt() in internal/gemini/gemini.go
so the only variable between a Gemini run and this one is the model. If that
prompt changes, change this copy in the same commit or the comparison is void.
"""

import argparse
import base64
import json
import mimetypes
import os
import sys
import time
import urllib.error
import urllib.request

API_URL = "https://openrouter.ai/api/v1/chat/completions"


def analyze_prompt(clip_count):
    if clip_count > 1:
        intro = (
            "These are simultaneous Tesla Sentry Mode clips of one event from different\n"
            "cameras on the same parked car."
        )
    else:
        intro = "This is one Tesla Sentry Mode camera clip from a parked car."
    return intro + """ The camera is mounted on the car itself.

Decide whether anything physically touched the car: a person, a door, a cart,
another vehicle, an animal — anything.

Critical: the exact point of touch is usually NOT visible in the frame. The
camera sits on the car's body, so an object touching the car does it at the
edge of or below the image, often while blocking the view. You will almost
never see the touch itself — you must infer it from motion:

- A car door swings open toward this car and its arc STOPS while the occupant
  is still getting in or out: the door stopped because it met this car. That
  is contact. A door that swings, stops short, and swings back without pausing
  did not touch.
- A person squeezing into or out of an adjacent car through a partially
  opened door, in a space too tight for the door to open fully: what stopped
  that door is this car. Contact.
- Another vehicle positioned against this car — its body meeting the bottom
  or edge of the frame, bumper to bumper, closer than any driver would park —
  or the image jolting as that vehicle arrives or leaves: contact.
- An object's motion ends against the car's position and it stays there — a
  resting door, a leaning person, a foot or hand placed down: contact.

Do not infer contact from proximity alone: people frequently walk or squeeze
past within inches and touch nothing. Passing close with uninterrupted motion
is NOT contact, and "probably brushed it" is not an observation — unless you
saw motion stop against the car, something rest or press on it, or the image
jolt, answer no contact.

threat: "none" if nothing touched the car and nothing threatened it; "low" for
light contact without damage risk (a touch, a brush, a foot, a resting door);
"high" for forceful or potentially damaging contact (a door swung into the
car, a collision, a strike) or deliberate interference with it."""


# Same five fields the Gemini client constrains, in JSON Schema rather than the
# Gemini dialect. strict=true so a provider that honors structured outputs
# cannot return prose around the object.
VERDICT_SCHEMA = {
    "type": "object",
    "properties": {
        "description": {
            "type": "string",
            "description": "What happened in the clip, one or two sentences.",
        },
        "contact": {
            "type": "boolean",
            "description": "Did anything physically touch the car — a person, a door, a cart, an animal, anything?",
        },
        "start_seconds": {
            "type": "integer",
            "description": "When the event begins, seconds from clip start.",
        },
        "end_seconds": {
            "type": "integer",
            "description": "When the event ends, seconds from clip start.",
        },
        "threat": {
            "type": "string",
            "enum": ["none", "low", "high"],
            "description": "Severity of any threat or damage to the car.",
        },
    },
    "required": ["description", "contact", "start_seconds", "end_seconds", "threat"],
    "additionalProperties": False,
}


def data_url(path):
    mime, _ = mimetypes.guess_type(path)
    if mime is None or not mime.startswith("video/"):
        # The bench stores blobs extensionless in some paths; the model needs a
        # media type either way, and every clip in this dataset is MP4.
        mime = "video/mp4"
    with open(path, "rb") as f:
        return "data:%s;base64,%s" % (mime, base64.b64encode(f.read()).decode("ascii"))


def build_request(model, clips, max_tokens, temperature, reasoning):
    content = [{"type": "text", "text": analyze_prompt(len(clips))}]
    for path in clips:
        content.append({"type": "video_url", "video_url": {"url": data_url(path)}})
    body = {
        "model": model,
        "messages": [{"role": "user", "content": content}],
        "max_tokens": max_tokens,
        "temperature": temperature,
        "response_format": {
            "type": "json_schema",
            "json_schema": {
                "name": "sentry_verdict",
                "strict": True,
                "schema": VERDICT_SCHEMA,
            },
        },
        "usage": {"include": True},
    }
    # Qwen3.8 is a hybrid-thinking model: left on, it spends the whole token
    # budget reasoning and returns empty content (finish_reason=length). The
    # level is an explicit choice per run, never inferred, because it changes
    # both the verdict and the bill.
    if reasoning == "off":
        body["reasoning"] = {"enabled": False}
    else:
        body["reasoning"] = {"effort": reasoning}
    return body


def post(body, api_key, timeout, attempts, backoff):
    data = json.dumps(body).encode("utf-8")
    last = None
    for attempt in range(1, attempts + 1):
        req = urllib.request.Request(
            API_URL,
            data=data,
            headers={
                "Authorization": "Bearer " + api_key,
                "Content-Type": "application/json",
                "X-Title": "teslcam-bench",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", "replace")[:2000]
            last = "HTTP %d: %s" % (e.code, detail)
            # 429 and 5xx are the provider being busy, not a bad request.
            if e.code != 429 and e.code < 500:
                break
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            last = "%s" % e
        if attempt < attempts:
            print("attempt %d failed (%s), retrying" % (attempt, last), file=sys.stderr)
            time.sleep(backoff * attempt)
    raise SystemExit("openrouter request failed after %d attempt(s): %s" % (attempts, last))


def extract_verdict(text):
    """Parse the model's message content as the verdict object.

    Providers that ignore strict structured output still tend to emit the
    object, sometimes fenced or with a sentence around it, so recover the
    outermost braces rather than failing the whole case.
    """
    text = text.strip()
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    start, end = text.find("{"), text.rfind("}")
    if start == -1 or end <= start:
        raise SystemExit("model did not return a JSON object:\n" + text[:2000])
    return json.loads(text[start : end + 1])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True, help="OpenRouter model id, e.g. qwen/qwen3.8-27b")
    ap.add_argument("--provider", help="pin one OpenRouter provider, e.g. AkashML")
    ap.add_argument(
        "--reasoning",
        required=True,
        choices=["off", "low", "medium", "high"],
        help="thinking budget; 'off' disables it entirely",
    )
    ap.add_argument("--max-tokens", type=int, required=True)
    ap.add_argument("--temperature", type=float, required=True)
    ap.add_argument("--timeout", type=float, required=True, help="seconds for one HTTP call")
    ap.add_argument("--attempts", type=int, required=True)
    ap.add_argument("--backoff", type=float, required=True, help="seconds, multiplied by attempt number")
    ap.add_argument("clips", nargs="+")
    args = ap.parse_args()

    api_key = os.environ.get("OPENROUTER_API_KEY")
    if not api_key:
        raise SystemExit("OPENROUTER_API_KEY is not set")

    body = build_request(
        args.model, args.clips, args.max_tokens, args.temperature, args.reasoning
    )
    if args.provider:
        body["provider"] = {"order": [args.provider], "allow_fallbacks": False}

    resp = post(body, api_key, args.timeout, args.attempts, args.backoff)
    choices = resp.get("choices") or []
    if not choices:
        raise SystemExit("no choices in response: " + json.dumps(resp)[:2000])
    message = choices[0].get("message") or {}
    text = message.get("content") or ""
    if not text.strip():
        raise SystemExit("empty content (finish_reason=%r)" % choices[0].get("finish_reason"))

    verdict = extract_verdict(text)

    usage = resp.get("usage") or {}
    verdict["usage"] = {
        "model": resp.get("model") or args.model,
        "prompt_tokens": usage.get("prompt_tokens", 0),
        "output_tokens": usage.get("completion_tokens", 0),
        "total_tokens": usage.get("total_tokens", 0),
    }
    # OpenRouter bills in credits and reports the charge per generation; keep
    # it so a run's real dollar cost does not have to be reconstructed later.
    if "cost" in usage:
        verdict["openrouter_cost_usd"] = usage["cost"]
    if resp.get("provider"):
        verdict["openrouter_provider"] = resp["provider"]

    json.dump(verdict, sys.stdout)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
