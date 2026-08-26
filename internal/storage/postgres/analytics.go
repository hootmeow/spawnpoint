package postgres

import (
	"context"
	"database/sql"
	"time"
)

type PlayerAnalytics struct {
	PlayerID           string                 `json:"player_id"`
	KillFeed           []KillFeedEntry        `json:"kill_feed"`
	WeaponBreakdown    []WeaponBreakdown      `json:"weapon_breakdown"`
	MapSplits          []PlayerMapSplit       `json:"map_splits"`
	Distribution       PlayerKillDistribution `json:"distribution"`
	FirstBloods        int                    `json:"first_bloods"`
	LongestKillStreak  int                    `json:"longest_kill_streak"`
	LongestDeathStreak int                    `json:"longest_death_streak"`
}

type KillFeedEntry struct {
	Type       string    `json:"type"`
	OpponentID string    `json:"opponent_id,omitempty"`
	Opponent   string    `json:"opponent"`
	Weapon     string    `json:"weapon"`
	MapName    string    `json:"map"`
	Time       time.Time `json:"time"`
}

type WeaponBreakdown struct {
	Weapon string `json:"weapon"`
	Kills  int    `json:"kills"`
	Deaths int    `json:"deaths"`
}

type PlayerMapSplit struct {
	MapName string `json:"map_name"`
	Rounds  int    `json:"rounds"`
	Kills   int    `json:"kills"`
	Deaths  int    `json:"deaths"`
}

type PlayerKillDistribution struct {
	Average     float64 `json:"average"`
	Median      float64 `json:"median"`
	Max         int     `json:"max"`
	Consistency float64 `json:"consistency"`
}

func (s *Store) PlayerAnalytics(ctx context.Context, playerID string) (PlayerAnalytics, error) {
	analytics := PlayerAnalytics{PlayerID: playerID}
	var err error

	analytics.KillFeed, err = s.playerKillFeed(ctx, playerID)
	if err != nil {
		return analytics, err
	}
	analytics.WeaponBreakdown, err = s.playerWeaponBreakdown(ctx, playerID)
	if err != nil {
		return analytics, err
	}
	analytics.MapSplits, err = s.playerMapSplits(ctx, playerID)
	if err != nil {
		return analytics, err
	}
	analytics.Distribution, err = s.playerKillDistribution(ctx, playerID)
	if err != nil {
		return analytics, err
	}
	analytics.FirstBloods, err = s.playerFirstBloods(ctx, playerID)
	if err != nil {
		return analytics, err
	}
	analytics.LongestKillStreak, err = s.killStreak(ctx, playerID, true)
	if err != nil {
		return analytics, err
	}
	analytics.LongestDeathStreak, err = s.killStreak(ctx, playerID, false)
	return analytics, err
}

