package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"
)

type streakRow struct {
	PlayerID   string
	PlayerName string
	Streak     int
	RoundID    string
	MapName    string
	EndedAt    string
}

type streaksPageData struct {
	GeneratedAt time.Time
	DBError     string
	Rows        []streakRow
	RankBase    int
	PageLinks   []filterLink
}

// streaksQuery finds each player's longest-ever run of consecutive kills, the
// same "consecutive kill events with no other actor's kill in between"
// definition fetchRoundStreaks (detail_pages.go) already uses per round, just
// generalized: partitioning by round_id (instead of scoping the whole query
// to one round_id) keeps a run from spanning a round boundary while still
// scanning every round in one pass, then distinct-on picks each player's best
// run across their whole history.
const streaksQuery = `
	with combat as (
		select
			e.round_id,
			coalesce(e.actor_player_id, actor_stats.player_id) as actor_id,
			e.timestamp_seconds,
			e.sequence_no
		from events e
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		where e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	),
	marked as (
		select *,
			case when lag(actor_id) over (partition by round_id order by timestamp_seconds, sequence_no) = actor_id then 0 else 1 end as new_run
		from combat
		where actor_id is not null
	),
	runs as (
		select round_id, actor_id, sum(new_run) over (partition by round_id order by timestamp_seconds, sequence_no) as run_id
		from marked
	),
	run_lengths as (
		select round_id, actor_id, run_id, count(*)::int as streak
		from runs
		group by round_id, actor_id, run_id
	),
	best as (
		select distinct on (actor_id) actor_id, round_id, streak
		from run_lengths
		order by actor_id, streak desc, round_id desc
	)
	select p.id::text, p.display_name, b.streak,
		r.id::text,
		coalesce(nullif(g.map_name, ''), 'unknown'),
		coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '')
	from best b
	join players p on p.id = b.actor_id and not p.hidden
	join rounds r on r.id = b.round_id
	join games g on g.id = r.game_id
	order by b.streak desc, p.display_name asc
	limit $1`

func (s *apiServer) fetchStreaks(ctx context.Context, limit int) ([]streakRow, error) {
	rows, err := s.db.QueryContext(ctx, streaksQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []streakRow
	for rows.Next() {
		var row streakRow
		if err := rows.Scan(&row.PlayerID, &row.PlayerName, &row.Streak, &row.RoundID, &row.MapName, &row.EndedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) streaksPage(w http.ResponseWriter, r *http.Request) {
	data := streaksPageData{GeneratedAt: time.Now().UTC()}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		// Window functions over every kill event site-wide, same reasoning as
		// /weapons and /kits getting a wider budget than the 5s default.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		rows, err := s.fetchStreaks(ctx, 200)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}

		const perPage = 25
		pages := (len(rows) + perPage - 1) / perPage
		if pages < 1 {
			pages = 1
		}
		page := pageParam(r, "page")
		if page > pages {
			page = pages
		}
		start := (page - 1) * perPage
		end := start + perPage
		if end > len(rows) {
			end = len(rows)
		}
		data.Rows = rows[start:end]
		data.RankBase = start
		if pages > 1 {
			for p := 1; p <= pages; p++ {
				u := "/streaks"
				if p > 1 {
					u += fmt.Sprintf("?page=%d", p)
				}
				data.PageLinks = append(data.PageLinks, filterLink{Label: fmt.Sprintf("%d", p), URL: u, Active: p == page})
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := streaksTemplate.Execute(w, data); err != nil {
		log.Printf("render streaks page failed: %v", err)
	}
}

var streaksTemplate = template.Must(template.New("streaks").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Kill Streaks - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "streaks" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Kill Streaks</strong><span class="muted">longest run of consecutive kills ever, server-wide</span></div>
<table><thead><tr><th class="rank-col">#</th><th>Player</th><th>Streak</th><th>Map</th><th>Round</th><th>Ended</th></tr></thead><tbody>
{{ range $i, $row := .Rows }}{{ $rank := add $.RankBase (rank $i) }}<tr><td class="rank-col">{{ if le $rank 3 }}<span class="medal {{ if eq $rank 1 }}g{{ else if eq $rank 2 }}s{{ else }}b{{ end }}">{{ $rank }}</span>{{ else }}{{ $rank }}{{ end }}</td><td><a class="link" href="{{ playerURL $row.PlayerID "all" }}">{{ $row.PlayerName }}</a></td><td>{{ $row.Streak }}</td><td><a class="link" href="{{ mapURL $row.MapName }}">{{ displayCode $row.MapName }}</a></td><td><a class="link" href="{{ roundURL $row.RoundID }}">view round</a></td><td class="mono muted">{{ $row.EndedAt }}</td></tr>
{{ else }}<tr><td colspan="6" class="muted">No kill streaks recorded yet.</td></tr>{{ end }}
</tbody></table>
{{ if .PageLinks }}<div class="pager"><span>Page</span>{{ range .PageLinks }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
