#!/usr/bin/env bash
#
# Spawnpoint interactive installer.
#
# Covers the common self-hosting shapes:
#   * toolchain:      installs Go (official upstream tarball) and PostgreSQL
#                     itself if either is missing — nothing to pre-provision
#   * standalone box: provision a local PostgreSQL role + database
#   * shared infra:   connect to an existing PostgreSQL you already run
#   * log sources:    watch a live BF1942/BFV server's log directory,
#                     watch a drop directory (rsync/FTP/scp), or API-only
#   * backfill:       one-shot import of historical .xml/.zxml logs
#   * systemd:        install/refresh spawnpoint-api + -worker units,
#                     then verify they actually came up (not just "started")
#
# Safe to re-run: existing .env values become the defaults, migrations are
# tracked in schema_migrations, and systemd units are refreshed in place.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$REPO_DIR/.env"
BIN_DIR="$REPO_DIR/bin"
MIGRATIONS_DIR="$REPO_DIR/migrations"
UNIT_DIR="/etc/systemd/system"
API_UNIT="spawnpoint-api.service"
WORKER_UNIT="spawnpoint-worker.service"

# Colors auto-disable when stdout is not a terminal, NO_COLOR is set
# (https://no-color.org), or the terminal cannot render them.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  bold=$(tput bold 2>/dev/null || true)
  dim=$(tput dim 2>/dev/null || true)
  red=$(tput setaf 1 2>/dev/null || true)
  green=$(tput setaf 2 2>/dev/null || true)
  yellow=$(tput setaf 3 2>/dev/null || true)
  cyan=$(tput setaf 6 2>/dev/null || true)
  reset=$(tput sgr0 2>/dev/null || true)
  if [ "$(tput colors 2>/dev/null || echo 8)" -ge 256 ]; then
    # BF1942 palette: olive drab, khaki, sand, gunmetal — matches the
    # bf1942-linux installers so all our setup scripts feel like one family.
    c_army=$(tput setaf 58 2>/dev/null || true)
    c_olive=$(tput setaf 100 2>/dev/null || true)
    c_khaki=$(tput setaf 143 2>/dev/null || true)
    c_sand=$(tput setaf 180 2>/dev/null || true)
    c_steel=$(tput setaf 246 2>/dev/null || true)
  else
    c_army=$yellow; c_olive=$green; c_khaki=$yellow; c_sand=""; c_steel=""
  fi
else
  bold=""; dim=""; red=""; green=""; yellow=""; cyan=""; reset=""
  c_army=""; c_olive=""; c_khaki=""; c_sand=""; c_steel=""
fi

HR_LINE='──────────────────────────────────────────────────────────'
hr()  { printf '%s%s%s\n' "$c_army" "$HR_LINE" "$reset"; }
cls() { [ -t 1 ] && clear 2>/dev/null; return 0; }

say()  { printf '%s\n' "$*"; }
head_() { printf '\n%s== %s ==%s\n' "$bold" "$*" "$reset"; }
ok()   { printf '%s✔%s %s\n' "$green" "$reset" "$*"; }
RUN_WARNINGS=()
warn() { printf '%s!%s %s\n' "$yellow" "$reset" "$*"; RUN_WARNINGS+=("$*"); }
die()  { printf '%s✘ %s%s\n' "$red" "$*" "$reset" >&2; exit 1; }

# box "Title" "line" ... — bordered callout for anything the user needs to
# stop and copy down (credentials, final summary). Same visual family as the
# credentials box in the bf1942-linux installers.
box() {
  local title="$1"; shift
  printf '\n%s╔══════════════════════════════════════════════════════════╗%s\n' "$yellow" "$reset"
  printf '%s║%s  %s%-58s%s%s║%s\n' "$yellow" "$reset" "$bold" "$title" "$reset" "$yellow" "$reset"
  printf '%s╚══════════════════════════════════════════════════════════╝%s\n' "$yellow" "$reset"
  for line in "$@"; do
    say "  $line"
  done
  echo ""
}

# step_ "Name" — like head_, but numbers each major phase so a long install
# reads as a checklist with visible progress instead of an undifferentiated
# scroll of output.
TOTAL_STEPS=11
STEP=0
step_() {
  STEP=$((STEP + 1))
  printf '\n%s%s▶ Step %d/%d — %s%s\n' "$c_khaki" "$bold" "$STEP" "$TOTAL_STEPS" "$*" "$reset"
  hr
}

banner() {
  cls
  printf '\n'
  printf '%s   ██████╗ ███████╗ ██╗ █████╗  ██╗  ██╗██████╗%s\n' "$c_sand" "$reset"
  printf '%s   ██╔══██╗██╔════╝███║██╔══██╗ ██║  ██║╚════██╗%s\n' "$c_sand" "$reset"
  printf '%s   ██████╔╝█████╗  ╚██║╚██████║ ███████║ █████╔╝%s\n' "$c_khaki" "$reset"
  printf '%s   ██╔══██╗██╔══╝   ██║ ╚═══██║ ╚════██║██╔═══╝%s\n' "$c_khaki" "$reset"
  printf '%s   ██████╔╝██║      ██║ █████╔╝      ██║███████╗%s\n' "$c_olive" "$reset"
  printf '%s   ╚═════╝ ╚═╝      ╚═╝ ╚════╝       ╚═╝╚══════╝%s\n' "$c_olive" "$reset"
  printf '\n'
  hr
  printf '   %s★%s %sSPAWNPOINT · XML EVENT-LOG STATS PLATFORM%s %s★%s\n' "$yellow" "$reset" "$bold" "$reset" "$yellow" "$reset"
  printf '   %sgo + postgres · api + worker · systemd-managed%s\n' "$dim" "$reset"
  hr
  printf '\n'
}

# ask VAR "Prompt" "default"
ask() {
  local var="$1" prompt="$2" default="${3:-}" answer
  if [ -n "$default" ]; then
    read -r -p "$prompt [$default]: " answer
    printf -v "$var" '%s' "${answer:-$default}"
  else
    read -r -p "$prompt: " answer
    printf -v "$var" '%s' "$answer"
  fi
}

