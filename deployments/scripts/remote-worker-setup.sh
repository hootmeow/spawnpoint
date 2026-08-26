#!/usr/bin/env bash
#
# Spawnpoint — remote log-upload worker installer.
#
# Run this on the box that runs YOUR BF1942 dedicated server(s) to securely
# ship completed round XML logs to a central spawnpoint site over
# HTTPS. It builds a small worker that only:
#   - watches your server's mods/<mod>/logs directory for finished rounds
#   - POSTs each completed log to the site's ingest endpoint with a bearer
#     token scoped to exactly that upload endpoint
#
# It does NOT need, and never asks for, SSH/database/admin access to the
# receiving server. The token can only submit logs; it cannot read anything
# back. The site owner can revoke or rotate it at any time without touching
# your box.
#
# You'll need, from the site owner, per game-server instance:
#   1. The ingest URL   (e.g. https://stats.example.com/api/ingest/upload)
#   2. An ingest API key scoped to that instance
#
# Safe to re-run: existing instances are left alone unless you choose to
# reconfigure them, and you can add more instances later.

set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/hootmeow/spawnpoint.git}"
INSTALL_DIR="${INSTALL_DIR:-/opt/spawnpoint-worker}"
SRC_DIR="$INSTALL_DIR/src"
BIN_DIR="$INSTALL_DIR/bin"
ETC_DIR="$INSTALL_DIR/etc"
VAR_DIR="$INSTALL_DIR/var"
UNIT_DIR="/etc/systemd/system"
SERVICE_USER="spawnpoint-worker"

bold=$(tput bold 2>/dev/null || true)
red=$(tput setaf 1 2>/dev/null || true)
green=$(tput setaf 2 2>/dev/null || true)
yellow=$(tput setaf 3 2>/dev/null || true)
reset=$(tput sgr0 2>/dev/null || true)

say()  { printf '%s\n' "$*"; }
head_() { printf '\n%s== %s ==%s\n' "$bold" "$*" "$reset"; }
ok()   { printf '%s\xe2\x9c\x94%s %s\n' "$green" "$reset" "$*"; }
warn() { printf '%s!%s %s\n' "$yellow" "$reset" "$*"; }
die()  { printf '%s\xe2\x9c\x98 %s%s\n' "$red" "$*" "$reset" >&2; exit 1; }

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

ask_secret() {
  local var="$1" prompt="$2" answer
  read -r -s -p "$prompt: " answer
  printf '\n'
  printf -v "$var" '%s' "$answer"
}

