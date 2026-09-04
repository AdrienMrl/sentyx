# Sentry-analysis benchmark

A labeled set of real sentry events, replayed through the analyzer so a change
to the prompt, model, or detail level can be judged on footage instead of on
one clip and a hunch.

The question it answers is not "is the analyzer good" but the tradeoff the
design actually turns on: **how much can we cut cost and detail before it
starts missing real contact, and how loud does it get in exchange.**

## Layout

| path | committed? | what |
| --- | --- | --- |
| `bench/dataset.json` | yes | the labels and provenance — the actual artifact |
| `bench/clips/<case>/` | no (gitignored) | the footage, re-fetchable from the server |
| `bench/results/<run>.json` | no (gitignored) | stored runs, re-scorable offline |

Labels are small and reviewable in a diff; the video is not, so it stays out of
git and is pulled back with `fetch` on any machine that needs it.

## Workflow

Everything runs through `scripts/run-bench.sh`, which only adds `GEMINI_API_KEY`
from `experiments/gemini/.env`.

**1. Find footage.** Events live on the production server; the blobs are mode
0600 under the service account, so fetching goes over SSH and needs passwordless
`sudo` on that host.

```
scripts/run-bench.sh remote-list -ssh-host vps -remote-data-dir /var/lib/teslcam
```

**2. Pull one event into the dataset.** All camera angles come down, ranked the
way production ranked them (the clip it actually analyzed first). Footage that
never went through the server — a downloaded clip, a staged test in the
driveway — goes in with `add` instead:

```
scripts/run-bench.sh add -case-id garage-mustang-door \
  -origin "downloaded clip" ~/Downloads/m2-res_480p.mp4
```

```
scripts/run-bench.sh fetch -ssh-host vps -remote-data-dir /var/lib/teslcam \
  -event sentyx:2026-08-13_19-14-44
```

The case lands **unlabeled**. Production's own past verdict is stored under
`source.prior_verdict` as a reference for whoever labels it, and the scorer
never reads it — grading the model against its own earlier output would measure
nothing.

The case's `clips` list is exactly what the model is shown, so if only one
camera saw the contact, delete the other entries from the list (leave the files
on disk — they are still worth watching while labeling).

**3. Watch the clips and label.** Open the labeling UI:

```
scripts/run-bench.sh label
```

It lists the cases, plays each one's clips (tabs switch camera when a case has
several), and asks the two questions: did anything touch the car, and how bad
was it. Saving writes straight back to `bench/dataset.json`.

Or edit `bench/dataset.json` by hand — the label is a small object:

```json
"label": {
  "contact": true,
  "threat": "low",
  "start_seconds": 41,
  "end_seconds": 46
}
```

- `contact` — **the primary label.** Did anything physically touch the car:
  a person, a door, a cart, an animal. It is a fact, not a judgement, so two
  labelers give the same answer — and seeing the touch at all is what the model
  is worst at, which is the whole reason this benchmark exists.
- `threat` — severity, secondary and deliberately coarse: `none`, `low`,
  `high`. The analyzer emits the same three.
- `start_seconds` / `end_seconds` — optional, and only used to score whether
  the model pointed at the right moment. Omit them unless you care.
- `tags` (optional, on the case) — slice a report later: `night`, `garage`,
  `near-miss`.

`scripts/run-bench.sh list` shows what still needs labels.

**4. Run.** Every clip a case lists is analyzed together, as one event. A bare
run measures **what production runs today** — Gemini 3.7 Flash, medium media
resolution, 3 fps, matching the VPS config:

```
scripts/run-bench.sh run -notes "baseline: production config as of today"
```

Those defaults are a starting point, not an implicit configuration: each one is
written into the run file and printed at the top of the report, so a stored
result always states what produced it. Change one to test a variant:

```
scripts/run-bench.sh run -media-resolution low -fps 1 -notes "cheap pass"
```

`-repeat 3` analyzes each
case several times — the model is not deterministic, and a benchmark that runs
each case once reports noise as progress. `-dry-run` prints the plan and spends
nothing; `-case <id|tag>,<id>` narrows the run.

**5. Read the report.** It prints per case first, then the aggregate, then every
disagreement spelled out. Runs are saved, so `report` re-renders — and re-scores
— an old run after the scorer or the labels change, without paying the API again:

```
scripts/run-bench.sh report                        # newest run
scripts/run-bench.sh report bench/results/<id>.json
```

## Swapping the model

The runner only ever sees a `server.Analyzer` — one method, clips in, JSON
verdict out. Nothing in the runner, scorer, or report knows what produced the
verdict, so three levels of swap cost three different amounts:

**Another Gemini model** — a flag. `-model gemini-3.6-flash`, and the same for
`-media-resolution` / `-fps`. This is the comparison the benchmark exists for.

**Anything else, through a command** — `-analyzer "<cmd>"`. A Python or
TypeScript script wrapping GPT, Claude, or a local model is benchmarkable
without writing any Go:

