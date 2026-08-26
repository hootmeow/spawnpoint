# Changelog

All notable changes to this project are documented here. Releases are tagged
`vX.Y.Z`; `update.sh` moves an install between tags and shows the commits in
between.

## Unreleased

### Admin accounts, roles, and audit log

- **Named admin accounts** (migration `0014_admin_accounts.sql`): create users
  under **Admin → Users** with a username, bcrypt-hashed password, and a role —
  `admin` (everything) or `moderator` (dashboards, player moderation, and
  identity tools; no server registry, ingest keys, user management, or audit
  log). `ADMIN_TOKEN` remains as the break-glass credential and the bootstrap
  path: sign in with it once, create your account, keep the token somewhere
  safe (or unset it once an admin account exists).
- **Server-side sessions**: the admin cookie now references a database-backed
  session (only its SHA-256 is stored), so individual sessions can be revoked,
  disabling a user signs them out immediately, and "Sign Out All" per user
  works. Password resets revoke that user's sessions. Existing signed-cookie
  sessions from older versions are invalid after upgrading — sign in again.
- **Audit log**: every admin action (logins, server/key changes, player
  moderation, identity merges, user management) is recorded with actor, IP,
  and detail, and browsable under **Admin → Audit**.

### Privacy hardening

- The public event APIs and the round event feed no longer expose the
  per-CD-key `playerKeyHash` inside raw event params (it now only exists in
  the database and the archived raw logs).
- "Hide from public pages" is now enforced on every public surface keyed by
  player id or display name: `/player?id=`, `/player/export`, `/rivalry`,
  `/compare?type=player`, the player-events/activity/weapon-events/
  weapon-stats/vehicle-stats APIs, round narrative widgets (first blood,
  streaks, swing moments), per-weapon/per-vehicle leaderboards, kit
  carriers, kill-pair widgets, and the `/chat` log all exclude hidden
  players. Known residual: a hidden player's nickname can still appear
  inside *other* players' round rosters and round-history feeds, by design
  ("round history pages keep the record").
- New **Admin → Settings** page (migration `0015_site_settings.sql`) lets an
  admin hide the public `/chat` log site-wide without deleting anything.
- Fixed an open-redirect in the admin login's `next=` parameter: control
  characters in the URL could smuggle a `//evil.com`-style redirect past the
  existing checks.
- The admin login form now carries its own CSRF token (a double-submit
  cookie, since there's no authenticated session yet to key the existing
  per-session CSRF check off of), closing a login-CSRF gap where a
  cross-site form could have submitted credentials on a victim's behalf.
- `X-Forwarded-Proto` is no longer trusted for deciding whether a request is
  HTTPS (and thus whether to mark cookies `Secure`) unless `SECURE_COOKIES`
  is set — previously any client could spoof this header on a plaintext
  connection.

### Stats & exports

- New **`/streaks`** page: a server-wide leaderboard of each player's longest
  ever run of consecutive kills, not just the best streak within a single
  round.
- Weapon and vehicle pages (`/weapon`, `/vehicle`) now show a monthly usage
  trend chart alongside the existing lifetime totals, making meta shifts
  after a mod or config change visible over time.
- The rivalry page (`/rivalry`) now trends kills between the two players by
  month instead of only showing the lifetime head-to-head total.
- New **`/export/rounds`** endpoint: a bulk CSV/JSON export of round-level
  results, filterable by clan tag, server, and a `since` date, mirroring the
  existing per-player export. Capped at 50,000 rows per request and excludes
  hidden players like every other export.

### Other

- `setup.sh` self-heals the two most common causes of a "service can't reach
  its own binary/log directory" failure (a restrictive umask on freshly built
  files, a manually created log directory) by `chmod`-ing them before falling
  back to anything less secure; falling back to running services as root now
  requires an explicit confirmation instead of a silent warning, and any
  warnings from the run are recapped next to the final summary instead of
  scrolling out of view.
- `setup.sh` now accepts an optional instance name when installing systemd
  services, so a second install on the same box (e.g. a public demo alongside
  production) gets its own unit names instead of overwriting the first
  install's. Leave it blank and nothing changes.
- Tightened the sandboxing on setup.sh-installed systemd services to match
  `remote-worker-setup.sh`'s: read-only root filesystem (`ProtectSystem=strict`)
  with explicit write access only to the data/archive directories, read-only
  access to configured log directories, dropped capabilities, and several
  other systemd hardening options.
- Redesigned `/admin/login` with a proper focal layout, mark icon, and
  labeled input fields instead of the previous bare form.
- Run setup.sh-installed systemd services as a dedicated `spawnpoint` user with
  basic sandboxing (falls back to root with a warning when the checkout isn't
  reachable by the service user, e.g. under `/root`).
- Re-running `setup.sh` now preserves hand-added `.env` settings (`HOST`,
  `SECURE_COOKIES`, `PUBLIC_BASE_URL`, `DISCORD_WEBHOOK_URL`, …) instead of
  rewriting the file from scratch, and writes commented placeholders for the
  going-live options.
- `update.sh` no longer aborts on installs without a worker unit, and always
  shows the commit log between the current and target tags.
- `/live` caches GameSpy query results for 10 seconds so page views (and
  crawlers) don't fire a UDP query at every registered game server per hit.
- The page cache no longer stores responses larger than 4 MB truncated; they
  are now served uncached instead.
- Go module renamed to `github.com/hootmeow/spawnpoint` to match the
  repository; removed dead `internal/config` and `internal/ingest` packages.
- Default server display name is now "BF1942 Server".

## v1.0.0

Initial public release: XML event-log ingestion (local watch, drop directory,
or remote upload), player identity across nicknames, round outcomes and
medals, the full server-rendered web UI (leaderboards, awards, heatmaps,
clans, live status, trends, compare/rivalry), JSON API, admin area, Discord
notifications, and the setup.sh / update.sh / Docker Compose deployment paths.
