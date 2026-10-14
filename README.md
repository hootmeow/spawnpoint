# Spawnpoint

Self-hosted statistics platform for Battlefield 1942 / Battlefield Vietnam
dedicated servers — a modern successor to select(bf). One PostgreSQL database,
one Go API binary serving a zero-JavaScript dark-themed web UI, and one
ingestion worker that parses the engine's per-round XML event logs.

## Get started — pick your situation

New here? Follow the **[Guided setup](docs/getting-started.md)** — one
copy-paste walkthrough, no choices to make. Otherwise jump to your case:

| I want to… | Go to |
| --- | --- |
| **Get running fast** — one Linux box + a BF1942 server → a stats site | **[Guided setup →](docs/getting-started.md)** |
| Run it with **Docker / containers** | [Docker](#docker) |
| Ship logs from a **game server on another machine** | [Remote game server](#multiple-game-servers) |
| Use a **PostgreSQL I already run**, or see all installer options | [Quick start](#quick-start) |
| **Take it public** (TLS, hardening) | [Going live](#going-live-public-deployment) |
| Look up **what I need** or a **config option** | [Requirements](#requirements) · [Configuration](#configuration-env) |

## Features

- **Full event ingestion** — parses `ev_*.xml` / `.zxml` round logs into
  normalized events, round stats, games, and rounds. Every raw log is archived
  content-addressed (`<sha256>__<original-name>`), so the entire database can
  be rebuilt from the archive at any time with `-reingest`.
- **Player identity across nicknames** — the engine logs a per-CD-key
  `playerKeyHash` next to each human `createPlayer`. Ingestion anchors players
  to that keyhash, records every nickname as an alias, and automatically merges
  older nickname-keyed rows when a keyhash reveals they belong to the same
  human. Two humans sharing a nickname stay separate players.
- **Vehicle kill attribution** — see [Weapon Attribution](#weapon-attribution).
- **Round outcomes & medals** — victory type (tickets / all flags /
  objectives / time limit), per-round gold medals in six categories with a
  medal case on every profile, and per-round time-played tracking
  (hours played, score per minute).
- **Web UI** (server-rendered inline-SVG charts, no client-side dependencies):
  - `/` dashboard, `/players`, `/player?id=` profiles (performance charts,
    K/D donut, weapon/kit/vehicle breakdowns, victims & nemeses, aliases,
    medal case, time played, and a self-serve
    `/player/export?id=&format=csv|json` stats download)
  - `/leaderboard` with metric / map / period / min-rounds / bot filters
  - `/awards` — gold/silver/bronze career medals plus single-round records
  - `/map?name=` — kill-origin and death-zone density heatmaps from logged
    world positions, plus weapon/vehicle/player pressure
  - `/chat` — searchable battlefield chat log
  - `/live` — real-time server status (map, players, tickets) over
    GameSpy queries, one card per registered server
  - `/trends`, `/maps`, `/rounds`, `/weapons`, `/vehicles`, `/kits`,
    `/compare`, `/rivalry`
  - `/clans`, `/clan?tag=` — players grouped by `[TAG]`/`-TAG-`/`=TAG=`/
    `{TAG}` nickname convention, with a per-clan roster and frequent-opponent
    list
  - `/admin` management area behind `/admin/login`: named admin accounts
    with two roles (`admin` for everything; `moderator` for dashboards,
    player moderation, and identity tools), server-side revocable sessions,
    and a full audit log of admin actions. `ADMIN_TOKEN` bootstraps the
    first account and stays available as a break-glass credential. Inside:
    ingest health dashboard and job monitor, server registry with
    per-server ingest API keys, player moderation (hide from public pages,
    force bot/human), manual identity merge/split tools, and user
    management
- **JSON API** — `/api/players`, `/api/rounds`, `/api/weapons`,
  `/api/player-search`, `/api/events`, and more; `/healthz` for probes.
- `/about`, `/privacy`, and `/terms` — what the site is, what data it shows,
  how to request changes, and acceptable use / no-warranty terms.
- **SEO/crawling** — `og:image` and Twitter card previews (needs
  `PUBLIC_BASE_URL`), `/sitemap.xml`, and `/robots.txt` (opts `/admin`,
  `/api`, `/search`, and `/chat` out of indexing, points crawlers at the
  sitemap).
- **Discord notifications** — set `DISCORD_WEBHOOK_URL` to post a
  round-complete summary (map, top frag, round medals) after each live round,
  and an online/offline message whenever a registered server's status query
  stops or resumes responding. Round-complete notifications are suppressed
  for anything older than `ROUND_NOTIFY_WINDOW` (default 20m), so backlog
  imports and `-reingest` runs don't spam the channel.
- **Monitoring** — `spawnpoint-healthcheck.timer` polls `/healthz` and
  posts to the same Discord webhook after sustained failures; see
  [Monitoring](docs/production.md#monitoring) for install steps and why it's
  a first layer, not a replacement for real off-box uptime monitoring.

## Requirements

This is a Linux server application. It runs on the machine that hosts the stats
site — that can be the same box as your BF1942 server or a separate one.

**Hardware** — modest. A 1 vCPU / 1 GB RAM VPS handles a couple of busy
servers; 2 GB is comfortable. Disk is dominated by the raw-log archive plus the
database — budget roughly 1–2 GB per active server per year, plus room for
PostgreSQL. The site is fully server-rendered, so visitors only need a browser.

**Operating system** — a modern 64-bit Linux with systemd (Ubuntu 20.04+,
Debian 11+, Rocky/Alma/RHEL 8+, or similar). macOS works for local development;
the server itself isn't supported on Windows (use WSL2).

**Software** — the `setup.sh` installer expects these already on the host:

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.22+ | builds the binaries — <https://go.dev/dl/> |
| PostgreSQL | 13+ | 16 recommended; `setup.sh` can install it for you |
| `psql` (PostgreSQL client) | any | e.g. `apt install postgresql-client` |
| `git`, `curl` | any | clone the repo, health-check the API |

Using the **Docker** path instead? Then you only need Docker Engine + Docker
Compose v2 — no Go, `psql`, or PostgreSQL on the host.

**Network / ports**

| Port | Component | Notes |
| --- | --- | --- |
| `8080` (`PORT`) | stats app | web UI + API — put TLS in front before exposing it publicly |
| `5432` | PostgreSQL | keep bound to localhost or a private network |
| game + query port (e.g. `14567` / `23000`) | your BF1942 server | only needed for the `/live` status page |

**From your BF1942 server** you also need DICE XML event logging enabled and a
readable mod `logs` directory — see
[BF1942 Server Integration](#bf1942-server-integration).

## Quick start

> New here? The **[Guided setup](docs/getting-started.md)** walks through this
> step by step with checkpoints.

```bash
git clone https://github.com/hootmeow/spawnpoint
cd spawnpoint
sudo ./setup.sh
```

Prefer Docker? See [Docker](#docker) below for a Compose-based install
instead of `setup.sh`.

The interactive installer covers the common self-hosting shapes:

- **Standalone** — provisions a local PostgreSQL role + database (installs
  PostgreSQL via your package manager if needed).
- **Existing database** — point it at any PostgreSQL you already run; it
  verifies the connection and applies only the missing schema migrations
  (tracked in `schema_migrations`, safe on databases created before the
  installer existed).
- **Log sources** — auto-detects local BF1942/BFV installs (`*/mods/*/logs`),
  or watches any drop directory that logs are rsync'd/FTP'd into, or sets up
  the web UI only. Multiple directories are supported.
- **Backlog import** — one-shot import of historical `.xml`/`.zxml` logs
  before the watcher takes over (already-seen logs are deduplicated by content
  hash, so this is safe to repeat).
- **systemd** — installs and starts `spawnpoint-api` and
  `spawnpoint-worker` units running as a dedicated `spawnpoint` system user
  (or prints the manual run commands when run without root/systemd).

Re-running `setup.sh` is safe: current `.env` values become the defaults,
hand-added `.env` settings (`HOST`, `SECURE_COOKIES`, `PUBLIC_BASE_URL`, …)
are preserved, migrations are skipped when already applied, and units are
refreshed in place.

## Going live (public deployment)

`setup.sh` gives you a working install. Before you expose it to the internet,
walk this checklist — most items are one line, and the
[Production Runbook](docs/production.md) has the full detail.

1. **Strong admin token.** `ADMIN_TOKEN` bootstraps the first admin account
   and remains a break-glass credential with full access. Use a
   long random value (`openssl rand -hex 24` — `setup.sh` generates one), and
   once signed in, create a named admin account under **Admin → Users** —
   day-to-day work should happen on accounts (individually revocable,
   role-scoped, audited), not the shared token.
2. **Put TLS in front.** The app speaks plain HTTP and must never be exposed on
   `PORT` directly. Terminate TLS with Nginx or Caddy and reverse-proxy to it;
   the [Nginx + Cloudflare guide](docs/nginx-cloudflare-reverse-proxy.md) is a
   copy-paste walkthrough. Then set `HOST=127.0.0.1` so the app is only
   reachable through the proxy.
3. **Set `SECURE_COOKIES=1`** once TLS is in front, so the admin session cookie
   is never sent in cleartext. (Leave it unset only while testing over plain
   HTTP — the app logs a warning to remind you.)
4. **Lock down PostgreSQL** — bound to localhost or a private address, never the
   public internet, using a least-privilege role.
5. **Set `PUBLIC_BASE_URL`** (e.g. `https://stats.example.com`) for correct link
   previews and `/sitemap.xml`, and **`PRIVACY_CONTACT`** for the `/privacy`
   takedown contact.
6. **Turn on backups** — the `spawnpoint-backup.timer` unit
   ([Backups](docs/production.md#backups)).
7. **Watch health** — `/healthz` for uptime probes, and the
   `spawnpoint-healthcheck.timer` as a first alerting layer
   ([Monitoring](docs/production.md#monitoring)).

## Updating

Releases are tagged `vX.Y.Z`. To update a `setup.sh`-installed checkout to the
latest release:

```bash
sudo ./update.sh
```

This fetches tags, checks out the newest one, rebuilds the binaries with the
version embedded, applies any new schema migrations (skipping ones already
recorded in `schema_migrations`), and restarts the systemd units. It refuses
to run over uncommitted local changes and prints a changelog of commits
between your current tag and the target before asking for confirmation.

Pin or roll back to a specific release:

```bash
./update.sh --tag v1.2.0
```

Rolling back is safe because migrations here only add tables/views/indexes —
running older code against a newer schema doesn't break anything. Check the
running version at any time with `spawnpoint-api -version` or on the
`/admin` overview page.

## Docker

The root `Dockerfile` builds three small images (`api`, `worker`, `admin`)
from one multi-stage build via `--target`. For a full stack — Postgres, the
one-shot migration runner, api, and worker — use the production Compose file
(requires Docker Compose v2, i.e. the `docker compose` CLI):

```bash
cd deployments/compose
cp compose.env.example .env   # fill in a real POSTGRES_PASSWORD, ADMIN_TOKEN, etc
docker compose -f docker-compose.prod.yml up -d --build
```

Mount your BF1942/BFV server's log directory at `BF1942_LOG_DIR` (set in
`.env`) so the worker container can watch it; its own content-addressed
archive lives in a separate named volume so it survives container rebuilds.
`docker-compose.dev.yml` in the same directory is a lighter dev-only stack
(`go run`, no worker service, no automatic migrations) for iterating on the
code itself.

To update a Docker deployment, pull/rebuild and re-run `up -d --build` — the
`migrate` service re-applies only new migrations before api/worker start,
same as `update.sh` does for a bare-metal install.

## Manual setup

```bash
# 1. database + schema
createdb spawnpoint
psql "$DATABASE_URL" -c 'create extension if not exists pg_trgm; create extension if not exists pgcrypto;'
for f in migrations/*.sql; do psql "$DATABASE_URL" -f "$f"; done

# 2. build
go build -o bin/spawnpoint-api ./apps/api
go build -o bin/spawnpoint-worker ./apps/worker

# 3. run
set -a; . ./.env; set +a
bin/spawnpoint-api &
bin/spawnpoint-worker -watch -interval 15s /path/to/bf1942/mods/bf1942/logs
```

### Configuration (`.env`)

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | — | PostgreSQL connection URL (required) |
| `PORT` | `8080` | Web UI / API listen port |
| `HOST` | *(unset = all interfaces)* | Set to `127.0.0.1` when running behind a reverse proxy on the same host (see [Nginx + Cloudflare](docs/nginx-cloudflare-reverse-proxy.md)) |
| `ARCHIVE_DIR` | `data/raw-logs` | Content-addressed raw log archive |
| `SERVER_NAME` | `BF1942 Server` | Game server display name |
| `SERVER_ADDRESS` / `SERVER_PORT` | `127.0.0.1` / `14567` | Game server metadata |
| `ADMIN_TOKEN` | *(empty)* | Break-glass admin credential: sign in with it at `/admin/login` (then create named accounts under Admin → Users), or send it as a bearer header for scripts. Use a long random value. With it unset, the admin area works only via existing admin accounts |
| `SECURE_COOKIES` | *(unset)* | Set to `1` when TLS terminates in front of the app so the admin session cookie is marked `Secure`. Leave unset only for local/plain-HTTP testing |
| `SESSION_KEY_FILE` | `data/session.key` | Where the admin session-signing secret is persisted so sessions survive restarts. Must be writable by the service user |
| `MAX_LOG_FILE_BYTES` | `104857600` | Ingestion size cap per (decompressed) log |
| `MAX_LOG_EVENTS` | `5000000` | Max events parsed from a single log, bounding parser memory against a malicious or oversized log |
| `RATE_LIMIT_RPS` | `20` | Per-IP request rate limit (burst 2x; `0` disables) |
| `STALE_INGEST_MINUTES` | `120` | `/healthz` turns 503 and `/admin` warns when no ingest job for this long |
| `UPLOAD_URL` / `UPLOAD_API_KEY` | — | Worker-only: ship logs to a central API instead of a local DB |
| `PRIVACY_CONTACT` | — | Shown on `/privacy` for takedown/removal requests; a bare email renders as `mailto:`, anything else (URL, form) is used as-is |
| `DISCORD_WEBHOOK_URL` | — | Round-complete and server up/down notifications; unset disables both |
| `ROUND_NOTIFY_WINDOW` | `20m` | Suppresses round-complete notifications for rounds older than this (backlog/reingest safety) |
| `SERVER_WATCH_INTERVAL` | `60s` | Worker-only: how often `-watch` mode polls registered servers for up/down transitions |
| `PUBLIC_BASE_URL` | — | Site's public URL (e.g. `https://stats.example.com`, no trailing slash); enables `og:image`/`twitter:image` previews and absolute URLs in `/sitemap.xml` |
| `HEALTHCHECK_FAIL_THRESHOLD` | `3` | Consecutive `/healthz` failures before `spawnpoint-healthcheck.timer` posts a Discord alert (see [Monitoring](docs/production.md#monitoring)) |

### Worker modes

```bash
bin/spawnpoint-worker -watch -interval 15s <dir>...   # follow live logs
bin/spawnpoint-worker <dir-or-file>...                # one-shot import
bin/spawnpoint-worker -watch \
  -server-dir "Coop Bots@127.0.0.1:14567=/srv/bf1942/coop/logs" \
  -server-dir "Infantry Only@127.0.0.1:14568=/srv/bf1942/inf/logs"
bin/spawnpoint-worker -reingest data/raw-logs         # rebuild DB from archive
bin/spawnpoint-worker -parse-only <file>              # parser dry run
bin/spawnpoint-worker -watch \
  -upload-url https://stats.example.com/api/ingest/upload \
  -upload-key bfps_...  <dir>...                          # remote game server
```

Use repeatable `-server-dir` entries when multiple local servers write logs on
the same box. Each entry maps one log directory to a server row, so aggregate
pages can show either all servers or a single server without mixing bot-heavy
and human-only populations unintentionally.

`-reingest` purges the previously stored games, rounds, events, and stats for
each log (matched by content hash) and re-ingests it with the current parser —
use it after parser upgrades so improvements (vehicle attribution, identity
linking) apply to history. Players, aliases, and identities are preserved.

### Multiple game servers

Each worker registers itself by `SERVER_NAME`; games, rounds, and events are
tagged with that server, and `/leaderboard`, `/trends`, `/rounds`, and
`/awards` grow a server filter as soon as more than one server has data.
Player identity (keyhash) is global, so the same player keeps one profile
across servers.

For a game server on another machine, don't point it at the database — create
the server in `/admin/servers`, generate an ingest API key, and run the worker
there in `-upload-url` mode (or set `UPLOAD_URL` / `UPLOAD_API_KEY` in its
env). It ships each completed log over HTTPS; the central API parses,
archives, and dedupes by content hash.

The remote operator doesn't need database, SSH, or admin access to your
site — only the ingest URL and the one API key you generated for their
instance. Send them
[`deployments/scripts/remote-worker-setup.sh`](deployments/scripts/remote-worker-setup.sh):
it builds the worker, creates an unprivileged system user, checks the URL/key
before installing anything, and installs a hardened systemd service per game
server instance (handles multiple instances on one box). Manual setup with
the flags above still works if they'd rather run it by hand.

## Round Timestamps

The DICE XML log only contains timestamps relative to round start; the only
absolute clock is the log file name (`ev_<port>-<YYYYMMDD>_<HHMM>.xml`).
Ingestion parses `games.started_at` from that name, and the archive keeps files
as `<sha256>__<original-name>` so both the content hash and the original name
survive re-ingestion.

## Weapon Attribution

The BF1942 engine logs `weapon="(none)"` on `scoreEvent` whenever a kill was not
made with a handheld weapon. The parser resolves these by tracking vehicle
occupancy per player slot (`enterVehicle`/`exitVehicle`/death/spawn) and
attributing weaponless kills to the vehicle or stationary gun the killer
occupied, with `_PCO<n>` position suffixes collapsed (e.g. `Hanomag_MG42_PCO1`
becomes `Hanomag_MG42`). Kills that cannot be attributed (explosions, log gaps)
keep an empty weapon and stay out of the weapon tables.

## BF1942 Server Integration

The worker is compatible with BFSRM/BFSMD-style managers and plain BF1942 Linux
dedicated binaries. The server side only needs DICE XML event logging enabled
and a writable mod log directory, such as:

```text
/home/bf1942_user/bf1942/mods/bf1942/logs
```

See [BFSRM and Linux Dedicated Server Stats Setup](docs/bfsrm-linux-stats-setup.md)
for the exact manager/script steps to create the log directory, enable
`game.serverEventLogging`, and point the Spawnpoint worker at the XML files.

For database roles, backups, and hardened systemd templates, see the
[Production Runbook](docs/production.md). For a step-by-step public-internet
setup — Nginx terminating a Cloudflare Origin Certificate, locked down so the
app is only reachable through it — see
[Nginx + Cloudflare Reverse Proxy](docs/nginx-cloudflare-reverse-proxy.md).

## Project Layout

```text
apps/api/              HTTP API + server-rendered UI (theme, charts, pages)
apps/worker/           log watcher / importer / reingester
bflog/                 Battlefield XML parser + vehicle-kill attribution (public: importable by other tools)
internal/storage/      PostgreSQL ingestion, identity linking, analytics
domain/                normalized event types (public)
migrations/            schema (applied by setup.sh/update.sh, tracked in schema_migrations)
fixtures/              parser fixtures
deployments/           production + dev docker-compose stacks, systemd templates, backups
docs/                  architecture and production notes
Dockerfile              multi-stage build for the api/worker/admin images
setup.sh               interactive installer
update.sh              updates an existing install to the latest (or a pinned) release
```

## Design Priorities

- Historical correctness before real-time features.
- Raw event storage before aggregate-only stats.
- Self-hostable deployment by default.
- Small, testable parser core (`go test ./...`).

## License

[MIT](LICENSE). This is an unofficial fan project — not affiliated with or
endorsed by EA or DICE.