confirm() {
  local prompt="$1" default="${2:-y}" answer hint
  if [ "$default" = y ]; then hint="Y/n"; else hint="y/N"; fi
  read -r -p "$prompt [$hint]: " answer
  answer="${answer:-$default}"
  case "$answer" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

# as_service_user CMD... — run a command as the worker's system user, for
# access checks. Prefers runuser (not present on minimal/Alpine-style images).
as_service_user() {
  if command -v runuser >/dev/null; then
    runuser -u "$SERVICE_USER" -- "$@"
  else
    su -s /bin/sh -c "$(printf '%q ' "$@")" "$SERVICE_USER"
  fi
}

# ---------------------------------------------------------------------------
head_ "Spawnpoint remote worker setup"
say "This installs an unprivileged uploader that ships your server's round"
say "logs to a stats site over HTTPS. It never gets SSH, database, or admin"
say "access to that site — only a per-instance upload token you were given."

head_ "Preflight"
[ "$(id -u)" = 0 ] || die "Run this as root (sudo). It creates a system user, installs to $INSTALL_DIR, and manages systemd units."
command -v git >/dev/null || die "git not found. Install it (e.g. apt install git) and re-run."
ok "git $(git --version | awk '{print $3}')"
command -v go >/dev/null || die "Go toolchain not found. Install Go 1.22+ (https://go.dev/dl/) and re-run."
GO_VER="$(go version | awk '{print $3}')"
GO_MINOR="$(printf '%s' "$GO_VER" | sed -nE 's/^go1\.([0-9]+).*/\1/p')"
[ -n "$GO_MINOR" ] && [ "$GO_MINOR" -lt 22 ] && die "Go 1.22+ is required, found $GO_VER."
ok "$GO_VER"
command -v systemctl >/dev/null && [ -d /run/systemd/system ] || die "systemd not found — this script only supports systemd hosts."
ok "systemd present"
command -v curl >/dev/null || die "curl not found. Install it (e.g. apt install curl) and re-run."

# --- fetch + build -----------------------------------------------------------
head_ "Fetch and build"
mkdir -p "$INSTALL_DIR"
if [ -d "$SRC_DIR/.git" ]; then
  git -C "$SRC_DIR" fetch --tags origin
else
  git clone --no-checkout "$REPO_URL" "$SRC_DIR"
fi
# Default to the latest release tag, not a moving branch, so an operator
# running this months from now builds a known-good point instead of whatever
# happens to be on main that day. Falls back to 'main' if the repo has no
# tags yet.
LATEST_TAG="$(git -C "$SRC_DIR" tag --list --sort=-v:refname | head -1)"
ask GIT_REF "Git ref to build (tag/branch/commit the site owner gave you)" "${LATEST_TAG:-main}"
git -C "$SRC_DIR" checkout --detach "$GIT_REF"
BUILD_VERSION="$(git -C "$SRC_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
mkdir -p "$BIN_DIR"
(cd "$SRC_DIR" && go build -ldflags "-X main.version=$BUILD_VERSION" -o "$BIN_DIR/spawnpoint-worker" ./apps/worker)
ok "Built $BIN_DIR/spawnpoint-worker ($BUILD_VERSION)"

# --- system user --------------------------------------------------------------
head_ "System user"
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  NOLOGIN="$(command -v nologin || echo /usr/sbin/nologin)"
  useradd --system --user-group --no-create-home --home-dir "$INSTALL_DIR" --shell "$NOLOGIN" "$SERVICE_USER"
  ok "Created system user $SERVICE_USER (no login shell, no home directory)"
else
  ok "System user $SERVICE_USER already exists"
fi
mkdir -p "$ETC_DIR" "$VAR_DIR"
chmod 700 "$ETC_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$VAR_DIR" "$ETC_DIR"

# --- one or more server instances --------------------------------------------
INSTALLED=()
add_instance=1
while [ "$add_instance" = 1 ]; do
  head_ "Game server instance"

  while true; do
    ask INSTANCE_NAME "Short instance name (letters, numbers, dashes only — used in file/service names)" ""
    [[ "$INSTANCE_NAME" =~ ^[A-Za-z0-9-]+$ ]] && [ -n "$INSTANCE_NAME" ] && break
    warn "Use only letters, numbers, and dashes."
  done
  ENV_FILE="$ETC_DIR/$INSTANCE_NAME.env"
  UNIT_NAME="spawnpoint-worker-$INSTANCE_NAME.service"
  if [ -f "$ENV_FILE" ] && ! confirm "Instance '$INSTANCE_NAME' already configured. Reconfigure it?" n; then
    say "Skipping $INSTANCE_NAME."
  else
    say ""
    say "Looking for this instance's BF1942 log directory (mods/<mod>/logs) ..."
    mapfile -t FOUND < <(find /home /opt /srv /usr/local /root -maxdepth 7 -type d -path '*/mods/*/logs' 2>/dev/null | head -10 || true)
    LOG_DIR=""
    if [ "${#FOUND[@]}" -gt 0 ]; then
      i=1
      for d in "${FOUND[@]}"; do say "  [$i] $d"; i=$((i + 1)); done
      say "  [$i] Somewhere else (enter a path)"
      read -r -p "Choice [1]: " pick
      pick="${pick:-1}"
      if [[ "$pick" =~ ^[0-9]+$ ]] && [ "$pick" -ge 1 ] && [ "$pick" -le "${#FOUND[@]}" ]; then
        LOG_DIR="${FOUND[$((pick - 1))]}"
      fi
    fi
    if [ -z "$LOG_DIR" ]; then
      ask LOG_DIR "Log directory path for this instance" ""
    fi
    [ -d "$LOG_DIR" ] || die "Directory not found: $LOG_DIR"

    ask UPLOAD_URL "Ingest URL (from the site owner, e.g. https://stats.example.com/api/ingest/upload)" ""
    [ -n "$UPLOAD_URL" ] || die "An ingest URL is required."
    case "$UPLOAD_URL" in
      http://*)
        SCHEME_HOST="${UPLOAD_URL#http://}"
        HOST_ONLY="${SCHEME_HOST%%/*}"
        HOST_ONLY="${HOST_ONLY%%:*}"
        if [ "$HOST_ONLY" != "localhost" ] && [ "$HOST_ONLY" != "127.0.0.1" ] && [ "$HOST_ONLY" != "::1" ]; then
          warn "This URL is plain http:// to a non-loopback host."
          warn "Your upload API key would be sent in the clear on every upload."
          warn "Ask the site owner for an https:// URL instead."
          confirm "Continue anyway with this insecure URL?" n || die "Aborted — get an https:// ingest URL and re-run."
        fi
        ;;
    esac
    ask_secret UPLOAD_KEY "Ingest API key for this instance (input hidden)"
    [ -n "$UPLOAD_KEY" ] || die "An API key is required."
    if [[ ! "$UPLOAD_KEY" =~ ^bfps_[0-9a-f]{40,}$ ]]; then
      warn "This doesn't look like a spawnpoint ingest key (expected 'bfps_' followed by hex)."
      warn "Double-check it was copied in full — trailing/leading whitespace or a"
      warn "truncated paste are the usual cause."
      confirm "Use it anyway?" n || die "Aborted — get the exact key from the site owner and re-run."
    fi

    say ""
    say "Checking the ingest endpoint before installing anything..."
    HTTP_CODE="$(curl -s -o /dev/null -m 15 -w '%{http_code}' -X POST \
      -H "Authorization: Bearer $UPLOAD_KEY" -H "X-Original-Name: connectivity-check.xml" \
      --data-binary "" "$UPLOAD_URL" || true)"
    case "$HTTP_CODE" in
      400) ok "Reachable and the key was accepted." ;;
      401) die "The site rejected this API key (401 Unauthorized). Get a fresh one from the site owner and re-run." ;;
      403) die "The server entry for this instance is disabled (403) on the site. Ask the site owner to enable it in /admin/servers." ;;
      000|"") warn "Could not reach $UPLOAD_URL (DNS/TLS/firewall?). Continuing anyway — the service will keep retrying on its own once it's up, but check the URL if uploads never show up." ;;
      *) warn "Unexpected response ($HTTP_CODE) from the ingest endpoint. Continuing, but mention this to the site owner if uploads don't show up." ;;
    esac

    umask 077
    {
      echo "UPLOAD_URL=$UPLOAD_URL"
      echo "UPLOAD_API_KEY=$UPLOAD_KEY"
      echo "WORKER_STATE_FILE=$VAR_DIR/$INSTANCE_NAME-seen-cache.json"
    } >"$ENV_FILE"
    chown "$SERVICE_USER:$SERVICE_USER" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    ok "Wrote $ENV_FILE (mode 600, owned by $SERVICE_USER — nobody else can read the key)"

    if ! as_service_user test -r "$LOG_DIR" 2>/dev/null; then
      warn "$SERVICE_USER cannot read $LOG_DIR yet."
      warn "Grant read access, e.g.: setfacl -m u:$SERVICE_USER:rX -R \"$LOG_DIR\""
      warn "(or add $SERVICE_USER to the group that owns it). The service will log"
      warn "permission errors until this is fixed."
    fi

    cat >"$UNIT_DIR/$UNIT_NAME" <<UNIT
