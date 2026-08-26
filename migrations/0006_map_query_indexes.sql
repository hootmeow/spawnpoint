-- Map page performance: the /map handler filters events by game_id (via a
-- join to games on map_name) combined with event_type/score_type/vehicle
-- predicates. Without indexes matching those combinations, Postgres falls
-- back to bitmap-scanning the *entire* events table for the map's event
-- type/score_type before joining to games, which on a busy map (hundreds of
-- thousands of matching rows) took 700ms-7s per query and could blow the
-- page's request timeout ("canceling statement due to user request").
--
-- These are narrow partial indexes keyed by game_id so the planner can do a
-- cheap nested loop from games (usually a few thousand rows for one map)
-- into events, instead of scanning the whole table. Predicates are written
-- to exactly match the application's query text (see apps/api/insight_pages.go
-- and apps/api/heatmap.go) because Postgres only uses a partial index when it
-- can prove the index predicate is implied by the query's WHERE clause.
--
-- CONCURRENTLY avoids locking out ingest writes while these build on an
-- existing, populated database.
create index concurrently if not exists idx_events_heatmap
  on events (game_id, (lower(coalesce(params->>'score_type', ''))))
  where event_type = 'score' and params ? 'player_location';

create index concurrently if not exists idx_events_kill_weapon
  on events (game_id, (lower(coalesce(params->>'score_type', ''))))
  where event_type = 'score' and vehicle_code is null and nullif(weapon_code, '') is not null;

create index concurrently if not exists idx_events_vehicle_enter_destroy
  on events (game_id) where event_type in ('enter_vehicle', 'destroy_vehicle');

create index concurrently if not exists idx_events_vehicle_score
  on events (game_id) where event_type = 'score' and vehicle_code is not null;
