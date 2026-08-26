# Production Runbook

This project is intentionally self-hostable: one PostgreSQL database, one API
process, and one XML ingestion worker.

## Database

Create the database and apply migrations in order:

```bash
createdb spawnpoint
psql "$DATABASE_URL" -c 'create extension if not exists pg_trgm; create extension if not exists pgcrypto;'
for f in migrations/*.sql; do psql "$DATABASE_URL" -f "$f"; done
```

For production, create separate API, worker, and admin roles after migrations:

```bash
psql "$ADMIN_DATABASE_URL" -f deployments/postgres/roles.sql
```

Use the API role in the API service, the worker role in the worker service, and
keep the admin role for migrations, summary refreshes, and maintenance.

The production hardening migration adds:

- detail-query compatibility views for vehicle and kit stats
- materialized summary views for player/map/weapon/vehicle dashboards
- indexes for event timelines, player lookups, score filters, map filters, and
  text search
- ingest job tracking for parsed, skipped, and failed import visibility

Refresh summary views after large backfills:

```bash
go run ./apps/admin -refresh-summaries
```

The worker also refreshes summaries after successful ingestion when the
production migration has been applied.

## Backups

Install `pg_dump` and run:

```bash
DATABASE_URL='postgres://...' BACKUP_DIR=/var/backups/spawnpoint \
  deployments/scripts/backup-postgres.sh
```

Restore test backups regularly:

```bash
createdb spawnpoint_restore
gunzip -c /var/backups/spawnpoint/spawnpoint-YYYYMMDDTHHMMSSZ.dump.gz | \
  pg_restore --dbname "$RESTORE_DATABASE_URL"
```

## Monitoring

`deployments/scripts/healthcheck-alert.sh` + the
`spawnpoint-healthcheck.service`/`.timer` pair poll `/healthz` every 2
minutes and post a Discord message (`DISCORD_WEBHOOK_URL`) after
`HEALTHCHECK_FAIL_THRESHOLD` (default 3) consecutive failures, and again when
it recovers:

```bash
cp deployments/systemd/spawnpoint-healthcheck.{service,timer} /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now spawnpoint-healthcheck.timer
```

This is an **on-box** synthetic check — it runs as its own systemd unit
independent of `spawnpoint-api`, so it still fires if that process is
wedged or down, but it shares the same host and network path, so it can't
catch problems like the whole VM being down, DNS/routing failures, or your
hosting provider having an outage. Pair it with a real off-box uptime
monitor (UptimeRobot, Better Uptime, Pingdom, etc. — pick one and point it
at `https://<your-domain>/healthz`) for that; this script is a useful first
layer, not a replacement.

## Docker

