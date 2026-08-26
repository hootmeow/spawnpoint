#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL is required" >&2
  exit 1
fi

BACKUP_DIR="${BACKUP_DIR:-/var/backups/spawnpoint}"
RETENTION_DAYS="${RETENTION_DAYS:-14}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="${BACKUP_DIR}/spawnpoint-${STAMP}.dump"

install -d -m 0750 "$BACKUP_DIR"

# Keep the password out of the process list: /proc/<pid>/cmdline is
# world-readable, so passing the full DATABASE_URL as a pg_dump argument would
# leak the credential to any local user for the duration of the dump. Split the
# password off into PGPASSWORD (exposed only via /proc/<pid>/environ, which is
# restricted to the process owner) and hand pg_dump a URL without it.
CONN="$DATABASE_URL"
if [[ "$DATABASE_URL" =~ ^([a-zA-Z]+)://([^:/@]+):([^@]+)@(.+)$ ]]; then
  scheme="${BASH_REMATCH[1]}"
  user="${BASH_REMATCH[2]}"
  export PGPASSWORD="${BASH_REMATCH[3]}"
  hostpart="${BASH_REMATCH[4]}"
  CONN="${scheme}://${user}@${hostpart}"
fi

pg_dump --format=custom --no-owner --no-acl "$CONN" > "$OUT"
gzip -f "$OUT"
find "$BACKUP_DIR" -type f -name 'spawnpoint-*.dump.gz' -mtime +"$RETENTION_DAYS" -delete

echo "${OUT}.gz"
