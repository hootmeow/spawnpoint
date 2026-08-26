#!/usr/bin/env bash
#
# Spawnpoint updater.
#
# Moves an existing checkout to the latest (or a pinned) git tag, applies any
# schema migrations that tag introduces, rebuilds the binaries with the
# version embedded, and restarts the systemd units if they exist.
#
# Safe to re-run: migrations are skipped once recorded in schema_migrations,
# and running with no new tag available is a no-op.
#
# Usage:
#   ./update.sh                 # update to the newest vX.Y.Z tag
#   ./update.sh --tag v1.2.0    # update (or roll back) to a specific tag
#   ./update.sh --yes           # skip the confirmation prompt

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$REPO_DIR/.env"
BIN_DIR="$REPO_DIR/bin"
MIGRATIONS_DIR="$REPO_DIR/migrations"
API_UNIT="spawnpoint-api.service"
WORKER_UNIT="spawnpoint-worker.service"

bold=$(tput bold 2>/dev/null || true)
dim=$(tput dim 2>/dev/null || true)
red=$(tput setaf 1 2>/dev/null || true)
green=$(tput setaf 2 2>/dev/null || true)
yellow=$(tput setaf 3 2>/dev/null || true)
reset=$(tput sgr0 2>/dev/null || true)

say()  { printf '%s\n' "$*"; }
head_() { printf '\n%s== %s ==%s\n' "$bold" "$*" "$reset"; }
ok()   { printf '%s✔%s %s\n' "$green" "$reset" "$*"; }
warn() { printf '%s!%s %s\n' "$yellow" "$reset" "$*"; }
die()  { printf '%s✘ %s%s\n' "$red" "$*" "$reset" >&2; exit 1; }

confirm() {
  local prompt="$1" default="${2:-y}" answer hint
  if [ "$AUTO_YES" = 1 ]; then return 0; fi
  if [ "$default" = y ]; then hint="Y/n"; else hint="y/N"; fi
  read -r -p "$prompt [$hint]: " answer
  answer="${answer:-$default}"
  case "$answer" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

env_default() {
  local key="$1" fallback="${2:-}"
  if [ -f "$ENV_FILE" ]; then
    local line
    line=$(grep -E "^${key}=" "$ENV_FILE" | tail -1 | cut -d= -f2- || true)
    line="${line%\"}"
    line="${line#\"}"
    if [ -n "$line" ]; then printf '%s' "$line"; return; fi
  fi
  printf '%s' "$fallback"
}

psql_url() { psql "$1" -X -q -v ON_ERROR_STOP=1 "${@:2}"; }

TARGET_TAG=""
AUTO_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    -t|--tag) TARGET_TAG="${2:?--tag needs a value}"; shift 2 ;;
    -y|--yes) AUTO_YES=1; shift ;;
    -h|--help)
      say "Usage: $0 [--tag vX.Y.Z] [--yes]"
      exit 0 ;;
    *) die "Unknown argument: $1 (see --help)" ;;
  esac
done

head_ "Spawnpoint update"
say "Repo: $REPO_DIR"

command -v git >/dev/null || die "git not found."
[ -d "$REPO_DIR/.git" ] || die "$REPO_DIR is not a git checkout — clone the repo with git to use update.sh."
command -v go >/dev/null || die "Go toolchain not found. Install Go 1.22+ and re-run."
command -v psql >/dev/null || die "psql not found. Install the PostgreSQL client and re-run."
[ -d "$MIGRATIONS_DIR" ] || die "migrations/ not found next to update.sh."

cd "$REPO_DIR"
if [ -n "$(git status --porcelain)" ]; then
  die "Uncommitted local changes in $REPO_DIR — commit, stash, or discard them before updating."
fi

head_ "Checking for updates"
git fetch --tags --quiet origin
CURRENT_TAG="$(git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD)"
if [ -z "$TARGET_TAG" ]; then
  TARGET_TAG="$(git tag --list 'v*' --sort=-v:refname | head -1)"
fi
[ -n "$TARGET_TAG" ] || die "No 'v*' tags found on origin — nothing to update to yet."
git rev-parse "$TARGET_TAG" >/dev/null 2>&1 || die "Tag $TARGET_TAG not found (check spelling, or run 'git fetch --tags')."

