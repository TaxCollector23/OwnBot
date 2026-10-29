#!/usr/bin/env bash
# Record with asciinema; all daemon state is disposable. No model is installed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CLI="${OWNBOT_DEMO_CLI:-$ROOT/bin/ownbot}"
DAEMON="${OWNBOT_DEMO_DAEMON:-$ROOT/bin/ownbotd}"
DEMO_STATE="$(mktemp -d "${TMPDIR:-/tmp}/ownbot-recording.XXXXXX")"
export OWNBOT_API_URL="http://127.0.0.1:${OWNBOT_DEMO_PORT:-19065}"
"$DAEMON" --listen "${OWNBOT_API_URL#http://}" --data-dir "$DEMO_STATE" >"$DEMO_STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
cleanup() {
  kill "$DAEMON_PID" 2>/dev/null || true
  wait "$DAEMON_PID" 2>/dev/null || true
  rm -rf "$DEMO_STATE"
}
trap cleanup EXIT
for attempt in {1..50}; do
  kill -0 "$DAEMON_PID" 2>/dev/null || { cat "$DEMO_STATE/daemon.log" >&2; exit 1; }
  if curl -fsS "$OWNBOT_API_URL/api/status" >/dev/null 2>&1; then break; fi
  sleep .1
done
curl -fsS "$OWNBOT_API_URL/api/status" >/dev/null
printf '\033[2J\033[H# ownbot CLI smoke test\n# Disposable state. No model installed or run.\n\n'
sleep 2
printf '$ ownbot help\n'
"$CLI" help
sleep 6
printf '\033[2J\033[H# Inspect the disposable daemon\n\n$ export OWNBOT_API_URL=%s\n$ ownbot inspect --data-dir "$DEMO_STATE"\n' "$OWNBOT_API_URL"
"$CLI" inspect --data-dir "$DEMO_STATE"
sleep 5
printf '\n$ ownbot connect list --data-dir "$DEMO_STATE"\n'
"$CLI" connect list --data-dir "$DEMO_STATE"
sleep 3
printf '\n$ curl -s -o /dev/null -w "dashboard HTTP %%{http_code}\\n" "$OWNBOT_API_URL/dashboard"\n'
curl -s -o /dev/null -w 'dashboard HTTP %{http_code}\n' "$OWNBOT_API_URL/dashboard"
printf '\n# CLI and dashboard reachable. No model inference tested.\n'
sleep 5
