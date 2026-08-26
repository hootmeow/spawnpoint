-- Precomputed per-player totals for /awards' default view (period=all,
-- bots=all, no server filter). The 11 aggregate-award categories
-- (fetchAggregateAward) each re-scan round_player_stats joined to
-- rounds/games/players (~270ms x 11), and the Vehicle Ace award
-- (fetchVehicleAceAward) re-scans events for vehicle kills (~2.2s) — all
-- site-wide, all on every page load. A period filter, bot filter, or server
-- filter still runs the live query.
create materialized view if not exists award_totals_mv as
select
	p.id as player_id,
	p.display_name,
	count(distinct s.round_id)::int as rounds,
	coalesce(sum(s.kills), 0)::int as kills,
	coalesce(sum(s.deaths), 0)::int as deaths,
	coalesce(sum(s.score), 0)::int as score,
	coalesce(sum(s.captures), 0)::int as captures,
	coalesce(sum(s.heals), 0)::int as heals,
	coalesce(sum(s.repairs), 0)::int as repairs,
	coalesce(sum(s.defences), 0)::int as defences,
	coalesce(sum(s.attacks), 0)::int as attacks,
	coalesce(sum(s.teamkills), 0)::int as teamkills
from round_player_stats s
join rounds r on r.id = s.round_id
join games g on g.id = r.game_id
join players p on p.id = s.player_id and not p.hidden
group by p.id, p.display_name;

create unique index if not exists idx_award_totals_mv_player on award_totals_mv (player_id);

create materialized view if not exists vehicle_ace_totals_mv as
select p.id as player_id, p.display_name, count(*)::int as value
from events e
join round_player_stats s on s.round_id = e.round_id and s.slot_id = e.actor_slot_id
join rounds r on r.id = e.round_id
join games g on g.id = r.game_id
join players p on p.id = s.player_id and not p.hidden
where e.event_type = 'score'
and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
and e.vehicle_code is not null and e.vehicle_code not like '%Soldier'
group by p.id, p.display_name;

create unique index if not exists idx_vehicle_ace_totals_mv_player on vehicle_ace_totals_mv (player_id);