Prefer containers over bare-metal systemd units? See the
[Docker](../README.md#docker) section of the README —
`docker-compose.prod.yml` runs Postgres, a one-shot migration runner, the
api, and the worker, all built from the root `Dockerfile`. Everything below
in this doc (backups, hardening, TLS front door) still applies; only the
process-supervision layer changes.

Create your first named admin account (bare-metal `setup.sh` does this for
you automatically; a Docker-only deploy has no equivalent wizard, so run it
once by hand after the stack is up):

```bash
docker compose -f deployments/compose/docker-compose.prod.yml exec \
  -e ADMIN_CREATE_USERNAME=youradminname \
  -e ADMIN_CREATE_PASSWORD='a-strong-password' \
  api /usr/local/bin/spawnpoint-api -create-admin
```

This requires migration `0014_admin_accounts.sql` to already be applied (the
`migrate` one-shot service in `docker-compose.prod.yml` does this on stack
startup). Re-running with the same username resets that account's password —
useful for recovery, but keep the command out of shell history/CI logs since
the password is visible on the command line.

One Docker-specific caveat: the app trusts `X-Forwarded-For` /
`CF-Connecting-IP` headers only when the TCP peer is a loopback or private
address (that's how it recognizes "my own reverse proxy"). Inside a
container, traffic that arrives through Docker's *userland* proxy — most
commonly IPv6 connections to a published port — reaches the app with the
private Docker gateway as its peer, so a direct internet client could spoof
those headers and dodge the per-IP rate limiter and login throttle. If the
site is public and you're not fronting it with a real reverse proxy on the
same host or Docker network, either publish the port on IPv4 only (e.g.
`0.0.0.0:8080:8080`) or disable Docker's userland proxy
(`"userland-proxy": false` in `/etc/docker/daemon.json`) so the app always
sees real client addresses.

## Services

Build binaries:

```bash
go build -o bin/spawnpoint-api ./apps/api
go build -o bin/spawnpoint-worker ./apps/worker
go build -o bin/spawnpoint-admin ./apps/admin
```

Install the service files from `deployments/systemd/`, then reload and start:

```bash
systemctl daemon-reload
systemctl enable --now spawnpoint-api.service
systemctl enable --now spawnpoint-worker.service
```

The sample services are configured for a stricter deployment layout:

```text
/opt/spawnpoint                  application files and binaries
/etc/spawnpoint/spawnpoint.env  root-owned environment file
/var/lib/spawnpoint/raw-logs      raw archive owned by spawnpoint
/srv/spawnpoint/drop/server-a     external upload/drop folder
```

Create a dedicated service user and lock down the environment file:

```bash
useradd --system --home /var/lib/spawnpoint --shell /usr/sbin/nologin spawnpoint
install -d -o spawnpoint -g spawnpoint -m 0750 /var/lib/spawnpoint/raw-logs
install -d -o root -g spawnpoint -m 0750 /etc/spawnpoint
install -m 0640 -o root -g spawnpoint spawnpoint.env /etc/spawnpoint/spawnpoint.env
```

## API Front Door

Run the API behind Caddy or Nginx for TLS, HTTP/2, compression, and access logs.
The API itself includes request logging, conservative cache headers, DB pool
limits, and HTTP server timeouts, but TLS termination belongs at the reverse
proxy. Set `HOST=127.0.0.1` in `.env` so the app is only reachable through
whichever proxy you put in front of it, not directly.

For a full walkthrough of the recommended setup — Nginx + a Cloudflare Origin
Certificate, with the origin locked down to Cloudflare's IPs only — see
[Nginx + Cloudflare Reverse Proxy](nginx-cloudflare-reverse-proxy.md).

Example Caddy site (a lighter option if you don't need Cloudflare in front):

```caddyfile
stats.example.com {
  encode zstd gzip
  reverse_proxy 127.0.0.1:18080
}
```

## Environment

Recommended `.env`:

```text
DATABASE_URL=postgres://spawnpoint:spawnpoint@127.0.0.1:5432/spawnpoint?sslmode=disable
PORT=18080
ARCHIVE_DIR=/var/lib/spawnpoint/raw-logs
MAX_LOG_FILE_BYTES=104857600
SERVER_NAME=BF1942 Server
SERVER_ADDRESS=127.0.0.1
SERVER_PORT=14567
ADMIN_TOKEN=replace-with-long-random-token
SECURE_COOKIES=1
SESSION_KEY_FILE=/var/lib/spawnpoint/session.key
PUBLIC_BASE_URL=https://stats.example.com
PRIVACY_CONTACT=admin@example.com
DB_MAX_OPEN_CONNS=16
DB_MAX_IDLE_CONNS=8
DB_CONN_MAX_LIFETIME_SECONDS=900
DB_CONN_MAX_IDLE_SECONDS=300
```

## Import Modes

The safest production model is still file-based ingestion:

1. BF1942 writes completed XML logs to its mod `logs` directory.
2. `spawnpoint-worker -watch` polls that directory.
3. The worker validates that the XML is complete, stores the raw file archive,
   and inserts normalized events into PostgreSQL.

For servers hosted elsewhere, use a drop folder on the stats host:

```text
/srv/spawnpoint/drop/server-a
/srv/spawnpoint/drop/server-b
```

Prefer SFTP, rsync over SSH, or a VPN-mounted transfer path. Plain FTP should
only be used on a trusted private network or behind a VPN because credentials
and data are not protected in transit.

If an FTP-compatible upload path is required:

- create a dedicated upload user per game server
- chroot or jail the upload user to its own drop directory
- disable shell access for upload users
- do not reuse the database or system service account for uploads
- upload to a temporary filename, then rename to `.xml` or `.zxml` after the
  transfer completes
- run the worker as a separate `spawnpoint` user with read access to drop folders
  and write access only to `ARCHIVE_DIR`
- keep `ARCHIVE_DIR` separate from upload directories so untrusted uploads are
  never served or re-used as application files
- set `MAX_LOG_FILE_BYTES` to bound raw XML and decompressed `.zxml` size
- expect failed, skipped, and parsed imports to appear in `/status` after
  migration 0003 has been applied

Example worker for multiple drop folders:

```bash
spawnpoint-worker -watch -interval 15s \
  /srv/spawnpoint/drop/server-a \
  /srv/spawnpoint/drop/server-b
```

## Security Hardening

Use PostgreSQL for production. SQLite would be easier to operate, but this app
does analytics-heavy aggregation, materialized summaries, concurrent ingestion,
and future multi-server imports. PostgreSQL is the right default and should run
on the same host or a private network address, not exposed publicly.

Recommended hardening:

- create a dedicated Linux user, for example `spawnpoint`
- move the app out of `/root`, for example `/opt/spawnpoint`
- store `.env` as root-owned or `spawnpoint`-owned with `0600` permissions
- bind PostgreSQL to localhost unless a private DB host is required
- use a least-privilege database role for the API and worker
- put Caddy or Nginx in front of the API for TLS, compression, rate limits, and
  access logs
- once TLS is terminating in front, set `SECURE_COOKIES=1` so the admin session
  cookie is only ever sent over HTTPS (the app can't tell it's behind TLS when
  the proxy forwards plain HTTP, so this flag is how you tell it)
- do day-to-day admin work with named accounts (**Admin → Users**), not the
  shared `ADMIN_TOKEN`: accounts are individually revocable, role-scoped
  (`moderator` for helpers who only need player moderation), and every action
  lands in the audit log (**Admin → Audit**) with the actor's name. Keep
  `ADMIN_TOKEN` as a sealed-envelope break-glass credential — or unset it
  entirely once an admin account exists, at the cost of no recovery path if
  you lose the account password (re-set the token to get back in)
- point `SESSION_KEY_FILE` at a writable, persistent path (the hardened systemd
  unit uses `/var/lib/spawnpoint/session.key`); the CSRF-token signing
  secret lives there so open admin sessions survive restarts
- server ingest API keys are stored only as SHA-256 hashes, so a database dump
  or backup never exposes usable upload credentials
- firewall public access to PostgreSQL, FTP, and the BF1942 admin/RCON port
- treat uploaded XML as untrusted input; keep parser limits conservative and
  avoid following symlinks in writable drop folders
- monitor disk usage for raw archives, PostgreSQL data, and backups
- schedule `deployments/scripts/backup-postgres.sh` nightly — ready-made units
  in `deployments/systemd/spawnpoint-backup.{service,timer}`
- point uptime monitoring at `/healthz`: it returns 503 when the database is
  unreachable or no ingest job has run for `STALE_INGEST_MINUTES`
- test backup restore, not just backup creation
- set `ADMIN_TOKEN`; admin pages are disabled without it. Browsers sign in at
  `/admin/login` (session cookie); scripts send `Authorization: Bearer <token>`.
  The token is never accepted in a URL, and request logs record paths only

Good next production refinements for this codebase:

- add a dedicated import/drop-folder service template with locked-down
  permissions
- add ingestion file-size limits and symlink rejection
- add database roles split by API read access and worker write access
- add structured JSON logs and basic Prometheus-style counters
- add an admin-only refresh endpoint or CLI wrapper for summary views
- add retention controls for raw archives and old request logs
- add optional HTTP basic auth or proxy-auth guidance for private admin pages

## Updates and Versioning

Releases are tagged `vX.Y.Z` and the running version is embedded into each
binary at build time (`-ldflags "-X main.version=..."`, set by both
`setup.sh` and `update.sh`). `sudo ./update.sh` moves a checkout to the
latest tag, applies any new migrations, rebuilds, and restarts the systemd
units; `./update.sh --tag vX.Y.Z` pins or rolls back to a specific release.
See the [Updating](../README.md#updating) section of the README. Check the
running version with `spawnpoint-api -version` or on `/admin`.

## Operational Checks

- `GET /healthz` should return `{"status":"ok"}`.
- `journalctl -u spawnpoint-api -f` should show request logs.
- `journalctl -u spawnpoint-worker -f` should show ingested/skipped logs.
- Monitor PostgreSQL disk usage and backup age.
- Re-run `go test ./...` before deploying parser or schema changes.

## Performance

`setup.sh` applies all migrations in `migrations/` in order, including
map-page query indexes (migration `0006`), and tunes Postgres for small boxes
during standalone provisioning. See [performance.md](performance.md) if
`/map` pages are slow or return a `canceling statement due to user request`
error — that doc covers the indexing strategy and the Postgres settings that
matter most on a 1-2 CPU / <4GB RAM VPS.
