# Performance: slow pages and small-VPS Postgres tuning

If any page ever returns `Database error: pq: canceling statement due to
user request`, it means the page's queries ran past their request timeout
and Postgres cancelled them — not a crash, not corrupt data. This has a
specific, known cause on this codebase and a specific fix. `/map` was the
first page found this way; `/weapons`, `/vehicles`, `/kits`, and `/awards`
turned out to have the same underlying problem (whole-table aggregation with
no map filter) and got the same treatment — see "Other whole-site pages"
below.

## Why it happens

`/map` runs about a dozen queries per request, each filtering `events` (and
sometimes `round_player_stats`) down to one map by joining through `games`.
On a busy map — tens or hundreds of thousands of matching events — a query
shaped like:

```sql
select ... from events e join games g on g.id = e.game_id
where g.map_name = $1 and e.event_type = 'score' and ...
```

can get planned as: scan the *entire* `events` table for rows matching
`event_type`/`score_type`/etc. (ignoring which map they belong to), then join
that against the small set of games for this map. On a table with millions of
events that bitmap scan alone can take several seconds — multiplied by the
number of such queries on the page, it adds up past a 10-second timeout.

The fix isn't a bigger timeout (that just hides the problem under load) or
brute-force caching (the first hit for any map/filter combo is still slow).
It's giving Postgres a cheap way to go the other direction: find this map's
games first (a few thousand rows, or fewer), then look up each game's
matching events directly.

## The indexes (migration `0006_map_query_indexes.sql`)

Four narrow **partial indexes**, each keyed by `game_id` first and scoped
with a `where` clause matching one of the page's query shapes:

- `idx_events_heatmap` — kill/death position lookups for the map heatmaps
- `idx_events_kill_weapon` — top-weapons panel
- `idx_events_vehicle_enter_destroy` / `idx_events_vehicle_score` — top-vehicles
  panel (this one has an `OR` across two `event_type` sets in the app query,
  so it's backed by two indexes and the query itself is written as a
  `UNION ALL` of two branches — Postgres won't use a partial index unless the
  query's `WHERE` clause provably implies the index's predicate, and it can't
  prove that through an `OR`)

These took the worst offender on a live ~2M-row `events` table from ~7s to
~150ms, and another from ~680ms to ~30ms. `setup.sh` applies this migration
like any other; on an existing populated database the indexes build
`CONCURRENTLY` so ingest writes aren't blocked while they build.

If you add a new map-filtered query, match its `WHERE` clause to a partial
index the same way, or it'll silently fall back to a full scan — verify with
`EXPLAIN ANALYZE` for a busy map (`select map_name, count(*) from games group
by 1 order by 2 desc limit 5;` to find one).

## Precomputed summaries (migration `0007_summary_caches.sql`)

Indexing gets `/map`'s live queries from multi-second to low-hundreds-of-ms,
but a handful of queries are inherently whole-table — no per-map index helps
a homepage stat that's deliberately aggregated across *every* map, or a
`vehicle_code` distinct-list subquery that has to look at every vehicle
event ever logged. Those got materialized instead, following the same
pattern this codebase already used for `player_totals_mv` / `map_totals_mv`
/ `weapon_totals_mv` / `vehicle_totals_mv` (migration `0002`):

- `vehicle_kill_totals_mv` — homepage "top vehicles" bar (was ~2-3.5s live,
  is now a ~3ms indexed read)
- `site_totals_mv` — homepage stat strip: rounds/players/kills/events/maps
  counted with no filter at all (was ~800ms, now ~10ms)
- `map_summary_mv` — the default (all bots, all time, all servers) `/map`
  view's vehicle panel, event-type mix, and a 5,000-point sample of kill/
  death world positions for the heatmaps, one row per map (was 2-4s combined
  across those panels, now a single indexed row lookup)

All three are refreshed by `Store.RefreshSummaries` (`internal/storage/
postgres/store.go`), which already runs after every successful ingest —
`map_summary_mv` refreshes `CONCURRENTLY` (needs the unique index the
migration creates) since it re-scans `events` and would otherwise take an
exclusive lock for several seconds. A full refresh of all seven views
currently takes ~14s on this 1-CPU box; that's ingest-triggered background
work, not something a page request waits on.

