-- Precomputed site-wide vehicle activity (the default, unfiltered /vehicles
-- list). The live query aggregates events matching a 3-way OR (enter/exit/
-- destroy events, OR score events with a vehicle_code, OR score events whose
-- weapon_code is a vehicle-mounted gun per vehicle_code_list_mv) with no map
-- filter, which took 6-10s even after the vehicle_code_list_mv fix. Refreshed
-- by Store.RefreshSummaries like the other _mv views (migrations 0002, 0007).
-- A serverFilter, ?q= search, or per-player view still runs the live query.
create materialized view if not exists vehicle_activity_totals_mv as
with vehicle_codes as (
	select code from vehicle_code_list_mv
),
vehicle_events as (
	select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') as vehicle_code,
		e.raw_event_name, e.event_type, e.params, e.round_id,
		coalesce(e.actor_player_id, actor_slot_stats.player_id) as actor_id
	from events e
	left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
	where (
		e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle')
		or (e.event_type = 'score' and e.vehicle_code is not null)
		or (e.event_type = 'score' and coalesce(e.weapon_code, '') in (select code from vehicle_codes))
	)
	and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')) is not null
	and regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') not like '%Soldier'
	and lower(regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '')) not similar to '%(dummy|camera|wheel)%'
)
select
	vehicle_code,
	count(*) filter (where event_type = 'enter_vehicle')::int as entries,
	count(*) filter (where event_type = 'score' and lower(coalesce(params->>'score_type', '')) in ('kill', 'kills'))::int as kills,
	count(*) filter (where event_type = 'score' and lower(coalesce(params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int as teamkills,
	count(distinct actor_id)::int as users,
	count(distinct round_id)::int as rounds,
	count(*) filter (where raw_event_name = 'destroyVehicle' or event_type = 'destroy_vehicle')::int as destroyed,
	count(distinct actor_id) filter (where raw_event_name = 'destroyVehicle' or event_type = 'destroy_vehicle')::int as destroyers
from vehicle_events
group by vehicle_code;

create unique index if not exists idx_vehicle_activity_totals_mv_vehicle on vehicle_activity_totals_mv (vehicle_code);
create index if not exists idx_vehicle_activity_totals_mv_kills on vehicle_activity_totals_mv (kills desc);

-- Per-weapon-code detail queries (weaponPage's killers/victims/maps/summary
-- panels) filtered events by `coalesce(weapon_code,'') = $1` combined with
-- event_type/score_type, with no index matching that exact expression, so
-- Postgres fell back to a broad bitmap scan + heap recheck discarding ~85%
-- of candidate rows. This is keyed by the same coalesce() expression the
-- queries use — Postgres only uses a partial/functional index when it can
-- prove the index expression is syntactically implied by the query.
create index concurrently if not exists idx_events_weapon_score_lookup
	on events ((coalesce(weapon_code, '')), (lower(coalesce(params->>'score_type', ''))))
	where event_type = 'score' and vehicle_code is null;

-- Per-vehicle-code detail queries (vehiclePage's operators/destroyers/maps/
-- event-mix/summary panels) filter by the same
-- regexp_replace(coalesce(nullif(vehicle_code,''), nullif(weapon_code,'')),
-- '_PCO[0-9]+$', '') expression. Deliberately NOT partial (unlike the index
-- above): one of these queries filters `event_type = 'destroy_vehicle' or
-- raw_event_name = 'destroyVehicle'`, and Postgres's partial-index proof
-- can't derive from a raw_event_name condition that event_type must be in
-- some restricted set, so any partial predicate here silently stopped the
-- planner from using the index for that one query. Unpartitioned costs more
-- disk (15MB vs a few MB) but works for every current and future query
-- shape without re-deriving predicate provability each time.
create index concurrently if not exists idx_events_vehicle_code_expr
	on events ((regexp_replace(coalesce(nullif(vehicle_code, ''), nullif(weapon_code, '')), '_PCO[0-9]+$', '')));