# confirm "Prompt" default(y|n)
confirm() {
  local prompt="$1" default="${2:-y}" answer hint
  if [ "$default" = y ]; then hint="Y/n"; else hint="y/N"; fi
  read -r -p "$prompt [$hint]: " answer
  answer="${answer:-$default}"
  case "$answer" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

# ask_secret VAR "Prompt" — like ask, but the answer isn't echoed to the
# terminal and never appears in a "[default]" hint. Use for anything that
# shouldn't land in scrollback, a terminal-capture tool (script(1),
# asciinema, tmux logging), or a screen someone else can see.
ask_secret() {
  local var="$1" prompt="$2" answer
  read -r -s -p "$prompt: " answer
  printf '\n'
  printf -v "$var" '%s' "$answer"
}

# choose VAR "Prompt" default option...
choose() {
  local var="$1" prompt="$2" default="$3" answer i
  shift 3
  say ""
  say "$bold$prompt$reset"
  i=1
  for option in "$@"; do
    say "  [$i] $option"
    i=$((i + 1))
  done
  while true; do
    read -r -p "Choice [$default]: " answer
    answer="${answer:-$default}"
    if [[ "$answer" =~ ^[0-9]+$ ]] && [ "$answer" -ge 1 ] && [ "$answer" -le $# ]; then
      printf -v "$var" '%s' "$answer"
      return
    fi
    warn "Enter a number between 1 and $#."
  done
}

rand_hex() { od -vN "${1:-16}" -An -tx1 /dev/urandom | tr -d ' \n'; }

# path_world_traversable PATH — true if every ancestor directory (up to /)
# has the "other" execute bit set, i.e. an unprivileged system user could
# `cd` all the way down to PATH. A checkout under /root (mode 700 by
# default) fails this even though PATH itself may look fine, which is
# exactly the case that silently forces services to run as root later.
path_world_traversable() {
  local p="$1" perm other
  while [ "$p" != "/" ] && [ -n "$p" ]; do
    perm="$(stat -c '%a' "$p" 2>/dev/null)" || return 1
    other="${perm: -1}"
    case "$other" in 1|3|5|7) ;; *) return 1 ;; esac
    p="$(dirname "$p")"
  done
  return 0
}

# Read a value out of an existing .env so re-runs default to the current setup.
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

# ---------------------------------------------------------------------------
banner
say "Repo: $REPO_DIR"
say "This installer gets Go and PostgreSQL in place if they aren't already,"
say "provisions or connects a database, applies schema migrations, builds the"
say "binaries, points the ingestion worker at your BF1942 logs, installs"
say "systemd services, and verifies they actually came up at the end."
say "$TOTAL_STEPS steps — you'll be asked before anything destructive."

# --- preflight --------------------------------------------------------------
step_ "Preflight"
[ -d "$MIGRATIONS_DIR" ] || die "migrations/ not found next to setup.sh — run this from a checkout of the repo."

IS_ROOT=0
[ "$(id -u)" = 0 ] && IS_ROOT=1
HAS_SYSTEMD=0
if command -v systemctl >/dev/null && [ -d /run/systemd/system ]; then HAS_SYSTEMD=1; fi
if [ "$IS_ROOT" = 0 ]; then
  warn "Not running as root: provisioning local PostgreSQL, installing Go, and installing systemd units will be unavailable."
fi

PKG_MGR=""
if command -v apt-get >/dev/null; then PKG_MGR=apt
elif command -v dnf >/dev/null; then PKG_MGR=dnf
elif command -v yum >/dev/null; then PKG_MGR=yum
fi
pkg_install() {
  [ "$IS_ROOT" = 1 ] && [ -n "$PKG_MGR" ] || return 1
  case "$PKG_MGR" in
    apt) apt-get update -qq && apt-get install -y -qq "$@" ;;
    dnf) dnf install -y "$@" ;;
    yum) yum install -y "$@" ;;
  esac
}

command -v curl >/dev/null || { warn "curl not found — installing it (needed to fetch Go)."; pkg_install curl || die "Install curl manually and re-run."; }

# install_go VERSION_HINT — official upstream tarball, not the distro
# package: distro Go packages lag upstream by a version or two and this
# project tracks current Go releases (see go.mod).
install_go() {
  [ "$IS_ROOT" = 1 ] || die "Go 1.22+ is required and none was found. Install it yourself (https://go.dev/dl/) and re-run, or re-run this installer as root."
  local arch ver url tmp
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "No automatic Go install for CPU architecture $(uname -m) — install Go 1.22+ manually from https://go.dev/dl/ and re-run." ;;
  esac
  ver="$(curl -fsSL https://go.dev/VERSION?m=text 2>/dev/null | head -1)"
  [[ "$ver" =~ ^go[0-9] ]] || ver="go1.23.4"  # pinned fallback if the version lookup fails (network hiccup)
  url="https://go.dev/dl/${ver}.linux-${arch}.tar.gz"
  say "Installing $ver for linux/$arch to /usr/local/go ..."
  tmp="$(mktemp)"
  curl -fsSL "$url" -o "$tmp" || die "Download failed: $url"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmp"
  rm -f "$tmp"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  hash -r
}

NEED_GO=0
if ! command -v go >/dev/null; then
  NEED_GO=1
else
  GO_VER="$(go version | awk '{print $3}')"
  GO_MINOR="$(printf '%s' "$GO_VER" | sed -nE 's/^go1\.([0-9]+).*/\1/p')"
  [ -n "$GO_MINOR" ] && [ "$GO_MINOR" -lt 22 ] && NEED_GO=1
fi
if [ "$NEED_GO" = 1 ]; then
  warn "Go 1.22+ not found${GO_VER:+ (found $GO_VER)}."
  confirm "Install the current Go release now (official upstream tarball)?" y || die "Go 1.22+ is required."
  install_go
fi
GO_VER="$(go version | awk '{print $3}')"
ok "$GO_VER"

