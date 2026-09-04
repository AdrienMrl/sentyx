#!/usr/bin/env bash
# Run teslcam-bench with the Gemini key loaded from the usual place. Every
# argument is passed straight through. A bare "run" uses production's current
# configuration (gemini-3.7-flash, medium media resolution, 3 fps, 2 camera
# angles) and records it with the results, so runs stay comparable.
#
#   scripts/run-bench.sh list
#   scripts/run-bench.sh remote-list -ssh-host vps -remote-data-dir /var/lib/teslcam
#   scripts/run-bench.sh fetch -ssh-host vps -remote-data-dir /var/lib/teslcam -event sentyx:2026-08-13_19-14-44
#   scripts/run-bench.sh run
#   scripts/run-bench.sh run -media-resolution low -fps 1 -notes "cheap pass"
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$PROJECT_DIR"

if [[ -z "${GEMINI_API_KEY:-}" && -f "$PROJECT_DIR/experiments/gemini/.env" ]]; then
  # The key historically lives in the experiment's .env.
  set -a; source "$PROJECT_DIR/experiments/gemini/.env"; set +a
fi

exec go run ./cmd/teslcam-bench "$@"
