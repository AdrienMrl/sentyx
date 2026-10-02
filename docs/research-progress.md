# Keeping the research dashboard current

Run from the repository root:

```sh
go run ./cmd/teslcam-research
```

Open http://127.0.0.1:8770. The app reads the files below on every
`GET /api/snapshot`; the browser refreshes every 30 seconds and has a manual
refresh button. No restart is needed after a data update. The Go command is
only a web server, with `-addr` and `-dir` options: it has no `add`, `goal`, or
`codex` subcommands. Those names appear in the legacy OpenCode loop plugin,
but the separate CLI is not available in the current environment. Use the
file-based workflow below instead.

## When to update

- On starting or resuming research: read the previous journal, synchronize the
  dashboard objective with `goal.md`, set its status to `active`, and append
  the current focus and next experiment.
- Before launching an experiment: append its hypothesis, command, run ID,
  output location, and status `running`. Include delegated work.
- At meaningful checkpoints during long jobs: record actual progress and the
  next step. Do not invent measurements or append repetitive heartbeat entries.
- On completion or failure: append the outcome, measured metrics, artifact
  paths, interpretation, and next decision. Log unsuccessful experiments too.
- After dataset changes or advisor reviews: record what changed or was learned
  and how it affects the next step.
- When the overall research status changes: update `goal.json` and append an
  explanation. An individual failed experiment does not block the whole goal.
  Use `complete` only when the real-data objective is achieved; synthetic
  success is a milestone. Use `paused` for a user-requested pause.

Write records into the local checkout serving the dashboard, even when the
work runs on the PC. The lead researcher should serialize writes from workers.
Preserve history; append corrections that identify the earlier entry rather
than rewriting old findings.

## Files and fields

`bench/research/goal.json` is a JSON object with `objective` (string), `status`
(`active`, `paused`, `blocked`, or `complete`), and `updated` (UTC RFC3339 time).
Preserve additional existing fields. Update it by atomic replacement. Its
status is also read by `.opencode/plugins/goal.js` when `loop.enabled` exists;
it is not merely a visual badge. Documentation edits alone should not activate
research or modify loop-control files.

`bench/research/journal.jsonl` is append-only, one JSON object per line:

- Required: unique `id`, UTC RFC3339 `time`, `kind`, and concise `title`.
- Optional: `summary` string, `status` string, `metrics` object, `tags` array.
- Useful kinds: `focus`, `experiment`, `result`, `decision`, `dataset`, `blocker`.
- Put commands, artifact paths, dataset/split identity, threshold, and run ID in
  `summary` so they are visible in the app. Extra fields can be retained but
  are not necessarily displayed.

For the real/synthetic metric cards, a result must include **all four numeric
counts** `fp`, `fn`, `negatives`, and `positives` in `metrics`, with both class
totals greater than zero. Tag it `real` or `synthetic`; write separate entries
for the two datasets. The app shows the latest qualifying entry per dataset,
not the best historical result. `web` and `field` also qualify as real, while
synthetic entries tagged `domain-gap` are excluded from the synthetic card.
Add `fpr` and `fnr` as fractions from 0 to 1 for the trend chart, and `auc` if
measured. Do not fill missing measurements with zeros. Clearly distinguish
training diagnostics from evaluation; avoid qualifying dataset tags on training
metrics so they do not replace the evaluation cards.

`bench/research/codex/<unique-id>.json` stores advisor conversations. Each is
an object with `id`, `time`, `model`, `context`, `prompt`, `response`,
`duration_sec`, and optional `error`. Save actual requests and responses using
atomic replacement, then append the resulting decision to the journal. These
files populate Advisor notes; ordinary worker updates belong in the journal.

## Copyable file update recipe

This example appends a focus entry. Replace its title and summary with the
actual work before running; for results, change the kind and add measured
metrics and tags as described above.

```sh
python3 - <<'PY'
import datetime, fcntl, json, pathlib, uuid

root = pathlib.Path('bench/research')
root.mkdir(parents=True, exist_ok=True)
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00', 'Z')
entry = {
    'id': str(uuid.uuid4()), 'time': now,
    'kind': 'focus', 'status': 'running',
    'title': 'Describe the current research focus',
    'summary': 'Describe the evidence, next experiment, and artifact location.',
}
with (root / 'journal.jsonl').open('a') as f:
    fcntl.flock(f, fcntl.LOCK_EX)
    f.write(json.dumps(entry) + '\n')
    f.flush()
PY
```

For a goal update, use the same Python imports and `root`/`now` setup, then:

```python
import os, tempfile
path = root / 'goal.json'
goal = json.loads(path.read_text()) if path.exists() else {}
goal.update(objective='The current objective from goal.md', status='active', updated=now)
with tempfile.NamedTemporaryFile(mode='w', dir=root, suffix='.tmp', delete=False) as f:
    json.dump(goal, f, indent=2)
    f.write('\n')
    temporary = f.name
os.replace(temporary, path)
```

Use the same atomic-write pattern for advisor JSON files. Serialize goal
updates through the lead researcher to avoid overwriting concurrent changes.

After updating, verify the live reader accepts the record:

```sh
curl --fail --silent --show-error http://127.0.0.1:8770/api/snapshot | python3 -m json.tool
```

Check that the new entry appears and that result counts and tags match the
intended dataset. Invalid JSON or timestamps can make the snapshot fail; repair
formatting immediately without changing the underlying historical evidence.