if ! command -v psql >/dev/null; then
  warn "psql (PostgreSQL client) not found — it's required even if you're connecting to someone else's database."
  confirm "Install the PostgreSQL client now?" y || die "psql is required."
  pkg_install postgresql-client || pkg_install postgresql || die "Could not install a PostgreSQL client. Install one manually and re-run."
  command -v psql >/dev/null || die "psql still not found after install — check the package manager output above."
fi
ok "psql $(psql --version | awk '{print $3}')"

if [ "$IS_ROOT" = 1 ] && [ "$HAS_SYSTEMD" = 1 ] && ! path_world_traversable "$REPO_DIR"; then
  warn "$REPO_DIR isn't reachable by an unprivileged user (a parent directory,"
  warn "likely /root, isn't traversable). Services would later be forced to run"
  warn "as root instead of a dedicated unprivileged user."
  NEW_DIR=/opt/spawnpoint
  if [ "$REPO_DIR" = "$NEW_DIR" ]; then
    # Already at the target path — a previous run's "mkdir -p" made this
    # directory under a restrictive umask, so it's the perms that are wrong,
    # not the location. Fix them in place; a tar copy here would read and
    # write the same tree at once and corrupt itself.
    if confirm "Fix permissions on $REPO_DIR (chmod o+rx) and continue?" y; then
      # Every directory under here (bin/, migrations/, ...) was created under
      # the same restrictive umask as REPO_DIR itself, so fixing just the top
      # level would still leave e.g. bin/ unreachable to the service user.
      find "$REPO_DIR" -type d -exec chmod o+rx {} +
      path_world_traversable "$REPO_DIR" || die "$REPO_DIR is still unreachable by an unprivileged user — check the permissions on its parent directories (e.g. /opt) and re-run."
      ok "Fixed permissions on $REPO_DIR (recursively)."
    else
      warn "Continuing at $REPO_DIR — services will run as root (see docs/production.md)."
    fi
  elif confirm "Move this checkout to $NEW_DIR now and continue from there?" y; then
    if [ -e "$NEW_DIR" ]; then
      die "$NEW_DIR already exists — move or remove it, then re-run setup.sh from $REPO_DIR."
    fi
    mkdir -p -m 0755 "$NEW_DIR"
    tar -C "$REPO_DIR" -cf - --exclude=bin --exclude=data . | tar -C "$NEW_DIR" -xf -
    # tar preserves the source directories' own modes, which may be just as
    # restrictive as REPO_DIR's if they inherited the same umask — fix them
    # here too instead of waiting to discover it on the next run.
    find "$NEW_DIR" -type d -exec chmod o+rx {} +
    ok "Copied to $NEW_DIR — continuing there."
    exec "$NEW_DIR/setup.sh" "$@"
  else
    warn "Continuing at $REPO_DIR — services will run as root (see docs/production.md)."
  fi
fi

# --- database ---------------------------------------------------------------
step_ "Database"
DATABASE_URL="$(env_default DATABASE_URL "")"

DB_MODE_DEFAULT=1
[ -n "$DATABASE_URL" ] && DB_MODE_DEFAULT=3
DB_OPTIONS=(
  "Standalone — create a local PostgreSQL role + database on this machine"
  "Existing PostgreSQL — I have a server, give me a connection prompt"
)
[ -n "$DATABASE_URL" ] && DB_OPTIONS+=("Keep current database ($(sed -E 's#//[^:]+:[^@]*@#//***:***@#' <<<"$DATABASE_URL"))")
choose DB_MODE "How should the database be set up?" "$DB_MODE_DEFAULT" "${DB_OPTIONS[@]}"

if [ "$DB_MODE" = 1 ]; then
  [ "$IS_ROOT" = 1 ] || die "Standalone provisioning needs root (it talks to the local postgres superuser). Re-run with sudo, or pick 'Existing PostgreSQL'."
  if ! command -v pg_ctl >/dev/null && ! id postgres >/dev/null 2>&1; then
    warn "No local PostgreSQL server detected."
    if confirm "Install PostgreSQL now with the system package manager?"; then
      if command -v apt-get >/dev/null; then apt-get update -qq && apt-get install -y -qq postgresql
      elif command -v dnf >/dev/null; then dnf install -y postgresql-server && postgresql-setup --initdb && systemctl enable --now postgresql
      elif command -v yum >/dev/null; then yum install -y postgresql-server && postgresql-setup --initdb && systemctl enable --now postgresql
      else die "No supported package manager found. Install PostgreSQL manually and re-run."
      fi
    else
      die "PostgreSQL is required for a standalone install."
    fi
  fi
  id postgres >/dev/null 2>&1 || die "postgres system user not found after install — check your PostgreSQL installation."

  ask DB_NAME "Database name" "spawnpoint"
  ask DB_USER "Database user" "spawnpoint"
  DB_PASS="$(rand_hex 16)"
  if confirm "Generate a random database password (recommended)?" y; then
    say "Generated (not shown — it's only used locally and saved straight to .env, mode 600)."
  else
    while true; do
      ask_secret DB_PASS "Database password (input hidden)"
      [ -n "$DB_PASS" ] && break
      warn "A password is required."
    done
  fi

  su - postgres -c "psql -X -q -v ON_ERROR_STOP=1" <<SQL
do \$\$
begin
  if not exists (select 1 from pg_roles where rolname = '$DB_USER') then
    create role "$DB_USER" login password '$DB_PASS';
  else
    alter role "$DB_USER" login password '$DB_PASS';
  end if;
