#!/usr/bin/env bash
# Polls the site's own /healthz and posts a Discord alert on sustained
# failure — an on-box synthetic monitor, not a substitute for real off-box
# uptime monitoring (see docs/production.md). It runs as its own process
# independent of spawnpoint-api, so it still fires if that process is
# wedged or down, not just slow.
set -euo pipefail

HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:8080/healthz}"
STATE_FILE="${STATE_FILE:-/var/lib/spawnpoint/healthcheck-state}"
FAIL_THRESHOLD="${HEALTHCHECK_FAIL_THRESHOLD:-3}"

if [[ -z "${DISCORD_WEBHOOK_URL:-}" ]]; then
  echo "DISCORD_WEBHOOK_URL is not set; healthcheck-alert has nothing to notify, exiting" >&2
  exit 0
fi

install -d -m 0750 "$(dirname "$STATE_FILE")"
failures=0
[[ -f "$STATE_FILE" ]] && failures="$(cat "$STATE_FILE")"

notify() {
  local message="$1"
  curl -fsS -X POST -H 'Content-Type: application/json' \
    -d "$(printf '{"content":"%s"}' "$message")" \
    "$DISCORD_WEBHOOK_URL" >/dev/null
}

if curl -fsS -m 10 "$HEALTH_URL" >/dev/null 2>&1; then
  # Reset state before notifying: a transient webhook failure here (Discord
  # is down too, say) shouldn't leave the failure counter stuck non-zero and
  # re-trigger a duplicate "recovered" notification next run.
  wasFailing="$failures"
  echo 0 > "$STATE_FILE"
  if [[ "$wasFailing" -ge "$FAIL_THRESHOLD" ]]; then
    notify "🟢 **$HEALTH_URL** is responding again after $wasFailing failed check(s)"
  fi
else
  failures=$((failures + 1))
  echo "$failures" > "$STATE_FILE"
  if [[ "$failures" -eq "$FAIL_THRESHOLD" ]]; then
    notify "🔴 **$HEALTH_URL** has failed $failures consecutive health checks"
  fi
fi
