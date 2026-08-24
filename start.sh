#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

mkdir -p "$ROOT/bin" "$ROOT/data"
if [[ ! -x "$ROOT/bin/trpc-service" ]]; then
  "$ROOT/build.sh"
fi

PID_FILE="$ROOT/data/trpc-service.pid"
LOG_FILE="$ROOT/data/trpc-service.log"
ADDR="${TRPC_SERVICE_ADDR:-127.0.0.1:8080}"
HEALTH_URL="${TRPC_SERVICE_HEALTH_URL:-http://${ADDR}/healthz}"

if [[ -f "$PID_FILE" ]]; then
  EXISTING_PID="$(cat "$PID_FILE")"
  if [[ "$EXISTING_PID" =~ ^[0-9]+$ ]] && kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo "already running: pid=$EXISTING_PID"
    exit 0
  fi
  rm -f "$PID_FILE"
fi

nohup "$ROOT/bin/trpc-service" -addr "$ADDR" >"$LOG_FILE" 2>&1 &
PID=$!
echo "$PID" >"$PID_FILE"

for _ in {1..50}; do
  if ! kill -0 "$PID" 2>/dev/null; then
    wait "$PID" || EXIT_CODE=$?
    rm -f "$PID_FILE"
    echo "start failed: process exited with code ${EXIT_CODE:-0}" >&2
    tail -n 20 "$LOG_FILE" >&2 || true
    exit 1
  fi
  if curl --fail --silent --max-time 1 "$HEALTH_URL" >/dev/null; then
    echo "started: pid=$PID addr=$ADDR"
    exit 0
  fi
  sleep 0.1
done

kill "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true
rm -f "$PID_FILE"
echo "start failed: readiness check timed out: $HEALTH_URL" >&2
tail -n 20 "$LOG_FILE" >&2 || true
exit 1