end
\$\$;
SQL
  if ! su - postgres -c "psql -X -q -Atc \"select 1 from pg_database where datname='$DB_NAME'\"" | grep -q 1; then
    su - postgres -c "createdb -O \"$DB_USER\" \"$DB_NAME\""
  fi
  DATABASE_URL="postgres://$DB_USER:$DB_PASS@localhost:5432/$DB_NAME?sslmode=disable"
  ok "Provisioned database $DB_NAME owned by $DB_USER"

  # The map/stats pages run several map-filtered analytical queries per
  # request; Postgres's defaults assume spinning disks and enough RAM to be
  # generous with JIT, which is wrong for most small VPS boxes (cloud disks
  # are effectively SSD even when reported as rotational, and JIT's compile
  # overhead exceeds its runtime savings for small interactive queries on
  # 1-2 CPUs). pg_reload_conf() applies this without needing to know the
  # systemd unit name for this distro's PostgreSQL packaging.
  TOTAL_MEM_MB=$(( $(grep MemTotal /proc/meminfo | awk '{print $2}') / 1024 ))
  if [ "$TOTAL_MEM_MB" -gt 0 ] && [ "$TOTAL_MEM_MB" -lt 4096 ]; then
    head_ "PostgreSQL tuning"
    say "Detected ${TOTAL_MEM_MB}MB RAM — applying settings tuned for small boxes"
    say "(random_page_cost, effective_cache_size, jit off, work_mem)."
    EFFECTIVE_CACHE_MB=$(( TOTAL_MEM_MB * 3 / 4 ))
    su - postgres -c "psql -X -q -v ON_ERROR_STOP=1" <<SQL
alter system set random_page_cost = 1.1;
alter system set effective_cache_size = '${EFFECTIVE_CACHE_MB}MB';
alter system set jit = off;
alter system set work_mem = '32MB';
alter system set max_connections = 40;
select pg_reload_conf();
SQL
    # max_connections needs a full restart, not just a reload; safe here
    # since this runs during initial provisioning, before any app service is
    # started against this database. "postgresql" is the standard meta-unit
    # on Debian/Ubuntu; harmless no-op elsewhere.
    systemctl restart postgresql 2>/dev/null || true
    ok "Applied PostgreSQL tuning for constrained hardware."
  fi
elif [ "$DB_MODE" = 2 ]; then
  say ""
  say "Enter a PostgreSQL connection URL, e.g."
  say "  postgres://user:password@db.example.com:5432/spawnpoint?sslmode=require"
  while true; do
    ask DATABASE_URL "Connection URL" "$DATABASE_URL"
    [ -n "$DATABASE_URL" ] || { warn "A connection URL is required."; continue; }
    if psql_url "$DATABASE_URL" -Atc "select 1" >/dev/null 2>&1; then
      ok "Connected."
      break
    fi
    warn "Could not connect. Check host/credentials (error above) and try again, or Ctrl-C to abort."
    psql_url "$DATABASE_URL" -Atc "select 1" || true
  done
  say ""
  say "${dim}Note: if this server has under ~4GB RAM, consider asking whoever"
  say "manages it to set random_page_cost=1.1, a realistic effective_cache_size,"
  say "jit=off, work_mem=32MB, and max_connections=40 — see docs/performance.md.${reset}"
fi
psql_url "$DATABASE_URL" -Atc "select 1" >/dev/null || die "Cannot connect to $DATABASE_URL"

# --- migrations -------------------------------------------------------------
step_ "Schema migrations"
psql_url "$DATABASE_URL" <<'SQL'
create table if not exists schema_migrations (
  version text primary key,
  applied_at timestamptz not null default now()
);
SQL

# Databases created before this installer existed have no schema_migrations
# rows; probe a marker object per migration so we never replay 0001 onto them.
probe_for() {
  case "$1" in
    0001*) echo "select to_regclass('organizations') is not null" ;;
    0002*) echo "select to_regclass('player_totals_mv') is not null" ;;
    0003*) echo "select to_regclass('ingest_jobs') is not null" ;;
    0004*) echo "select to_regclass('round_awards') is not null" ;;
    0005*) echo "select exists (select 1 from information_schema.columns where table_name = 'players' and column_name = 'hidden')" ;;
    0006*) echo "select to_regclass('idx_events_heatmap') is not null" ;;
    0007*) echo "select to_regclass('map_summary_mv') is not null" ;;
    0008*) echo "select to_regclass('vehicle_activity_totals_mv') is not null" ;;
    0009*) echo "select to_regclass('kit_totals_mv') is not null" ;;
    0010*) echo "select to_regclass('award_totals_mv') is not null" ;;
    0011*) echo "select to_regclass('player_breakdown_mv') is not null" ;;
    0012*) echo "select exists (select 1 from information_schema.columns where table_name = 'players' and column_name = 'clan_tag')" ;;
    0013*) echo "select exists (select 1 from information_schema.columns where table_name = 'servers' and column_name = 'api_key_sha256')" ;;
    0014*) echo "select to_regclass('admin_users') is not null" ;;
    *)     echo "select false" ;;
  esac
}

