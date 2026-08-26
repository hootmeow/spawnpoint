-- Precomputed site-wide kit metrics (the default, unfiltered /kits page).
-- The live query (fetchKitMetrics) joins pickup_kit events back to
-- round_player_stats per carrier with no map filter — still 3-8s even after
-- the work_mem fix (migration notes in docs/performance.md). Refreshed by
-- Store.RefreshSummaries like the other _mv views. A serverFilter still
-- runs the live query.
create materialized view if not exists kit_totals_mv as
with pickups as (
	select e.kit_code, e.round_id, e.game_id, coalesce(e.actor_player_id, actor_stats.player_id) as player_id
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	where e.event_type = 'pickup_kit' and e.kit_code is not null
),
carriers as (
	select distinct kit_code, round_id, player_id from pickups where player_id is not null
),
stats as (
	select c.kit_code, coalesce(sum(s.kills), 0)::int as kills, coalesce(sum(s.deaths), 0)::int as deaths, coalesce(sum(s.teamkills), 0)::int as teamkills
	from carriers c join round_player_stats s on s.round_id = c.round_id and s.player_id = c.player_id
	group by c.kit_code
)
select
	p.kit_code,
	count(*)::int as pickups,
	count(distinct p.player_id)::int as users,
	count(distinct p.round_id)::int as rounds,
	count(distinct coalesce(g.map_name, 'unknown'))::int as maps,
	coalesce(max(stats.kills), 0)::int as kills_by_carriers,
	coalesce(max(stats.deaths), 0)::int as deaths_by_carriers,
	coalesce(max(stats.teamkills), 0)::int as teamkills_by_carriers
from pickups p
left join games g on g.id = p.game_id
left join stats on stats.kit_code = p.kit_code
group by p.kit_code;

create unique index if not exists idx_kit_totals_mv_kit on kit_totals_mv (kit_code);
create index if not exists idx_kit_totals_mv_pickups on kit_totals_mv (pickups desc);

-- The /kits page's "top users" and "top maps" side panels (kitTopUsersQuery/
-- kitTopMapsQuery) — same pickup_kit-event aggregation, no map filter.
create materialized view if not exists kit_top_users_mv as
select p.id as player_id, p.display_name, count(*)::int as pickups, count(distinct e.round_id)::int as rounds
from events e
left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
join players p on p.id = coalesce(e.actor_player_id, actor_stats.player_id) and not p.hidden
where e.event_type = 'pickup_kit' and e.kit_code is not null
group by p.id, p.display_name;

create index if not exists idx_kit_top_users_mv_pickups on kit_top_users_mv (pickups desc);

create materialized view if not exists kit_top_maps_mv as
select coalesce(g.map_name, 'unknown') as map_name, count(*)::int as pickups
from events e join games g on g.id = e.game_id
where e.event_type = 'pickup_kit' and e.kit_code is not null
group by 1;

create index if not exists idx_kit_top_maps_mv_pickups on kit_top_maps_mv (pickups desc);
