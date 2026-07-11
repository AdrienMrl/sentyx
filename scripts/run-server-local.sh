#!/usr/bin/env bash
# Run teslcam-server locally with sensible dev defaults. Override any of them
# via env vars. Record-only by default (no Gemini key needed); set ANALYZE=1
# to run the real analyzer on completed events.
#
#   scripts/run-server-local.sh              # ingest + store only
#   ANALYZE=1 scripts/run-server-local.sh    # + Gemini analysis
#   DATA=/tmp/t LISTEN=127.0.0.1:9000 QUIET=5s scripts/run-server-local.sh
set -euo pipefail

DATA="${DATA:-$HOME/teslcam-data}"
LISTEN="${LISTEN:-127.0.0.1:8090}"
QUIET="${QUIET:-10s}"        # local: short so events complete fast (car: ~90s)
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$PROJECT_DIR"

args=(-data "$DATA" -listen "$LISTEN" -quiet "$QUIET")
if [[ "${ANALYZE:-}" == "1" ]]; then
  # dotenv otherwise searches the server's repo-root working directory,
  # while the analyzer's documented .env lives beside the experiment.
  export DOTENV_CONFIG_PATH="${DOTENV_CONFIG_PATH:-$PROJECT_DIR/experiments/gemini/.env}"
  args+=(-analyze "${ANALYZE_CMD:-experiments/gemini/node_modules/.bin/tsx experiments/gemini/analyze-video.ts --json}")
  echo ">> analysis ON (needs GEMINI_API_KEY in experiments/gemini/.env)"
else
  echo ">> record-only (set ANALYZE=1 to enable Gemini analysis)"
fi

mkdir -p "$DATA"
echo ">> data=$DATA  listen=http://$LISTEN  quiet=$QUIET"
echo ">> push clips:  curl -X PUT --data-binary @file http://$LISTEN/files/TeslaCam/SentryClips/<event>/<name>"
echo ">> inspect:     curl http://$LISTEN/events   (ctrl-c to stop)"
exec go run ./cmd/teslcam-server "${args[@]}"
