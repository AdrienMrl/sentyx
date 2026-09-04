#!/usr/bin/env bash
# Run teslcam-server locally with sensible dev defaults. Override any of them
# via env vars. Record-only by default (no Gemini key needed); set ANALYZE=1
# to run the real analyzer on completed events.
#
#   scripts/run-server-local.sh              # ingest + store only
#   ANALYZE=1 scripts/run-server-local.sh    # + Gemini analysis
#   DATA=/tmp/t LISTEN=127.0.0.1:9000 scripts/run-server-local.sh
set -euo pipefail

DATA="${DATA:-$HOME/teslcam-data}"
LISTEN="${LISTEN:-127.0.0.1:8090}"
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$PROJECT_DIR"

args=(-data "$DATA" -listen "$LISTEN")

# Supabase user-account auth: opt-in, both vars required together.
if [[ -n "${SUPABASE_JWKS_URL:-}" || -n "${SUPABASE_ISSUER:-}" ]]; then
  if [[ -z "${SUPABASE_JWKS_URL:-}" || -z "${SUPABASE_ISSUER:-}" ]]; then
    echo "SUPABASE_JWKS_URL and SUPABASE_ISSUER must both be set to enable user-account auth" >&2
    exit 1
  fi
  args+=(-supabase-jwks-url "$SUPABASE_JWKS_URL" -supabase-issuer "$SUPABASE_ISSUER")
  echo ">> user-account auth ON (issuer $SUPABASE_ISSUER)"
fi
# FCM push notifications: opt-in, passthrough when a credentials file is given.
if [[ -n "${FCM_CREDENTIALS_FILE:-}" ]]; then
  args+=(-fcm-credentials-file "$FCM_CREDENTIALS_FILE")
  echo ">> FCM push ON (credentials $FCM_CREDENTIALS_FILE)"
fi

if [[ "${ANALYZE:-}" == "1" ]]; then
  if [[ -n "${ANALYZE_CMD:-}" ]]; then
    # Escape hatch: run an external analyzer command instead of the
    # built-in Gemini client (e.g. the TS experiment, or a fake in tests).
    args+=(-analyze "$ANALYZE_CMD")
    echo ">> analysis ON via external command: $ANALYZE_CMD"
  else
    if [[ -z "${GEMINI_API_KEY:-}" ]]; then
      # The key historically lives in the experiment's .env.
      set -a; source "$PROJECT_DIR/experiments/gemini/.env"; set +a
    fi
    args+=(-gemini-model "${GEMINI_MODEL:-gemini-3.7-flash}")
    if [[ -n "${GEMINI_MEDIA_RESOLUTION:-}" ]]; then
      args+=(-gemini-media-resolution "$GEMINI_MEDIA_RESOLUTION")
    fi
    if [[ -n "${GEMINI_FPS:-}" ]]; then
      args+=(-gemini-fps "$GEMINI_FPS")
    fi
    echo ">> analysis ON (native Gemini, model ${GEMINI_MODEL:-gemini-3.7-flash}${GEMINI_MEDIA_RESOLUTION:+, media resolution $GEMINI_MEDIA_RESOLUTION})"
  fi
else
  echo ">> record-only (set ANALYZE=1 to enable Gemini analysis)"
fi

mkdir -p "$DATA"
echo ">> data=$DATA  listen=http://$LISTEN"
echo ">> push a clip: go run ./cmd/teslcam-test -server http://$LISTEN <clip.mp4>"
echo ">> inspect:     curl http://$LISTEN/events   (ctrl-c to stop)"
exec go run ./cmd/teslcam-server "${args[@]}"
