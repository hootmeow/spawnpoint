-- Production hardening for dashboard/detail query paths.
-- This migration is additive and safe to run on existing installs.

create extension if not exists pg_trgm;

create index if not exists idx_events_game_time on events (game_id, occurred_at desc);
create index if not exists idx_events_round_time on events (round_id, occurred_at desc);
create index if not exists idx_events_target_time on events (target_player_id, occurred_at desc);
create index if not exists idx_events_weapon_type on events (weapon_code, event_type);
create index if not exists idx_events_vehicle_type on events (vehicle_code, event_type);
create index if not exists idx_events_kit_type on events (kit_code, event_type);
create index if not exists idx_events_score_type on events ((lower(coalesce(params->>'score_type', ''))));
create index if not exists idx_round_player_stats_round_player on round_player_stats (round_id, player_id);
create index if not exists idx_round_player_stats_player_round on round_player_stats (player_id, round_id);
create index if not exists idx_round_player_stats_kills on round_player_stats (kills desc, score desc);
create index if not exists idx_games_map_started on games (map_name, started_at desc);
create index if not exists idx_rounds_ended_at on rounds (ended_at desc);
create index if not exists idx_player_aliases_alias_trgm on player_aliases using gin (alias gin_trgm_ops);
create index if not exists idx_players_display_name_trgm on players using gin (display_name gin_trgm_ops);

-- Event-derived compatibility views used by detail query code. BF1942 XML does
-- not provide reliable seat duration in every log, so duration is a conservative
-- approximation from vehicle/kit event counts until richer session tracking is
-- added.
create or replace view round_vehicle_stats as
select
  e.round_id,
  coalesce(e.actor_player_id, actor_stats.player_id) as player_id,
  regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') as vehicle_code,
  count(*) filter (where e.event_type = 'enter_vehicle')::int as entries,
  greatest(count(*) filter (where e.event_type in ('enter_vehicle', 'exit_vehicle', 'score', 'destroy_vehicle'))::int * 10, 0) as duration_seconds
from events e
left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
where coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')) is not null
and regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') not like '%Soldier'
and (
  e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle')
  or (e.event_type = 'score' and (e.vehicle_code is not null or e.weapon_code is not null))
)
group by e.round_id, coalesce(e.actor_player_id, actor_stats.player_id), regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '');

create or replace view round_kit_stats as
select
  e.round_id,
  coalesce(e.actor_player_id, actor_stats.player_id) as player_id,
  e.kit_code,
  count(*)::int as pickups,
  greatest(count(*)::int * 10, 0) as duration_seconds
from events e
left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
where e.event_type = 'pickup_kit'
and e.kit_code is not null
group by e.round_id, coalesce(e.actor_player_id, actor_stats.player_id), e.kit_code;

create materialized view if not exists player_totals_mv as
select
  p.id as player_id,
  p.display_name,
  case
    when bool_or(s.is_bot is true) then true
    when bool_or(s.is_bot is false) then false
    else null
  end as is_bot,
  count(distinct s.round_id)::int as rounds,
  coalesce(sum(s.score), 0)::int as score,
  coalesce(sum(s.kills), 0)::int as kills,
  coalesce(sum(s.deaths), 0)::int as deaths,
  coalesce(sum(s.teamkills), 0)::int as teamkills,
  coalesce(sum(s.heals), 0)::int as heals,
  coalesce(sum(s.repairs), 0)::int as repairs,
  coalesce(sum(s.captures), 0)::int as captures,
  max(coalesce(r.ended_at, g.started_at)) as last_seen_at
from players p
left join round_player_stats s on s.player_id = p.id
left join rounds r on r.id = s.round_id
left join games g on g.id = r.game_id
group by p.id, p.display_name;

create unique index if not exists idx_player_totals_mv_player on player_totals_mv (player_id);
create index if not exists idx_player_totals_mv_kills on player_totals_mv (kills desc, score desc);
create index if not exists idx_player_totals_mv_last_seen on player_totals_mv (last_seen_at desc);

create materialized view if not exists map_totals_mv as
select
  coalesce(nullif(g.map_name, ''), 'unknown') as map_name,
  count(distinct r.id)::int as rounds,
  count(distinct r.id) filter (where r.winning_team_id = 1)::int as team1_wins,
  count(distinct r.id) filter (where r.winning_team_id = 2)::int as team2_wins,
  coalesce(sum(s.kills), 0)::int as kills,
  coalesce(avg(extract(epoch from (r.ended_at - g.started_at))), 0)::int as avg_duration_seconds,
  count(distinct s.player_id)::int as players
from games g
join rounds r on r.game_id = g.id
left join round_player_stats s on s.round_id = r.id
group by 1;

create unique index if not exists idx_map_totals_mv_map on map_totals_mv (map_name);
create index if not exists idx_map_totals_mv_rounds on map_totals_mv (rounds desc);

create materialized view if not exists weapon_totals_mv as
with vehicle_codes as (
  select distinct regexp_replace(vehicle_code, '_PCO[0-9]+$', '') as code
  from events
  where vehicle_code is not null and vehicle_code not like '%Soldier'
)
select
  coalesce(e.weapon_code, 'unknown') as weapon_code,
  count(*)::int as uses,
  count(*) filter (where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills'))::int as kills,
  count(*) filter (where lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int as teamkills,
  count(distinct coalesce(e.actor_player_id::text, actor_stats.player_id::text, e.actor_slot_id::text))::int as users
from events e
left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
where e.weapon_code is not null
and e.vehicle_code is null
and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
group by 1;

create unique index if not exists idx_weapon_totals_mv_weapon on weapon_totals_mv (weapon_code);
create index if not exists idx_weapon_totals_mv_kills on weapon_totals_mv (kills desc);

create materialized view if not exists vehicle_totals_mv as
select
  vehicle_code,
  coalesce(sum(entries), 0)::int as entries,
  coalesce(sum(duration_seconds), 0)::int as duration_seconds,
  count(distinct player_id)::int as users,
  count(distinct round_id)::int as rounds
from round_vehicle_stats
where vehicle_code is not null
group by vehicle_code;

create unique index if not exists idx_vehicle_totals_mv_vehicle on vehicle_totals_mv (vehicle_code);
create index if not exists idx_vehicle_totals_mv_entries on vehicle_totals_mv (entries desc);
