# teslcam-test

`teslcam-test` manually sends one real TeslaCam video through the backend and
prints the analyzer result. It creates a synthetic `event.json`, uploads both
files using the production ingest API, waits for the server's quiet period and
analysis, then parses the nested `analysis_json` into an ASCII tree.

## Local end-to-end test

Install the analyzer once and put `GEMINI_API_KEY=...` in
`experiments/gemini/.env`:

```sh
npm --prefix experiments/gemini install
```

In terminal one, start an analyzer-enabled server. The one-second quiet period
keeps manual tests fast:

```sh
DATA=/tmp/teslcam-test-data QUIET=1s ANALYZE=1 scripts/run-server-local.sh
```

In terminal two, upload a real clip:

```sh
go run ./cmd/teslcam-test \
  -server http://127.0.0.1:8090 \
  "experiments/gemini/test-video/INSANE TESLA ATTACK CAUGHT ON SENTRY MODE (clip 0m15-0m35).mp4"
```

Stop the server with Ctrl-C. Its disposable database and uploaded clip remain
under `/tmp/teslcam-test-data` until removed.

## General usage

Run against a local server:

```sh
go run ./cmd/teslcam-test -server http://127.0.0.1:8090 ./sentry-clip.mp4
```

Run against an authenticated deployment:

```sh
go run ./cmd/teslcam-test \
  -server https://api.example.com \
  -token-file /path/to/ingest.token \
  ./sentry-clip.mp4
```

Useful flags:

- `-camera 0` selects the Tesla trigger camera (`0` front, `3`/`5` left,
  `4`/`6` right, `7` back).
- `-timeout 10m` controls how long to wait for the server's quiet period and
  analyzer.
- `-event-id NAME` makes repeated/debug runs easier to identify.
- `-json` prints the final API response as indented JSON instead of the ASCII
  report.

The command exits non-zero for upload/poll errors, timeouts, and analyzer
results whose state is `failed`. Progress is written to stderr, leaving stdout
available for the report or JSON response.
