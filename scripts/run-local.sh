#!/usr/bin/env bash
# Run the API on this machine, against the Postgres and Redis already in Docker.
#
# Native rather than containerised on purpose. Outside the regular session the engine
# is the stop -- Webull accepts no stop order there -- so the position is protected only
# while this process is up and receiving prices. Every layer between the code and the
# machine is another thing that can be slow to come back, and a restart that takes two
# minutes is two minutes unprotected.
#
# Usage:  scripts/run-local.sh [serve|stop|logs|status]
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

log="$root/.run/api.log"
pidfile="$root/.run/api.pid"
mkdir -p "$root/.run"

load_env() {
  if [[ ! -f .env ]]; then
    echo "no .env — the API will not start without one" >&2
    exit 1
  fi
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
  # Compose used to supply these; running natively, the ports are the published ones.
  export DATABASE_URL="postgres://${POSTGRES_USER:-mip}:${POSTGRES_PASSWORD:-change-me}@127.0.0.1:${POSTGRES_PORT:-55432}/${POSTGRES_DB:-mip}?sslmode=disable"
  export REDIS_URL="${REDIS_URL:-redis://127.0.0.1:6379/0}"
  export WEBULL_ACCESS_TOKEN_FILE="${WEBULL_ACCESS_TOKEN_FILE:-$root/.secrets/webull-token.txt}"
}

case "${1:-serve}" in
serve)
  load_env
  go build -o "$root/.run/mip" ./cmd/mip
  # nohup, so closing the terminal does not take the stop with it.
  nohup "$root/.run/mip" serve \
    --addr=":${API_PORT:-8080}" \
    --allowed-origin="${ALLOWED_ORIGIN:-http://localhost:3001}" \
    >>"$log" 2>&1 &
  echo $! >"$pidfile"
  sleep 3
  if ! kill -0 "$(cat "$pidfile")" 2>/dev/null; then
    echo "the API exited on start-up; last lines:" >&2
    tail -20 "$log" >&2
    exit 1
  fi
  echo "api running, pid $(cat "$pidfile"), logs at $log"
  ;;
stop)
  if [[ -f "$pidfile" ]]; then
    kill "$(cat "$pidfile")" 2>/dev/null || true
    rm -f "$pidfile"
  fi
  pkill -f "$root/.run/mip serve" 2>/dev/null || true
  echo "stopped"
  ;;
logs)
  tail -f "$log"
  ;;
status)
  if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
    echo "api up, pid $(cat "$pidfile")"
  else
    echo "api down — anything relying on the engine to hold a stop is unprotected"
    exit 1
  fi
  ;;
*)
  echo "usage: $0 [serve|stop|logs|status]" >&2
  exit 2
  ;;
esac
