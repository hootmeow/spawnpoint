-- Precomputed per-player breakdown for the /player detail page: weapon/
-- vehicle kill+death tallies, kit/vehicle usage counts, and victim/nemesis
-- pairs. The live queries (fetchWeaponStats, fetchPlayerWeaponBreakdown,
-- fetchPlayerVehicleBreakdown, fetchPlayerCodeCounts x2, fetchKillPairs x2)
-- are ~7 separate site-wide-shaped scans per page load — combined they were
-- the dominant cost pushing /player past its timeout for active players
-- (~536 rounds). Only 325 players total, so one row per player with jsonb
-- panels is cheap to keep fresh. Refreshed by Store.RefreshSummaries.
--
-- "kills" in the weapons/vehicles panels is kill+teamkill combined —
-- verified against both live call sites (the bar chart sums Kills+
-- Teamkills; fetchPlayerWeaponBreakdown's own "kills" column already counts
-- kill-or-teamkill events with no separate teamkill field), so there's
-- nothing lost by not tracking them separately here.
create materialized view if not exists player_breakdown_mv as
with vehicle_codes as (
	select code from vehicle_code_list_mv
),
weapon_actor as (
	select coalesce(e.actor_player_id, actor_stats.player_id) as player_id, e.weapon_code as code,
		count(*)::int as kills
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	where e.event_type = 'score' and e.vehicle_code is null and nullif(e.weapon_code, '') is not null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.actor_player_id, actor_stats.player_id) is not null
	group by 1, 2
),
weapon_target as (
	select coalesce(e.target_player_id, target_stats.player_id) as player_id, e.weapon_code as code,
		count(*)::int as deaths
	from events e
	left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
	where e.event_type = 'score' and e.vehicle_code is null and nullif(e.weapon_code, '') is not null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.target_player_id, target_stats.player_id) is not null
	group by 1, 2
),
weapon_merged as (
	select coalesce(a.player_id, t.player_id) as player_id, coalesce(a.code, t.code) as code,
		coalesce(a.kills, 0) as kills, coalesce(t.deaths, 0) as deaths
	from weapon_actor a full outer join weapon_target t on a.player_id = t.player_id and a.code = t.code
),
weapons_agg as (
	select player_id, jsonb_agg(jsonb_build_object('code', code, 'kills', kills, 'deaths', deaths) order by kills desc, deaths desc, code asc) as data
	from weapon_merged group by player_id
),
vehicle_actor as (
	select coalesce(e.actor_player_id, actor_stats.player_id) as player_id,
		regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code,
		count(*)::int as kills
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	where e.event_type = 'score' and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.actor_player_id, actor_stats.player_id) is not null
	group by 1, 2
),
vehicle_target as (
	select coalesce(e.target_player_id, target_stats.player_id) as player_id,
		regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code,
		count(*)::int as deaths
	from events e
	left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
	where e.event_type = 'score' and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.target_player_id, target_stats.player_id) is not null
	group by 1, 2
),
vehicle_merged as (
	select coalesce(a.player_id, t.player_id) as player_id, coalesce(a.code, t.code) as code,
		coalesce(a.kills, 0) as kills, coalesce(t.deaths, 0) as deaths
	from vehicle_actor a full outer join vehicle_target t on a.player_id = t.player_id and a.code = t.code
),
vehicles_agg as (
	select player_id, jsonb_agg(jsonb_build_object('code', code, 'kills', kills, 'deaths', deaths) order by kills desc, deaths desc, code asc) as data
	from vehicle_merged group by player_id
),
kit_pickups as (
	select self_stats.player_id, e.kit_code as code, count(*)::int as cnt
	from events e
	join round_player_stats self_stats on self_stats.round_id = e.round_id and self_stats.slot_id = e.actor_slot_id
	where e.event_type = 'pickup_kit' and e.kit_code is not null and e.kit_code not like '%Soldier'
	group by 1, 2
),
kit_pickups_agg as (
	select player_id, jsonb_agg(jsonb_build_object('code', code, 'count', cnt) order by cnt desc, code asc) as data
	from kit_pickups group by player_id
),
vehicle_pickups as (
	select self_stats.player_id, e.vehicle_code as code, count(*)::int as cnt
	from events e
	join round_player_stats self_stats on self_stats.round_id = e.round_id and self_stats.slot_id = e.actor_slot_id
	where e.event_type = 'enter_vehicle' and e.vehicle_code is not null and e.vehicle_code not like '%Soldier'
	group by 1, 2
),
vehicle_pickups_agg as (
	select player_id, jsonb_agg(jsonb_build_object('code', code, 'count', cnt) order by cnt desc, code asc) as data
	from vehicle_pickups group by player_id
),
victims as (
	select self_stats.player_id as player_id, p.id as other_id, p.display_name as other_name, count(*)::int as cnt
	from events e
	join round_player_stats self_stats on self_stats.round_id = e.round_id and self_stats.slot_id = e.actor_slot_id
	join round_player_stats other_stats on other_stats.round_id = e.round_id and other_stats.slot_id = e.target_slot_id
	join players p on p.id = other_stats.player_id
	where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'tk')
	group by 1, 2, 3
),
victims_agg as (
	select player_id, jsonb_agg(jsonb_build_object('id', other_id, 'name', other_name, 'count', cnt) order by cnt desc, other_name asc) as data
	from victims group by player_id
),
nemeses as (
	select self_stats.player_id as player_id, p.id as other_id, p.display_name as other_name, count(*)::int as cnt
	from events e
	join round_player_stats self_stats on self_stats.round_id = e.round_id and self_stats.slot_id = e.target_slot_id
	join round_player_stats other_stats on other_stats.round_id = e.round_id and other_stats.slot_id = e.actor_slot_id
	join players p on p.id = other_stats.player_id
	where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'tk')
	group by 1, 2, 3
),
nemeses_agg as (
	select player_id, jsonb_agg(jsonb_build_object('id', other_id, 'name', other_name, 'count', cnt) order by cnt desc, other_name asc) as data
	from nemeses group by player_id
)
select
	p.id as player_id,
	coalesce(w.data, '[]'::jsonb) as weapons,
	coalesce(v.data, '[]'::jsonb) as vehicles,
	coalesce(kp.data, '[]'::jsonb) as kit_pickups,
	coalesce(vp.data, '[]'::jsonb) as vehicle_pickups,
	coalesce(vi.data, '[]'::jsonb) as victims,
	coalesce(ne.data, '[]'::jsonb) as nemeses