**These only cover the default, unfiltered view.** A `bots=`/`window=`
filter, or a `server=` filter on a multi-server install, always falls back
to the live migration-0006-indexed query — still fast, just not
precomputed. If you filter a map and it's slower than the default view,
that's why, not a regression.

The application code checks `does not exist` on these queries and falls
back to the live path, so it degrades gracefully on an install that hasn't
applied migration 0007 yet.

## Migrations 0008-0011: the rest of the site

Once `/map` and the homepage were fast, the same "canceling statement" error
turned out to still be live on `/weapons`, `/vehicles`, `/kits`, `/awards`,
and `/player` — all site-wide pages with no map to index by. Same pattern,
one migration per page:

- `0008_vehicle_activity_totals.sql` — `vehicle_activity_totals_mv` (the
  default `/vehicles` list, 6-10s → tens of ms) plus
  `idx_events_weapon_score_lookup` / `idx_events_vehicle_code_expr`, two
  functional indexes for the per-code detail pages (`/weapon?code=`,
  `/vehicle?code=`). The vehicle one is deliberately **not** partial — see
  the comment in the migration file for why a partial predicate silently
  stopped working for one of the five queries that share it.
- `0009_kit_totals.sql` — `kit_totals_mv` / `kit_top_users_mv` /
  `kit_top_maps_mv` for `/kits` (3-8s → ~30ms).
- `0010_award_totals.sql` — `award_totals_mv` / `vehicle_ace_totals_mv` for
  `/awards`' default (period=all, bots=all) view (5-8s → ~1s; the remaining
  time is `fetchRoundRecord`, already fast, plus template rendering).
- `0011_player_breakdown.sql` — `player_breakdown_mv` (weapons/vehicles/kit
  pickups/vehicle pickups/victims/nemeses, one row per player, jsonb panels
  same as `map_summary_mv`) and `player_first_bloods_mv`, for `/player`
  (was hitting its 15s timeout for active players — 536+ rounds — now
  consistently sub-second). `fetchPlayerKillFeed` and `fetchPlayerStreak`
  (kill/death streaks) are still live queries — they're either inherently
  paginated/live (kill feed) or would need per-player window-function
  partitioning to materialize (streaks); if `/player` needs to be faster
  still, those are the next targets.

All of these follow the same rule as 0007: verify the materialized query
against the live one on real data before wiring it in (a `vehicle_totals_mv`
existed already from migration 0002, and it looked like a shortcut for one
of these — turned out to have completely different semantics, built from
`round_vehicle_stats` rather than raw events. Diffing the two result sets
caught it before it shipped a data bug).

## The worker's seen-cache (persisted, `-state-file`)