say "Current: $bold$CURRENT_TAG$reset"
say "Target:  $bold$TARGET_TAG$reset"
if [ "$CURRENT_TAG" = "$TARGET_TAG" ]; then
  ok "Already on $TARGET_TAG. Nothing to do."
  exit 0
fi

if git rev-parse "$CURRENT_TAG" >/dev/null 2>&1; then
  say ""
  say "${dim}Commits between $CURRENT_TAG and $TARGET_TAG:${reset}"
  git log --oneline "$CURRENT_TAG..$TARGET_TAG" 2>/dev/null | head -20 | sed 's/^/  /' || true
fi

confirm "Update from $CURRENT_TAG to $TARGET_TAG now?" y || die "Aborted."

head_ "Checking out $TARGET_TAG"
git checkout --quiet "$TARGET_TAG"
ok "Checked out $TARGET_TAG"

head_ "Build"
BUILD_VERSION="$(git describe --tags --always --dirty)"
LDFLAGS="-X main.version=$BUILD_VERSION"
go build -ldflags "$LDFLAGS" -o "$BIN_DIR/spawnpoint-api" ./apps/api
go build -ldflags "$LDFLAGS" -o "$BIN_DIR/spawnpoint-worker" ./apps/worker
go build -ldflags "$LDFLAGS" -o "$BIN_DIR/spawnpoint-admin" ./apps/admin
ok "Built $BUILD_VERSION"

head_ "Schema migrations"
DATABASE_URL="$(env_default DATABASE_URL "")"
if [ -z "$DATABASE_URL" ]; then
  warn "No DATABASE_URL in $ENV_FILE — skipping migrations. Apply them manually before restarting services."
else
  psql_url "$DATABASE_URL" -Atc "select 1" >/dev/null || die "Cannot connect to $DATABASE_URL"
  psql_url "$DATABASE_URL" <<'SQL'
create table if not exists schema_migrations (
  version text primary key,
  applied_at timestamptz not null default now()
);
SQL
  NEW=0
  for file in "$MIGRATIONS_DIR"/*.sql; do
    mversion="$(basename "$file" .sql)"
    applied=$(psql_url "$DATABASE_URL" -Atc "select 1 from schema_migrations where version = '$mversion'")
    if [ "$applied" = 1 ]; then
      say "$dim  skip $mversion (recorded)$reset"
      continue
    fi
    say "  apply $mversion"
    if ! psql_url "$DATABASE_URL" -f "$file" >/dev/null; then
      warn "Migration $mversion failed. If the error mentions pg_trgm, run this as a"
      warn "superuser on that database first:  create extension pg_trgm;"
      die "Migration $mversion failed. Binaries were rebuilt but NOT restarted — old binaries are still running."
    fi
    psql_url "$DATABASE_URL" -Atc "insert into schema_migrations (version) values ('$mversion') on conflict do nothing" >/dev/null
    NEW=$((NEW + 1))
  done
  if [ "$NEW" -gt 0 ]; then ok "Applied $NEW new migration(s)."; else ok "Schema already up to date."; fi
fi

head_ "Restart services"
# Restart only the units that are actually installed — an API-only install
# (no watch directory) has no worker unit, and restarting a missing unit
# would abort the script under `set -e`.
RESTARTED=0
if command -v systemctl >/dev/null && [ -d /run/systemd/system ]; then
  for unit in "$API_UNIT" "$WORKER_UNIT"; do
    if [ ! -f "/etc/systemd/system/$unit" ]; then
      continue
    fi
    if systemctl restart "$unit"; then
      RESTARTED=$((RESTARTED + 1))
      sleep 1
      if systemctl is-active --quiet "$unit"; then ok "Restarted $unit"; else warn "$unit is not active — check: journalctl -u $unit -n 50"; fi
    else
      warn "Failed to restart $unit — check: journalctl -u $unit -n 50"
    fi
  done
fi
if [ "$RESTARTED" = 0 ]; then
  warn "No systemd units found — restart spawnpoint-api and spawnpoint-worker manually."
fi

head_ "Done"
ok "Now running $TARGET_TAG ($BUILD_VERSION)."
say "Roll back at any time with: ${bold}./update.sh --tag $CURRENT_TAG${reset}"
say "(schema migrations only add tables/views/indexes here, so rolling back the"
say "code while staying on a newer schema is safe)"
