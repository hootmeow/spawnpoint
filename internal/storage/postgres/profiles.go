package postgres

import (
	"context"
	"database/sql"
)

type PlayerProfile struct {
	ID                string `json:"id"`
	DisplayName       string `json:"display_name"`
	IsBot             *bool  `json:"is_bot,omitempty"`
	Rounds            int    `json:"rounds"`
	Score             int    `json:"score"`
	Kills             int    `json:"kills"`
	Deaths            int    `json:"deaths"`
	Teamkills         int    `json:"teamkills"`
	Heals             int    `json:"heals"`
	Repairs           int    `json:"repairs"`
	LastSeen          string `json:"last_seen"`
	LongestKillStreak int    `json:"longest_kill_streak"`
}

func (s *Store) PlayerProfile(ctx context.Context, playerID string) (*PlayerProfile, error) {
	row := s.db.QueryRowContext(ctx, `
		select
			p.id::text,
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
			coalesce(to_char(max(coalesce(r.ended_at, g.started_at)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as last_seen
		from players p
		left join round_player_stats s on s.player_id = p.id
		left join rounds r on r.id = s.round_id
		left join games g on g.id = r.game_id
		where p.id::text = $1
		group by p.id, p.display_name
	`, playerID)

	var profile PlayerProfile
	var isBot sql.NullBool
	if err := row.Scan(&profile.ID, &profile.DisplayName, &isBot, &profile.Rounds, &profile.Score, &profile.Kills, &profile.Deaths, &profile.Teamkills, &profile.Heals, &profile.Repairs, &profile.LastSeen); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if isBot.Valid {
		profile.IsBot = &isBot.Bool
	}

	streak, err := s.killStreak(ctx, playerID, true)
	if err != nil {
		return nil, err
	}
	profile.LongestKillStreak = streak
	return &profile, nil
}