[Unit]
Description=Spawnpoint remote upload worker ($INSTANCE_NAME)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_DIR/spawnpoint-worker -watch -interval 15s "$LOG_DIR"
Restart=on-failure
RestartSec=5
User=$SERVICE_USER
Group=$SERVICE_USER
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadOnlyPaths="$LOG_DIR"
ReadWritePaths=$VAR_DIR
CapabilityBoundingSet=
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictRealtime=true
RestrictSUIDSGID=true
SystemCallArchitectures=native
LimitNOFILE=65535
KillSignal=SIGINT
# This process reads small XML files and makes one HTTPS POST per completed
# round — a hard ceiling here is a tripwire, not a tuning knob, in case the
# binary is ever tricked into doing something it shouldn't.
MemoryMax=256M
CPUQuota=25%

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable "$UNIT_NAME" >/dev/null 2>&1
    systemctl restart "$UNIT_NAME"
    ok "$UNIT_NAME installed and started, watching $LOG_DIR"
    INSTALLED+=("$UNIT_NAME")
  fi

  confirm "Configure another server instance?" n && add_instance=1 || add_instance=0
done

# --- summary ------------------------------------------------------------------
head_ "Done"
if [ "${#INSTALLED[@]}" -gt 0 ]; then
  say "Installed/updated services:"
  for u in "${INSTALLED[@]}"; do say "  - $u"; done
  say ""
  say "Check status:   systemctl status ${INSTALLED[*]}"
  say "Watch logs:     journalctl -u ${INSTALLED[0]} -f"
else
  say "No new or changed instances."
fi
say ""
say "Each instance's API key lives only in $ETC_DIR/<name>.env (mode 600, owned"
say "by $SERVICE_USER). Nothing else on this box needs it. If this box is ever"
say "compromised, tell the site owner so they can revoke/rotate the affected"
say "key(s) — that alone fully closes the exposure; no action needed on the"
say "receiving server."