Separately from query performance: every worker restart used to re-verify
its entire log backlog against the database (the in-memory "have I already
processed this file" cache didn't survive a restart), writing a burst of
thousands of spurious `ingest_jobs` rows and spiking CPU/load for a minute
or two — see the `ingest_jobs` pruning note in `internal/storage/postgres/
store.go`'s `RefreshSummaries`. The cache is now persisted to a JSON file
(`-state-file`, defaults to a `worker-seen-cache.json` next to
`-archive-dir`) and reloaded on startup, so only genuinely new or changed
files get re-touched after a restart.
applied migration 0007 yet.

## Other whole-site pages (`/weapons`, `/vehicles`, `/kits`, `/awards`)

Unlike `/map`, these pages are deliberately site-wide — "top vehicles across
every map ever played" has no map to filter by, so migration 0006's
game_id-keyed indexes don't apply. Two fixes:

1. **`vehicle_code_list_mv`** (migration 0007): the query "which
   `weapon_code`s are actually vehicle-mounted guns, not handheld weapons"
   (`select distinct regexp_replace(vehicle_code, ...) from events where
   ...`) was copy-pasted into 16 call sites across `apps/api` (plus
   `weapon_totals_mv`'s own definition), every one re-scanning the whole
   `events` table for the same ~100-row answer. All 16 now read this
   materialized view instead — safe because it's mechanically identical to
   what each call site computed inline, just cached. If you add a 17th call
   site that needs this list, use `vehicle_code_list_mv`, not a fresh CTE.
2. **Wider timeouts.** `/weapons`, `/vehicles`, `/kits`, `/awards` got
   15-20s budgets (up from 5-8s) instead of `/map`'s treatment of full
   materialization + narrow indexes. These pages aggregate across the whole
   `events`/`round_player_stats` tables with complex per-player/search
   filtering (`?q=`, per-player drill-down) that doesn't reduce cleanly to a
   handful of indexed lookups the way `/map`'s per-game_id queries did, and
   a full materialized-view treatment for all of them was judged not worth
   the added complexity in one pass. `/vehicles` in particular can still
   take 8-10s on this codebase's live dataset (~2M events) — slow, but no
   longer erroring. If these need to be faster, the `map_summary_mv`
   pattern (materialize the default, unfiltered view; keep filtered/search
   variants live) is the template to follow.

## Postgres config on small boxes

Separately from indexing, Postgres's planner defaults assume a beefy,
spinning-disk server. Most small VPS/cloud boxes are neither:

- **`random_page_cost`** defaults to `4` (spinning disk math). Cloud block
  storage is effectively SSD-backed even when the kernel reports
  `rotational = 1` for the virtual device, so the real cost of a random index
  read is much closer to a sequential one. Leaving this at `4` makes the
  planner systematically undervalue index scans and reach for sequential/
  bitmap scans instead — exactly the plan shape that causes the slow queries
  above. Set it to `1.1`.
- **`effective_cache_size`** defaults to `4GB`, which on a 2GB box wildly
  overstates how much of the table Postgres can assume is cached. Set it to
  roughly 75% of actual RAM.
- **`jit`** (on by default) compiles the query plan before running it. That
  compile step itself costs CPU — worthwhile for a long-running analytical
  query, a bad trade for the many small sub-second interactive queries this
  app runs, especially on a 1-2 core box where the compile competes with
  everything else for the one CPU. Turn it off (`jit = off`) on boxes with
  fewer than ~4 cores or 4GB RAM.
- **`work_mem`** defaults to `4MB` — too small for this app's `GROUP BY`/
  sort-heavy analytics queries (`/kits`, `/vehicles`, `/weapons`, `/awards`
  all sort well over 100k rows). Below that threshold a sort or hash spills
  to disk (`EXPLAIN ANALYZE` shows `Sort Method: external sort  Disk:
  ...kB`), which is dramatically slower than an in-memory sort — one query on
  this codebase went from 9.5s to 2.9s from this setting alone. Set it to
  `32MB`. This is per-sort-operation, not per-connection, so a complex query
  can use several multiples of it; 32MB against `DB_MAX_OPEN_CONNS=16` and
  ~2GB RAM is a reasonable balance, not a number to raise casually on a small
  box.
- **`max_connections`** defaults to `100`, far above what this app ever
  opens (`DB_MAX_OPEN_CONNS=16` for the API pool, plus a couple for the
  worker/admin). Each potential connection slot reserves shared-memory
  bookkeeping whether used or not; on a live install this app's own Postgres
  processes were using ~528MB RSS at idle. Set it to `40` (16 + headroom for
  the worker, admin CLI, and manual `psql` sessions). Unlike the other three
  settings this needs a full restart, not just a reload — safe during
  initial setup since it runs before any app service connects, but plan a
  brief restart window if you change it on a running install.

`setup.sh` detects RAM under 4GB during **standalone** provisioning (where it
has local postgres superuser access via the `postgres` system user) and
applies these automatically with `ALTER SYSTEM` (+ a restart for
`max_connections`, `pg_reload_conf()` for the rest) — no
systemd unit name or config file path required, so it works the same whether
Postgres was installed via apt, dnf/yum, or was already there.

If you're pointing setup.sh at an **existing** database you don't manage
directly (managed Postgres, a DBA-run server, etc.), apply the same three
settings yourself if the box is small:

```sql
alter system set random_page_cost = 1.1;
alter system set effective_cache_size = '<~75% of RAM>MB';
alter system set jit = off;
select pg_reload_conf();
```

## Verifying after a deploy

```bash
psql "$DATABASE_URL" -c "\di+ idx_events_heatmap idx_events_kill_weapon idx_events_vehicle_enter_destroy idx_events_vehicle_score"
psql "$DATABASE_URL" -c "\dm+ vehicle_kill_totals_mv site_totals_mv map_summary_mv"
psql "$DATABASE_URL" -Atc "show random_page_cost; show effective_cache_size; show jit; show work_mem; show max_connections;"
```

Then hit `/map?name=<your busiest map>` (find it with the query above) a few
times and confirm it loads well under the page's timeout.
