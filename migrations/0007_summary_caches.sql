-- More precomputed summaries, same pattern as migration 0002's
-- player_totals_mv/map_totals_mv/weapon_totals_mv/vehicle_totals_mv:
-- these back the homepage dashboard and the default (unfiltered) /map view,
-- both of which were re-running full-table aggregations over `events`
-- (millions of rows) on every request and regularly blowing their query
-- timeout ("canceling statement due to user request"). All four are
-- refreshed by Store.RefreshSummaries, which already runs after every
-- successful ingest.

-- The "which weapon_codes are actually vehicle-mounted guns, not handheld
-- weapons" distinct list. This exact subquery (`select distinct
-- regexp_replace(vehicle_code, ...) from events where ...`) was duplicated
-- across 16 call sites in apps/api (weapon/vehicle stat pages, the homepage
-- dashboard bars, per-player breakdowns) and in weapon_totals_mv below —
-- every one of them re-scanning the whole events table for the same ~100-row
-- answer. All 16 Go call sites now read `vehicle_code_list_mv` instead.
create materialized view if not exists vehicle_code_list_mv as
select distinct regexp_replace(vehicle_code, '_PCO[0-9]+$', '') as code
from events
where vehicle_code is not null and vehicle_code not like '%Soldier';

create unique index if not exists idx_vehicle_code_list_mv_code on vehicle_code_list_mv (code);

-- Site-wide vehicle kill counts (the homepage "top vehicles" bar). Mirrors
-- weapon_totals_mv's vehicle_codes exclusion logic exactly.
create materialized view if not exists vehicle_kill_totals_mv as
select
	regexp_replace(coalesce(nullif(e.vehicle_code, ''), e.weapon_code), '_PCO[0-9]+$', '') as vehicle_code,
	count(*)::int as kills
from events e
where e.event_type = 'score'
and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
and (e.vehicle_code is not null or e.weapon_code in (select code from vehicle_code_list_mv))
and coalesce(nullif(e.vehicle_code, ''), e.weapon_code, '') not like '%Soldier'
group by 1;

create unique index if not exists idx_vehicle_kill_totals_mv_vehicle on vehicle_kill_totals_mv (vehicle_code);
create index if not exists idx_vehicle_kill_totals_mv_kills on vehicle_kill_totals_mv (kills desc);

-- Single-row site totals (the homepage stat strip): rounds/players/kills/
-- events/maps counted across the whole database. Only valid for the
-- all-servers view; a serverFilter still runs the live query.
create materialized view if not exists site_totals_mv as
select
	(select count(*) from rounds)::int as rounds,
	(select count(*) from players)::int as players,
	(select coalesce(sum(kills), 0) from round_player_stats)::int as kills,
	(select count(*) from events)::int as events,
	(select count(distinct map_name) from games where map_name is not null and map_name <> '')::int as maps;

-- Per-map summary for the default (all bots, all time, all servers) /map
-- view: vehicle activity, event-type mix, and sampled kill/death world
-- positions for the heatmaps. A serverFilter or a bots/window filter still
-- runs the live (already indexed, see migration 0006) query.
create materialized view if not exists map_summary_mv as
with maps as (
	select distinct coalesce(nullif(map_name, ''), 'unknown') as map_name from games
),
vehicle_raw as (
	select coalesce(nullif(g.map_name, ''), 'unknown') as map_name,
		regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code
	from events e join games g on g.id = e.game_id
	where e.event_type in ('enter_vehicle', 'destroy_vehicle')
	and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
	and lower(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), '')) not similar to '%(dummy|camera|wheel)%'
	union all
	select coalesce(nullif(g.map_name, ''), 'unknown') as map_name,
		regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code
	from events e join games g on g.id = e.game_id
	where e.event_type = 'score' and e.vehicle_code is not null
	and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
	and lower(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), '')) not similar to '%(dummy|camera|wheel)%'
),
vehicle_ranked as (
	select map_name, code, count(*)::int as cnt,
		row_number() over (partition by map_name order by count(*) desc) as rn
	from vehicle_raw group by map_name, code
),
vehicle_agg as (
	select map_name, jsonb_agg(jsonb_build_object('name', code, 'count', cnt) order by cnt desc) as data
	from vehicle_ranked where rn <= 15 group by map_name
),
event_ranked as (
	select coalesce(nullif(g.map_name, ''), 'unknown') as map_name, e.event_type, count(*)::int as cnt,
		row_number() over (partition by g.map_name order by count(*) desc, e.event_type asc) as rn
	from events e join games g on g.id = e.game_id
	group by g.map_name, e.event_type
),
event_agg as (
	select map_name, jsonb_agg(jsonb_build_object('name', event_type, 'count', cnt) order by cnt desc, event_type asc) as data
	from event_ranked where rn <= 12 group by map_name
),
kill_points_raw as (
	select coalesce(nullif(g.map_name, ''), 'unknown') as map_name, e.params->>'player_location' as loc,
		row_number() over (partition by g.map_name order by e.id) as rn
	from events e join games g on g.id = e.game_id
	where e.event_type = 'score' and e.params ? 'player_location'
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
),
kill_points_agg as (
	select map_name, jsonb_agg(loc) as data from kill_points_raw where rn <= 5000 group by map_name
),
death_points_raw as (
	select coalesce(nullif(g.map_name, ''), 'unknown') as map_name, e.params->>'player_location' as loc,
		row_number() over (partition by g.map_name order by e.id) as rn
	from events e join games g on g.id = e.game_id
	where e.event_type = 'score' and e.params ? 'player_location'
	and lower(coalesce(e.params->>'score_type', '')) in ('death', 'deathnomsg')
),
death_points_agg as (
	select map_name, jsonb_agg(loc) as data from death_points_raw where rn <= 5000 group by map_name
)
select
	m.map_name,
	coalesce(v.data, '[]'::jsonb) as vehicles,
	coalesce(ev.data, '[]'::jsonb) as events,
	coalesce(kp.data, '[]'::jsonb) as kill_points,
	coalesce(dp.data, '[]'::jsonb) as death_points
from maps m
left join vehicle_agg v on v.map_name = m.map_name
left join event_agg ev on ev.map_name = m.map_name
left join kill_points_agg kp on kp.map_name = m.map_name
left join death_points_agg dp on dp.map_name = m.map_name;

create unique index if not exists idx_map_summary_mv_map on map_summary_mv (map_name);