```
scripts/run-bench.sh run -analyzer "python3 tools/analyze_openai.py"
```

The command's own configuration (model, key, detail level) is its business —
put it in the command line or the environment. The benchmark records the
command string verbatim as the run's analyzer. `-model`, `-media-resolution`
and `-fps` configure the built-in Gemini client only, and are rejected
alongside `-analyzer` rather than recorded as a config that never ran.

*Input.* The case's clip paths are appended as the final arguments, in the
order the case lists them. Nothing is written to the process's stdin.
Anything before the paths in `-analyzer` is passed through as leading
arguments, split on whitespace (so no quoted arguments containing spaces):

```
python3 tools/analyze_openai.py \
  bench/clips/<case>/2026-08-13_19-13-10-right_pillar.mp4 \
  bench/clips/<case>/2026-08-13_19-13-10-front.mp4
```

The paths are real files on disk that live for the whole run, so the script can
re-read, transcode, or sample frames from them freely.

*Output.* Exit 0 and print exactly one JSON object on stdout — no prose, no
markdown fence, no second object. Stderr is ignored on success and captured
into the error message on failure, so it is the right place for logging. A
non-zero exit, unparseable stdout, or a threat outside the enum is recorded as
that case's error; it does not abort the run and is never counted as a missed
event.

```json
{
  "description": "A man opened the adjacent SUV's door into the rear quarter panel.",
  "contact": true,
  "start_seconds": 41,
  "end_seconds": 46,
  "threat": "low",
  "usage": {"model": "gpt-5", "prompt_tokens": 18422, "output_tokens": 310, "total_tokens": 18732}
}
```

| field | required | meaning |
| --- | --- | --- |
| `description` | yes | what happened, one or two sentences |
| `contact` | for contact scoring | `true`/`false`: did anything touch the car |
| `threat` | yes | `none`, `low` or `high` |
| `start_seconds` / `end_seconds` | for timing scoring | the moment, in seconds from the start of the first clip |
| `usage` | no | token counts; without it the run reports no cost |

The older field names (`threat_level`, `what_happened`,
`event_timestamp_seconds`) are also accepted, so verdicts stored before the
schema change still replay. Cost in dollars is only estimated for Gemini models
the pricing table knows; an external analyzer reports tokens but no dollars.

**A learned head on a frozen video encoder** — `bench/analyzers/vjepa/` is
a full experiment on that pattern: V-JEPA 2 encodes every clip once into a
token cache, small heads train by leave-one-case-out on the cache, and
`vjepa_bench.py analyze` is an `-analyzer` command like any other. Its README
has the recipe.

**Another provider natively in Go** — write a type with an `Analyze` method and
hand it to the runner. The catch is not the interface, it is that the prompt,
the response schema, and the two-pass closer-look escalation currently live
inside `internal/gemini` as unexported details. Reimplementing them for a second
provider means the benchmark would compare two prompts as much as two models,
and the result would not say which one moved. If cross-provider comparison
becomes the point, extract the prompt and pass orchestration into a
provider-neutral piece first, leaving each provider with only "upload media,
generate constrained JSON".

## What gets scored

- **contact missed** — something touched the car and the model said nothing
  did. **The number that matters.** A change that regresses this does not ship,
  whatever else it improves.
- **contact phantom** — the model claims contact that never happened. The
  counterweight: fixing misses by asserting contact everywhere is not a fix.
  An analyzer that reports no `contact` field leaves both unscored rather than
  being credited with a silent "no".
- **misses** — labeled worse than `none`, called `none`. The severity-side
  equivalent, one step removed from contact.
- **false alarms** — labeled `none`, called a threat. The cheap failure, but the
  one that trains the owner to ignore notifications.
- **exact threat** — three-level agreement after folding. Useful, but a `high`
  called `low` still woke the right person; contact and misses are the real
  signal, which is why they are counted apart rather than folded into one score.
- **timing** — did the reported window land on the labeled one (±3s, since the
  label is a human reading a clip sampled at a few fps).
- **unstable** — cases whose repeats disagreed with each other.
- **cost** — tokens and estimated dollars per analysis, from the API's own usage
  reporting. Quality is only meaningful next to what it costs.

## Building a dataset that is worth running

The corpus is skewed: essentially every real field event so far is quiet. A set
of 40 `none` cases will happily report 100% and tell you nothing, because a
model that answers "none" unconditionally scores the same. What the benchmark
needs, in rough priority:

1. **Real contact that is easy to miss** — a door swinging into the panel, a
   cart, a bag, a hand on the paint. These decide the detail-level tradeoff.
2. **Near misses** — someone walking within a foot and touching nothing. This
   is where false alarms come from, and where the "closer look" escalation
   either earns its cost or wastes it.
3. **Genuine threats** — the vandalism clips.
4. **Ordinary quiet events** — passers-by, cats, rain, headlights at night.
   Cheap to collect, and the only guard against a change that fixes misses by
   crying wolf at everything.
