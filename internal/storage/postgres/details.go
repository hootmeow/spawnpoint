package postgres

import (
	"context"
	"database/sql"
	"time"
)

type PlayerRank struct {
	PlayerID    string `json:"player_id"`
	DisplayName string `json:"display_name"`
	Count       int    `json:"count"`
	Seconds     int    `json:"seconds,omitempty"`
}

type MapRank struct {
	MapName string `json:"map_name"`
	Count   int    `json:"count"`
	Seconds int    `json:"seconds,omitempty"`
}

type WeaponDetail struct {
	Code       string       `json:"code"`
	Kills      int          `json:"kills"`
	Teamkills  int          `json:"teamkills"`
	Users      int          `json:"users"`
	Rounds     int          `json:"rounds"`
	FirstUsed  *time.Time   `json:"first_used,omitempty"`
	LastUsed   *time.Time   `json:"last_used,omitempty"`
	TopKillers []PlayerRank `json:"top_killers"`
	TopVictims []PlayerRank `json:"top_victims"`
	KillsByMap []MapRank    `json:"kills_by_map"`
}

type VehicleDetail struct {
	Code         string       `json:"code"`
	Entries      int          `json:"entries"`
	Duration     int          `json:"duration_seconds"`
	Users        int          `json:"users"`
	Rounds       int          `json:"rounds"`
	Kills        int          `json:"kills"`
	Teamkills    int          `json:"teamkills"`
	TopOperators []PlayerRank `json:"top_operators"`
	UsageByMap   []MapRank    `json:"usage_by_map"`
}

type KitDetail struct {
	Code     string       `json:"code"`
	Pickups  int          `json:"pickups"`
	Duration int          `json:"duration_seconds"`
	Users    int          `json:"users"`
	TopUsers []PlayerRank `json:"top_users"`
}

type MapDetail struct {
	Name           string       `json:"name"`
	Rounds         int          `json:"rounds"`
	Team1Wins      int          `json:"team1_wins"`
	Team2Wins      int          `json:"team2_wins"`
	TotalKills     int          `json:"total_kills"`
	AvgDurationSec int          `json:"avg_duration_seconds"`
	TopPlayers     []PlayerRank `json:"top_players"`
	TopWeapons     []MapRank    `json:"top_weapons"`
	TopVehicles    []MapRank    `json:"top_vehicles"`
}

func (s *Store) WeaponDetail(ctx context.Context, code string) (WeaponDetail, error) {
	var detail WeaponDetail
	detail.Code = code
	err := s.db.QueryRowContext(ctx, `
		select
			count(*) filter (where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills'))::int,
			count(*) filter (where lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int,
			count(distinct coalesce(e.actor_player_id::text, actor_stats.player_id::text, e.actor_slot_id::text))::int,
			count(distinct e.round_id)::int,
			min(coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval)),
			max(coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval))
		from events e
		left join games g on g.id = e.game_id
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		where e.event_type = 'score'
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and coalesce(e.weapon_code, '') = $1
	`, code).Scan(&detail.Kills, &detail.Teamkills, &detail.Users, &detail.Rounds, nullableTimeReceiver(&detail.FirstUsed), nullableTimeReceiver(&detail.LastUsed))
	if err != nil {
		return detail, err
	}

	detail.TopKillers, err = s.playerRanks(ctx, `
		select p.id::text, p.display_name, count(*)::int, 0::int
		from events e
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		join players p on p.id = coalesce(e.actor_player_id, actor_stats.player_id)
		where e.event_type = 'score'
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and coalesce(e.weapon_code, '') = $1
		group by p.id, p.display_name
		order by count(*) desc, p.display_name asc
		limit 15
	`, code)
	if err != nil {
		return detail, err
	}
	detail.TopVictims, err = s.playerRanks(ctx, `
		select p.id::text, p.display_name, count(*)::int, 0::int
		from events e
		left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
		join players p on p.id = coalesce(e.target_player_id, target_stats.player_id)
		where e.event_type = 'score'
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and coalesce(e.weapon_code, '') = $1
		group by p.id, p.display_name
		order by count(*) desc, p.display_name asc
		limit 10
	`, code)
	if err != nil {
		return detail, err
	}
	detail.KillsByMap, err = s.mapRanks(ctx, `
		select coalesce(nullif(g.map_name, ''), 'unknown'), count(*)::int, 0::int
		from events e
		left join games g on g.id = e.game_id
		where e.event_type = 'score'
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and coalesce(e.weapon_code, '') = $1
		group by 1
		order by count(*) desc, 1 asc
	`, code)
	return detail, err
}