for file in "$MIGRATIONS_DIR"/*.sql; do
  version="$(basename "$file" .sql)"
  applied=$(psql_url "$DATABASE_URL" -Atc "select 1 from schema_migrations where version = '$version'")
  if [ "$applied" = 1 ]; then
    say "$dim  skip $version (recorded)$reset"
    continue
  fi
  if [ "$(psql_url "$DATABASE_URL" -Atc "$(probe_for "$version")")" = t ]; then
    psql_url "$DATABASE_URL" -Atc "insert into schema_migrations (version) values ('$version') on conflict do nothing" >/dev/null
    say "$dim  skip $version (already present, recorded)$reset"
    continue
  fi
  say "  apply $version"
  if ! psql_url "$DATABASE_URL" -f "$file" >/dev/null; then
    warn "Migration $version failed. If the error mentions pg_trgm, run this as a"
    warn "superuser on that database first:  create extension pg_trgm;"
    die "Migration $version failed."
  fi
  psql_url "$DATABASE_URL" -Atc "insert into schema_migrations (version) values ('$version') on conflict do nothing" >/dev/null
done
ok "Schema is up to date."

# --- log source -------------------------------------------------------------
step_ "BF1942 log source"
say "The worker ingests the engine's per-round event logs (ev_*.xml / .zxml)."

WATCH_DIRS=()
choose LOG_MODE "Where do your logs come from?" 1 \
  "A BF1942/BFV server on this machine — find its log directory" \
  "A directory logs are copied into (rsync/FTP/scp drop, shared mount)" \
  "No logs yet — set up the web UI/API only"

if [ "$LOG_MODE" = 1 ]; then
  say ""
  say "Scanning for BF1942 log directories (*/mods/*/logs) ..."
  mapfile -t FOUND < <(find /home /opt /srv /usr/local /root -maxdepth 7 -type d -path '*/mods/*/logs' 2>/dev/null | head -10 || true)
  if [ "${#FOUND[@]}" -gt 0 ]; then
    say "Found:"
    i=1
    for d in "${FOUND[@]}"; do say "  [$i] $d"; i=$((i + 1)); done
    say "  [$i] Somewhere else (enter a path)"
    read -r -p "Choice [1]: " pick
    pick="${pick:-1}"
    if [[ "$pick" =~ ^[0-9]+$ ]] && [ "$pick" -ge 1 ] && [ "$pick" -le "${#FOUND[@]}" ]; then
      WATCH_DIRS+=("${FOUND[$((pick - 1))]}")
    else
      ask CUSTOM_DIR "Log directory path"
      [ -d "$CUSTOM_DIR" ] || die "Directory not found: $CUSTOM_DIR"
      WATCH_DIRS+=("$CUSTOM_DIR")
    fi
  else
    warn "Nothing found automatically."
    ask CUSTOM_DIR "Log directory path"
    [ -d "$CUSTOM_DIR" ] || die "Directory not found: $CUSTOM_DIR"
    WATCH_DIRS+=("$CUSTOM_DIR")
  fi
elif [ "$LOG_MODE" = 2 ]; then
  ask CUSTOM_DIR "Drop directory path"
  mkdir -p "$CUSTOM_DIR"
  WATCH_DIRS+=("$CUSTOM_DIR")
fi

if [ "${#WATCH_DIRS[@]}" -gt 0 ]; then
  while confirm "Watch another directory as well?" n; do
    ask EXTRA_DIR "Additional directory path"
    [ -d "$EXTRA_DIR" ] || { warn "Directory not found: $EXTRA_DIR (skipped)"; continue; }
    WATCH_DIRS+=("$EXTRA_DIR")
  done
  ask WATCH_INTERVAL "Poll interval (how often to look for finished logs)" "15s"
  ok "Watching: ${WATCH_DIRS[*]}"
else
  say "No watch directory configured; you can re-run setup.sh later or run the worker by hand."
fi

# --- site settings ----------------------------------------------------------
step_ "Site settings"
while true; do
  ask HTTP_PORT "Web UI / API port" "$(env_default PORT 8080)"
  if ! [[ "$HTTP_PORT" =~ ^[0-9]+$ ]] || [ "$HTTP_PORT" -lt 1 ] || [ "$HTTP_PORT" -gt 65535 ]; then
    warn "Enter a port number between 1 and 65535."
    continue
  fi
  if command -v ss >/dev/null && ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ":$HTTP_PORT\$"; then
    warn "Port $HTTP_PORT already has something listening on it."
    confirm "Use it anyway?" n && break || continue
  fi
  break
done
ask SERVER_NAME "Game server display name" "$(env_default SERVER_NAME "BF1942 Server")"
ask SERVER_ADDRESS "Game server address (metadata only)" "$(env_default SERVER_ADDRESS 127.0.0.1)"
ask SERVER_PORT "Game server port (metadata only)" "$(env_default SERVER_PORT 14567)"
ARCHIVE_DIR_DEFAULT="$(env_default ARCHIVE_DIR "$REPO_DIR/data/raw-logs")"
say ""
say "The app keeps its own backup copy of every log it ingests (for re-processing"
say "later, e.g. after a parser update). This directory is managed automatically —"
say "the app writes to it; you never place files there yourself."
ask ARCHIVE_DIR "Raw log backup directory (managed automatically)" "$ARCHIVE_DIR_DEFAULT"
mkdir -p "$ARCHIVE_DIR"

say ""
say "Admin pages (/status, /admin/*) need at least one admin account or an"
say "ADMIN_TOKEN. This installer creates you a named account — its username"
say "and password sign in at /admin/login, same as any other web app."
CREATE_ADMIN_ACCOUNT=0
if confirm "Create an admin account now?" y; then
  CREATE_ADMIN_ACCOUNT=1
  ask ADMIN_USERNAME "Admin username" "admin"
  while true; do
    ask_secret ADMIN_PASSWORD "Admin password (10+ chars, leave blank to generate one)"
    [ -z "$ADMIN_PASSWORD" ] && break
    [ "${#ADMIN_PASSWORD}" -ge 10 ] && [ "${#ADMIN_PASSWORD}" -le 72 ] && break
    warn "Password must be 10-72 characters."
  done
  if [ -z "$ADMIN_PASSWORD" ]; then
    ADMIN_PASSWORD="$(rand_hex 12)"
    GENERATED_ADMIN_PASSWORD=1
  fi
fi

ADMIN_TOKEN="$(env_default ADMIN_TOKEN "")"
if [ -n "$ADMIN_TOKEN" ]; then
  confirm "Keep the existing break-glass ADMIN_TOKEN too?" y || ADMIN_TOKEN=""
elif confirm "Also generate a break-glass ADMIN_TOKEN, for curl/API automation or as a lockout recovery path? (optional)" n; then
  ADMIN_TOKEN="$(rand_hex 24)"
  say "Admin token: $bold$ADMIN_TOKEN$reset  (also saved to .env)"
fi
if [ "$CREATE_ADMIN_ACCOUNT" = 0 ] && [ -z "$ADMIN_TOKEN" ]; then
  warn "No admin account and no ADMIN_TOKEN — the admin area will be disabled. Re-run setup.sh to add one later."
fi

# --- write .env -------------------------------------------------------------
step_ "Configuration"
umask 077
# Keys this installer manages itself. Every OTHER key already present in .env
# (HOST, SECURE_COOKIES, PUBLIC_BASE_URL, DISCORD_WEBHOOK_URL, tuning knobs,
# anything from the README configuration table) is preserved verbatim, so
# re-running setup.sh never undoes going-live hardening added by hand.
MANAGED_KEYS="PORT DATABASE_URL ARCHIVE_DIR SERVER_NAME SERVER_ADDRESS SERVER_PORT ADMIN_TOKEN SESSION_KEY_FILE"
PRESERVED_LINES=""
if [ -f "$ENV_FILE" ]; then
  while IFS= read -r line; do
    case "$line" in
      [A-Za-z_]*=*)
        key="${line%%=*}"
        managed=0
        for m in $MANAGED_KEYS; do
          [ "$key" = "$m" ] && managed=1 && break
        done
        [ "$managed" = 0 ] && PRESERVED_LINES="${PRESERVED_LINES}${line}"$'\n'
        ;;
    esac
  done <"$ENV_FILE"
fi
has_preserved() { printf '%s' "$PRESERVED_LINES" | grep -q "^$1="; }
{
  echo "PORT=$HTTP_PORT"
  echo "DATABASE_URL=$DATABASE_URL"
  echo "ARCHIVE_DIR=$ARCHIVE_DIR"
  echo "SERVER_NAME=\"$SERVER_NAME\""
  echo "SERVER_ADDRESS=$SERVER_ADDRESS"
  echo "SERVER_PORT=$SERVER_PORT"
  [ -n "$ADMIN_TOKEN" ] && echo "ADMIN_TOKEN=$ADMIN_TOKEN"
  if [ -n "$ADMIN_TOKEN" ]; then
    # Persist the admin session-signing secret so sessions survive restarts.
    echo "SESSION_KEY_FILE=$REPO_DIR/data/session.key"
  fi
  if [ -n "$PRESERVED_LINES" ]; then
    echo ""
    echo "# --- settings preserved from the previous .env ---"
    printf '%s' "$PRESERVED_LINES"
  fi
  echo ""
  echo "# --- going-live options (see README: Going live) ---"
  if ! has_preserved SECURE_COOKIES; then
    echo "# Set to 1 once TLS terminates in front of the app (reverse proxy), so"
    echo "# the admin cookie is marked Secure and never sent over plain HTTP."
    echo "# SECURE_COOKIES=1"
  fi
  if ! has_preserved HOST; then
    echo "# Bind only to loopback once a reverse proxy on this host is in front."
    echo "# HOST=127.0.0.1"
  fi
  if ! has_preserved PUBLIC_BASE_URL; then
    echo "# Public URL of the site — enables link previews and absolute sitemap URLs."
    echo "# PUBLIC_BASE_URL=https://stats.example.com"
  fi
  if ! has_preserved PRIVACY_CONTACT; then
    echo "# Contact shown on /privacy for takedown/removal requests."
    echo "# PRIVACY_CONTACT=admin@example.com"
  fi
  if ! has_preserved DISCORD_WEBHOOK_URL; then
    echo "# Discord webhook for round-complete and server up/down notifications."
    echo "# DISCORD_WEBHOOK_URL="
  fi
} >"$ENV_FILE"
chmod 600 "$ENV_FILE"
ok "Wrote $ENV_FILE (mode 600; existing custom settings preserved)"

# The umask 077 above was only meant to keep .env's secrets private. Left in
# place, it also applies to everything built/created below (bin/, data/, ...),
# leaving them root-only and unreachable to the unprivileged service user —
# reset it now so those get normal, world-traversable permissions.
umask 022

# --- build ------------------------------------------------------------------
step_ "Build"
BUILD_VERSION="$(cd "$REPO_DIR" && git describe --tags --always --dirty 2>/dev/null || echo dev)"
LDFLAGS="-X main.version=$BUILD_VERSION"
if ! (cd "$REPO_DIR" && go build -ldflags "$LDFLAGS" -o "$BIN_DIR/spawnpoint-api" ./apps/api); then
  die "Build failed for apps/api (see compiler output above) — nothing was installed or started."
fi
if ! (cd "$REPO_DIR" && go build -ldflags "$LDFLAGS" -o "$BIN_DIR/spawnpoint-worker" ./apps/worker); then
  die "Build failed for apps/worker (see compiler output above) — nothing was installed or started."
fi
[ -x "$BIN_DIR/spawnpoint-api" ] && [ -x "$BIN_DIR/spawnpoint-worker" ] || die "Build reported success but a binary is missing from $BIN_DIR — inspect it before continuing."
# Belt-and-suspenders on top of the umask reset above: make sure the service
# user (added later, non-root) can always read+execute these regardless of
# whatever umask was active when they were built.
chmod o+rx "$BIN_DIR" "$BIN_DIR/spawnpoint-api" "$BIN_DIR/spawnpoint-worker"
ok "Built bin/spawnpoint-api and bin/spawnpoint-worker ($BUILD_VERSION)"

# --- admin account -----------------------------------------------------------
step_ "Admin account"
if [ "$CREATE_ADMIN_ACCOUNT" = 1 ]; then
  if ADMIN_CREATE_USERNAME="$ADMIN_USERNAME" ADMIN_CREATE_PASSWORD="$ADMIN_PASSWORD" DATABASE_URL="$DATABASE_URL" \
      "$BIN_DIR/spawnpoint-api" -create-admin; then
    if [ "${GENERATED_ADMIN_PASSWORD:-0}" = 1 ]; then
      box "ADMIN ACCOUNT CREATED" \
        "Username: $bold$ADMIN_USERNAME$reset" \
        "Password: $bold$ADMIN_PASSWORD$reset  (generated — shown once, save it now)" \
        "" \
        "Sign in at /admin/login. Change the password under Admin > Users."
    else
      ok "Admin account ready: $ADMIN_USERNAME"
    fi
  else
    warn "Could not create the admin account (see error above) — sign in with ADMIN_TOKEN and create one under Admin > Users instead."
  fi
else
  say "Skipped — no admin account was requested."
fi

# --- historical import ------------------------------------------------------
step_ "Historical logs"
IMPORT_DIRS=("${WATCH_DIRS[@]}")
if confirm "Import existing logs from another directory too (old archives, backups)?" n; then
  ask HIST_DIR "Directory with historical .xml/.zxml logs"
  [ -d "$HIST_DIR" ] && IMPORT_DIRS+=("$HIST_DIR") || warn "Directory not found: $HIST_DIR (skipped)"
fi
if [ "${#IMPORT_DIRS[@]}" -gt 0 ]; then
  count=$(find "${IMPORT_DIRS[@]}" -maxdepth 1 \( -name '*.xml' -o -name '*.zxml' \) 2>/dev/null | wc -l)
  if [ "$count" -gt 0 ] && confirm "Import $count existing log file(s) now? (large backlogs can take a while)" y; then
    (cd "$REPO_DIR" && set -a && . "$ENV_FILE" && set +a && \
      "$BIN_DIR/spawnpoint-worker" "${IMPORT_DIRS[@]}") || warn "Import finished with errors (see output above); already-imported logs are skipped automatically."
    ok "Import pass complete."
  fi
fi

# --- services ---------------------------------------------------------------
step_ "Services"
INSTALLED_SERVICES=0
if [ "$HAS_SYSTEMD" = 1 ] && [ "$IS_ROOT" = 1 ] && confirm "Install and start systemd services (API + worker)?" y; then
  # Running a second install on this box (e.g. a public demo alongside real
  # production stats)? Each checkout already gets its own REPO_DIR, .env,
  # port, and database — the only thing that would collide is the systemd
  # unit name, which is otherwise a fixed "spawnpoint-api.service" for
  # every install. Leave this blank and nothing about a single-instance
  # setup changes at all.
  ask INSTANCE_NAME "Instance name (only needed if this box already runs another spawnpoint — leave blank otherwise)" ""
  if [ -n "$INSTANCE_NAME" ]; then
    [[ "$INSTANCE_NAME" =~ ^[A-Za-z0-9-]+$ ]] || die "Instance name must contain only letters, numbers, and dashes."
    API_UNIT="spawnpoint-api-$INSTANCE_NAME.service"
    WORKER_UNIT="spawnpoint-worker-$INSTANCE_NAME.service"
  fi

  # Run the services as a dedicated system user, not root — this is a
  # network-facing app. Falls back to root (with a warning) only when the
  # repo lives somewhere the service user cannot reach, e.g. under /root.
  SERVICE_USER="spawnpoint"
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    NOLOGIN="$(command -v nologin || echo /usr/sbin/nologin)"
    useradd --system --user-group --no-create-home --home-dir "$REPO_DIR" --shell "$NOLOGIN" "$SERVICE_USER"
    ok "Created system user $SERVICE_USER"
  fi
  mkdir -p "$REPO_DIR/data"
  chown -R "$SERVICE_USER:$SERVICE_USER" "$REPO_DIR/data" "$ARCHIVE_DIR"

  # as_service_user CMD... — run a command as the service user for access checks.
  as_service_user() {
    if command -v runuser >/dev/null; then
      runuser -u "$SERVICE_USER" -- "$@"
    else
      su -s /bin/sh -c "$(printf '%q ' "$@")" "$SERVICE_USER"
    fi
  }

  SERVICE_SPEC="$SERVICE_USER"
  if ! as_service_user test -x "$BIN_DIR/spawnpoint-api" 2>/dev/null; then
    warn "User $SERVICE_USER cannot reach $BIN_DIR yet — attempting to fix permissions (chmod o+rx)."
    find "$REPO_DIR" -type d -exec chmod o+rx {} +
    chmod o+rx "$BIN_DIR/spawnpoint-api" "$BIN_DIR/spawnpoint-worker" 2>/dev/null || true
    if as_service_user test -x "$BIN_DIR/spawnpoint-api" 2>/dev/null; then
      ok "Fixed — $SERVICE_USER can now reach $BIN_DIR."
    else
      warn "Still unreachable by $SERVICE_USER — a parent directory outside $REPO_DIR (e.g. /root, or"
      warn "$REPO_DIR's own parent) likely isn't traversable, and this script can't safely fix paths"
      warn "outside the checkout for you."
      if confirm "Run the network-facing API/worker as root instead? (not recommended)" n; then
        SERVICE_SPEC="root"
      else
        die "Refusing to continue without a fix. Move the checkout somewhere world-traversable (e.g. /opt/spawnpoint), or grant $SERVICE_USER access to $REPO_DIR's parent directories, then re-run setup.sh — see docs/production.md."
      fi
    fi
  fi
  for dir in "${WATCH_DIRS[@]}"; do
    if [ "$SERVICE_SPEC" != root ] && ! as_service_user test -r "$dir" 2>/dev/null; then
      # Same self-heal attempted for $BIN_DIR above: this is very often just
      # a directory created by hand (or by another tool) under a restrictive
      # umask, not a genuine access-control boundary — try the easy fix
      # before bothering the operator with it.
      chmod o+rx "$dir" 2>/dev/null || true
      if as_service_user test -r "$dir" 2>/dev/null; then
        ok "Fixed permissions on $dir so $SERVICE_USER can read it."
      else
        warn "User $SERVICE_USER cannot read log directory $dir, and chmod o+rx wasn't enough to fix it."
        warn "Grant access (e.g. setfacl -m u:$SERVICE_USER:rX -R $dir, or add $SERVICE_USER to the"
        warn "group that owns $dir) or the worker will log permission errors until you do."
      fi
    fi
  done

  # This is the higher-value target on the box (DB credentials, admin
  # sessions) compared to remote-worker-setup.sh's uploader, so it gets at
  # least the same sandboxing: read-only root filesystem with explicit
  # write access only to data/archive, and read access only to whatever log
  # directories it's actually configured to watch (commonly under /home,
  # e.g. a drop folder — so ProtectHome needs a matching ReadOnlyPaths
  # carve-out rather than blocking it outright).
  RW_PATHS="$REPO_DIR/data $ARCHIVE_DIR"
  SERVICE_HARDENING="User=$SERVICE_SPEC
Group=$SERVICE_SPEC
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=$RW_PATHS
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
SystemCallArchitectures=native
LimitNOFILE=65535"
  if [ "${#WATCH_DIRS[@]}" -gt 0 ]; then
    SERVICE_HARDENING="$SERVICE_HARDENING
ReadOnlyPaths=${WATCH_DIRS[*]}"
  fi
  if [ "$SERVICE_SPEC" = root ]; then
    SERVICE_HARDENING="NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=$RW_PATHS
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
SystemCallArchitectures=native
LimitNOFILE=65535"
    if [ "${#WATCH_DIRS[@]}" -gt 0 ]; then
      SERVICE_HARDENING="$SERVICE_HARDENING
ReadOnlyPaths=${WATCH_DIRS[*]}"
    fi
  fi

  cat >"$UNIT_DIR/$API_UNIT" <<UNIT
[Unit]
Description=Spawnpoint API${INSTANCE_NAME:+ ($INSTANCE_NAME)}
After=network.target postgresql.service
Wants=postgresql.service

[Service]
Type=simple
WorkingDirectory=$REPO_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_DIR/spawnpoint-api
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
$SERVICE_HARDENING

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable "$API_UNIT" >/dev/null 2>&1
  systemctl restart "$API_UNIT"
  ok "$API_UNIT installed and started (User=$SERVICE_SPEC)"

  if [ "${#WATCH_DIRS[@]}" -gt 0 ]; then
    cat >"$UNIT_DIR/$WORKER_UNIT" <<UNIT
[Unit]
Description=Spawnpoint XML ingestion worker${INSTANCE_NAME:+ ($INSTANCE_NAME)}
After=network.target postgresql.service

[Service]
Type=simple
WorkingDirectory=$REPO_DIR
EnvironmentFile=$ENV_FILE
# Keep the watch-mode seen-file cache inside the service-user-writable data
# dir regardless of where ARCHIVE_DIR points.
Environment=WORKER_STATE_FILE=$REPO_DIR/data/worker-seen-cache.json
ExecStart=$BIN_DIR/spawnpoint-worker -watch -interval $WATCH_INTERVAL ${WATCH_DIRS[*]}
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
$SERVICE_HARDENING

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable "$WORKER_UNIT" >/dev/null 2>&1
    systemctl restart "$WORKER_UNIT"
    ok "$WORKER_UNIT installed and started (watching: ${WATCH_DIRS[*]})"
  else
    warn "No watch directory configured — worker service skipped."
  fi
  INSTALLED_SERVICES=1

  if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "^Status: active"; then
    if ! ufw status 2>/dev/null | grep -qE "^$HTTP_PORT(/tcp)? "; then
      warn "ufw is active and doesn't currently allow port $HTTP_PORT."
      confirm "Run 'ufw allow $HTTP_PORT/tcp' now?" y && ufw allow "$HTTP_PORT/tcp" >/dev/null
    fi
  fi

  say ""
  say "Verifying the services actually came up ..."
  VERIFY_FAILED=0
  sleep 1
  if systemctl is-active --quiet "$API_UNIT"; then
    HEALTHY=0
    for _ in $(seq 1 10); do
      if curl -fsS -m 2 "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null 2>&1; then HEALTHY=1; break; fi
      sleep 1
    done
    if [ "$HEALTHY" = 1 ]; then
      ok "$API_UNIT is up and /healthz responds."
    else
      warn "$API_UNIT is running but /healthz never responded on port $HTTP_PORT."
      warn "Check:  journalctl -u $API_UNIT -n 50 --no-pager"
      VERIFY_FAILED=1
    fi
  else
    warn "$API_UNIT is not active. Check:  journalctl -u $API_UNIT -n 50 --no-pager"
    VERIFY_FAILED=1
  fi
  if [ "${#WATCH_DIRS[@]}" -gt 0 ]; then
    if systemctl is-active --quiet "$WORKER_UNIT"; then
      ok "$WORKER_UNIT is up."
    else
      warn "$WORKER_UNIT is not active. Check:  journalctl -u $WORKER_UNIT -n 50 --no-pager"
      VERIFY_FAILED=1
    fi
  fi
  [ "$VERIFY_FAILED" = 0 ] && ok "Verification passed."
else
  say "Skipping systemd. Run the processes manually:"
  say "  set -a; . $ENV_FILE; set +a"
  say "  $BIN_DIR/spawnpoint-api"
  if [ "${#WATCH_DIRS[@]}" -gt 0 ]; then
    say "  $BIN_DIR/spawnpoint-worker -watch -interval ${WATCH_INTERVAL:-15s} ${WATCH_DIRS[*]}"
  fi
fi

# --- summary ----------------------------------------------------------------
step_ "Done"
PUBLIC_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
box "SETUP COMPLETE" \
  "Web UI / API:   http://${PUBLIC_IP}:$HTTP_PORT/" \
  "Health check:   http://localhost:$HTTP_PORT/healthz" \
  "Admin login:    http://localhost:$HTTP_PORT/admin/login"
if [ "${#RUN_WARNINGS[@]}" -gt 0 ]; then
  # Everything above scrolls past a lot of apt/build/migration output — a
  # warning from step 2 is easy to lose by step 10. Recap them all here,
  # right next to the box the operator is most likely to actually read.
  say ""
  warn "This run had ${#RUN_WARNINGS[@]} warning(s) worth a second look:"
  for w in "${RUN_WARNINGS[@]}"; do
    say "  - $w"
  done
fi
if [ "$CREATE_ADMIN_ACCOUNT" = 1 ]; then
  say "Sign in with the admin account created above (username: $bold$ADMIN_USERNAME$reset)."
elif [ -n "$ADMIN_TOKEN" ]; then
  say "No admin account was created — sign in with the admin token from $ENV_FILE,"
  say "then create your personal account under Admin > Users."
else
  say "The admin area is disabled (no account and no ADMIN_TOKEN). Re-run setup.sh to add one."
fi
say ""
say "Configuration:  $ENV_FILE"
say "Raw log vault:  $ARCHIVE_DIR"
if [ "$INSTALLED_SERVICES" = 1 ]; then
  say "Services:       systemctl status $API_UNIT $WORKER_UNIT"
  if [ "${VERIFY_FAILED:-0}" = 1 ]; then
    warn "One or more services didn't verify cleanly above — check the journalctl commands before trusting this install."
  fi
fi
say ""
say "Also worth a look:  /leaderboard  /awards  /trends"
say ""
say "After parser upgrades you can rebuild the whole database from the archive:"
say "  set -a; . $ENV_FILE; set +a"
say "  $BIN_DIR/spawnpoint-worker -reingest $ARCHIVE_DIR"