func (s *Store) playerKillFeed(ctx context.Context, playerID string) ([]KillFeedEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		with feed as (
			select
				case
					when coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1 and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills') then 'kill'
					when coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1 and lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills') then 'teamkill'
					when coalesce(e.target_player_id::text, target_stats.player_id::text) = $1 and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills') then 'death'
					when coalesce(e.target_player_id::text, target_stats.player_id::text) = $1 and lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills') then 'teamkilled'
					else ''
				end as feed_type,
				case
					when coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1 then coalesce(target.id::text, '')
					else coalesce(actor.id::text, '')
				end as opponent_id,
				case
					when coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1 then coalesce(target.display_name, 'Unknown')
					else coalesce(actor.display_name, 'Unknown')
				end as opponent,
				coalesce(e.weapon_code, ''),
				coalesce(g.map_name, ''),
				coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) as event_time,
				e.sequence_no
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
			left join players actor on actor.id = coalesce(e.actor_player_id, actor_stats.player_id)
			left join players target on target.id = coalesce(e.target_player_id, target_stats.player_id)
			where e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
			and (
				coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1
				or coalesce(e.target_player_id::text, target_stats.player_id::text) = $1
			)
		)
		select feed_type, opponent_id, opponent, weapon_code, map_name, event_time
		from feed
		where feed_type <> ''
		order by event_time desc, sequence_no desc
		limit 60
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []KillFeedEntry{}
	for rows.Next() {
		var row KillFeedEntry
		if err := rows.Scan(&row.Type, &row.OpponentID, &row.Opponent, &row.Weapon, &row.MapName, &row.Time); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) playerWeaponBreakdown(ctx context.Context, playerID string) ([]WeaponBreakdown, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			e.weapon_code as weapon,
			count(*) filter (where coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1)::int as kills,
			count(*) filter (where coalesce(e.target_player_id::text, target_stats.player_id::text) = $1)::int as deaths
		from events e
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
		where e.event_type = 'score'
		and nullif(e.weapon_code, '') is not null
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and (
			coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1
			or coalesce(e.target_player_id::text, target_stats.player_id::text) = $1
		)
		group by 1
		order by kills desc, deaths desc, weapon asc
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []WeaponBreakdown{}
	for rows.Next() {
		var row WeaponBreakdown
		if err := rows.Scan(&row.Weapon, &row.Kills, &row.Deaths); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) playerMapSplits(ctx context.Context, playerID string) ([]PlayerMapSplit, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(nullif(g.map_name, ''), 'unknown'),
			count(distinct s.round_id)::int,
			coalesce(sum(s.kills), 0)::int,
			coalesce(sum(s.deaths), 0)::int
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		where s.player_id::text = $1
		group by 1
		order by rounds desc, 1 asc
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []PlayerMapSplit{}
	for rows.Next() {
		var row PlayerMapSplit
		if err := rows.Scan(&row.MapName, &row.Rounds, &row.Kills, &row.Deaths); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) playerKillDistribution(ctx context.Context, playerID string) (PlayerKillDistribution, error) {
	var result PlayerKillDistribution
	var median sql.NullFloat64
	var stddev sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		select
			coalesce(avg(kills), 0)::float8,
			percentile_cont(0.5) within group (order by kills),
			coalesce(max(kills), 0)::int,
			stddev_samp(kills)
		from round_player_stats
		where player_id::text = $1
	`, playerID).Scan(&result.Average, &median, &result.Max, &stddev)
	if err != nil {
		return result, err
	}
	if median.Valid {
		result.Median = median.Float64
	}
	if stddev.Valid {
		result.Consistency = stddev.Float64
	}
	return result, nil
}

func (s *Store) playerFirstBloods(ctx context.Context, playerID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		with first_kills as (
			select distinct on (e.round_id)
				e.round_id,
				coalesce(e.actor_player_id::text, actor_stats.player_id::text) as actor_id
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			where e.round_id is not null
			and e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
			order by e.round_id, coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval), e.sequence_no
		)
		select count(*)::int
		from first_kills
		where actor_id = $1
	`, playerID).Scan(&count)
	return count, err
}

func (s *Store) killStreak(ctx context.Context, playerID string, kills bool) (int, error) {
	return killStreak(ctx, s.db, playerID, kills)
}

func killStreak(ctx context.Context, db *sql.DB, playerID string, kills bool) (int, error) {
	groupOnDeath := "is_death"
	countColumn := "is_kill"
	if !kills {
		groupOnDeath = "is_kill"
		countColumn = "is_death"
	}
	var streak int
	err := db.QueryRowContext(ctx, `
		with stream as (
			select
				(coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1 and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')) as is_kill,
				(coalesce(e.target_player_id::text, target_stats.player_id::text) = $1 and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')) as is_death,
				coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) as event_time,
				e.sequence_no
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
			where e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
			and (
				coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1
				or coalesce(e.target_player_id::text, target_stats.player_id::text) = $1
			)
		),
		islands as (
			select
				is_kill,
				is_death,
				sum(case when `+groupOnDeath+` then 1 else 0 end) over (order by event_time, sequence_no) as island
			from stream
		),
		runs as (
			select count(*)::int as run_length
			from islands
			where `+countColumn+`
			group by island
		)
		select coalesce(max(run_length), 0)::int from runs
	`, playerID).Scan(&streak)
	return streak, err
}