func (s *Store) VehicleDetail(ctx context.Context, code string) (VehicleDetail, error) {
	var detail VehicleDetail
	detail.Code = code
	err := s.db.QueryRowContext(ctx, `
		select
			coalesce(sum(entries), 0)::int,
			coalesce(sum(duration_seconds), 0)::int,
			count(distinct player_id)::int,
			count(distinct round_id)::int
		from round_vehicle_stats
		where vehicle_code = $1
	`, code).Scan(&detail.Entries, &detail.Duration, &detail.Users, &detail.Rounds)
	if err != nil {
		return detail, err
	}
	err = s.db.QueryRowContext(ctx, `
		select
			count(*) filter (where lower(coalesce(params->>'score_type', '')) in ('kill', 'kills'))::int,
			count(*) filter (where lower(coalesce(params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int
		from events
		where event_type = 'score'
		and lower(coalesce(params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and coalesce(weapon_code, '') = $1
	`, code).Scan(&detail.Kills, &detail.Teamkills)
	if err != nil {
		return detail, err
	}
	detail.TopOperators, err = s.playerRanks(ctx, `
		select p.id::text, p.display_name, coalesce(sum(v.entries), 0)::int, coalesce(sum(v.duration_seconds), 0)::int
		from round_vehicle_stats v
		join players p on p.id = v.player_id
		where v.vehicle_code = $1
		group by p.id, p.display_name
		order by coalesce(sum(v.duration_seconds), 0) desc, coalesce(sum(v.entries), 0) desc, p.display_name asc
		limit 15
	`, code)
	if err != nil {
		return detail, err
	}
	detail.UsageByMap, err = s.mapRanks(ctx, `
		select coalesce(nullif(g.map_name, ''), 'unknown'), coalesce(sum(v.entries), 0)::int, coalesce(sum(v.duration_seconds), 0)::int
		from round_vehicle_stats v
		join rounds r on r.id = v.round_id
		join games g on g.id = r.game_id
		where v.vehicle_code = $1
		group by 1
		order by coalesce(sum(v.duration_seconds), 0) desc, coalesce(sum(v.entries), 0) desc, 1 asc
	`, code)
	return detail, err
}

func (s *Store) KitDetail(ctx context.Context, code string) (KitDetail, error) {
	var detail KitDetail
	detail.Code = code
	err := s.db.QueryRowContext(ctx, `
		select
			coalesce(sum(pickups), 0)::int,
			coalesce(sum(duration_seconds), 0)::int,
			count(distinct player_id)::int
		from round_kit_stats
		where kit_code = $1
	`, code).Scan(&detail.Pickups, &detail.Duration, &detail.Users)
	if err != nil {
		return detail, err
	}
	detail.TopUsers, err = s.playerRanks(ctx, `
		select p.id::text, p.display_name, coalesce(sum(k.pickups), 0)::int, coalesce(sum(k.duration_seconds), 0)::int
		from round_kit_stats k
		join players p on p.id = k.player_id
		where k.kit_code = $1
		group by p.id, p.display_name
		order by coalesce(sum(k.duration_seconds), 0) desc, coalesce(sum(k.pickups), 0) desc, p.display_name asc
		limit 15
	`, code)
	return detail, err
}

func (s *Store) MapDetail(ctx context.Context, name string) (MapDetail, error) {
	var detail MapDetail
	detail.Name = name
	err := s.db.QueryRowContext(ctx, `
		select
			count(distinct r.id)::int,
			count(distinct r.id) filter (where r.winning_team_id = 1)::int,
			count(distinct r.id) filter (where r.winning_team_id = 2)::int,
			coalesce(sum(rps.kills), 0)::int,
			coalesce(avg(extract(epoch from (r.ended_at - r.started_at))), 0)::int
		from rounds r
		join games g on g.id = r.game_id
		left join round_player_stats rps on rps.round_id = r.id
		where coalesce(g.map_name, '') = $1
	`, name).Scan(&detail.Rounds, &detail.Team1Wins, &detail.Team2Wins, &detail.TotalKills, &detail.AvgDurationSec)
	if err != nil {
		return detail, err
	}
	detail.TopPlayers, err = s.playerRanks(ctx, `
		select p.id::text, p.display_name, coalesce(sum(s.kills), 0)::int, 0::int
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		join players p on p.id = s.player_id
		where coalesce(g.map_name, '') = $1
		group by p.id, p.display_name
		order by coalesce(sum(s.kills), 0) desc, p.display_name asc
		limit 15
	`, name)
	if err != nil {
		return detail, err
	}
	detail.TopWeapons, err = s.mapRanks(ctx, `
		select e.weapon_code, count(*)::int, 0::int
		from events e
		join games g on g.id = e.game_id
		where coalesce(g.map_name, '') = $1
		and e.event_type = 'score'
		and nullif(e.weapon_code, '') is not null
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		group by e.weapon_code
		order by count(*) desc, 1 asc
		limit 15
	`, name)
	if err != nil {
		return detail, err
	}
	detail.TopVehicles, err = s.mapRanks(ctx, `
		select v.vehicle_code, coalesce(sum(v.entries), 0)::int, coalesce(sum(v.duration_seconds), 0)::int
		from round_vehicle_stats v
		join rounds r on r.id = v.round_id
		join games g on g.id = r.game_id
		where coalesce(g.map_name, '') = $1
		group by v.vehicle_code
		order by coalesce(sum(v.duration_seconds), 0) desc, coalesce(sum(v.entries), 0) desc, v.vehicle_code asc
		limit 15
	`, name)
	return detail, err
}

func (s *Store) playerRanks(ctx context.Context, query string, arg string) ([]PlayerRank, error) {
	rows, err := s.db.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []PlayerRank{}
	for rows.Next() {
		var row PlayerRank
		if err := rows.Scan(&row.PlayerID, &row.DisplayName, &row.Count, &row.Seconds); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) mapRanks(ctx context.Context, query string, arg string) ([]MapRank, error) {
	rows, err := s.db.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []MapRank{}
	for rows.Next() {
		var row MapRank
		if err := rows.Scan(&row.MapName, &row.Count, &row.Seconds); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func nullableTimeReceiver(dest **time.Time) any {
	return scannerFunc(func(src any) error {
		var value sql.NullTime
		if err := value.Scan(src); err != nil {
			return err
		}
		if value.Valid {
			*dest = &value.Time
		}
		return nil
	})
}

type scannerFunc func(any) error

func (f scannerFunc) Scan(src any) error {
	return f(src)
}