from players p
left join weapons_agg w on w.player_id = p.id
left join vehicles_agg v on v.player_id = p.id
left join kit_pickups_agg kp on kp.player_id = p.id
left join vehicle_pickups_agg vp on vp.player_id = p.id
left join victims_agg vi on vi.player_id = p.id
left join nemeses_agg ne on ne.player_id = p.id;

create unique index if not exists idx_player_breakdown_mv_player on player_breakdown_mv (player_id);

-- fetchPlayerFirstBloods scanned every score event site-wide (DISTINCT ON
-- round_id to find each round's first kill) and only THEN filtered to one
-- player — the same "compute once per site, not per request" fix as
-- vehicle_code_list_mv. This is the identical computation for every player;
-- materializing it as one count per player_id avoids the whole-table scan
-- on every /player page load (~1.1s).
create materialized view if not exists player_first_bloods_mv as
with first_kills as (
	select distinct on (e.round_id) e.round_id, coalesce(e.actor_player_id, actor_stats.player_id) as actor_id
	from events e
	left join games g on g.id = e.game_id
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	where e.round_id is not null and e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	order by e.round_id, coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval), e.sequence_no
)
select actor_id as player_id, count(*)::int as first_bloods
from first_kills
where actor_id is not null
group by actor_id;

create unique index if not exists idx_player_first_bloods_mv_player on player_first_bloods_mv (player_id);
