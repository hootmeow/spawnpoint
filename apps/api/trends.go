package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"time"
)

type trendsData struct {
	GeneratedAt   time.Time
	DBError       string
	ServerFilters []filterLink
	TotalRounds   int
	Rounds24h     int
	Kills24h      int
	Humans7d      int
	BusiestHour   string
	RoundsPerDay  template.HTML
	KillsPerDay   template.HTML
	PlayersPerDay template.HTML
	HourOfDay     template.HTML
	MapShare      template.HTML
}

func (s *apiServer) trendsPage(w http.ResponseWriter, r *http.Request) {
	data := trendsData{GeneratedAt: time.Now().UTC()}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		link := func(_, value string) string {
			if value == "" {
				return "/trends"
			}
			return "/trends?server=" + queryEscape(value)
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		if err := s.fillTrends(ctx, &data, serverFilter); err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := trendsTemplate.Execute(w, data); err != nil {
		log.Printf("render trends failed: %v", err)
	}
}

func (s *apiServer) fillTrends(ctx context.Context, data *trendsData, serverFilter string) error {
	dayItems := func(query string) ([]barItem, error) {
		rows, err := s.db.QueryContext(ctx, query, serverFilter)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []barItem
		for rows.Next() {
			var item barItem
			if err := rows.Scan(&item.Label, &item.Value); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}

	// Rounds per day (last 30 days with data).
	roundsPerDay, err := dayItems(`
		select to_char(day, 'MM-DD'), n::int from (
			select date_trunc('day', coalesce(r.ended_at, g.started_at)) as day, count(*) as n
			from rounds r join games g on g.id = r.game_id
			where coalesce(r.ended_at, g.started_at) >= now() - interval '30 days'
			  and ($1 = '' or g.server_id = nullif($1,'')::uuid)
			group by 1 order by 1
		) t`)
	if err != nil {
		return err
	}
	data.RoundsPerDay = columnChartSVG(roundsPerDay, colorInfo, "Rounds per day")

	killsPerDay, err := dayItems(`
		select to_char(day, 'MM-DD'), n::int from (
			select date_trunc('day', coalesce(r.ended_at, g.started_at)) as day, coalesce(sum(s.kills), 0) as n
			from round_player_stats s
			join rounds r on r.id = s.round_id
			join games g on g.id = r.game_id
			where coalesce(r.ended_at, g.started_at) >= now() - interval '30 days'
			  and ($1 = '' or g.server_id = nullif($1,'')::uuid)
			group by 1 order by 1
		) t`)
	if err != nil {
		return err
	}
	data.KillsPerDay = columnChartSVG(killsPerDay, colorKills, "Kills per day")

	playersPerDay, err := dayItems(`
		select to_char(day, 'MM-DD'), n::int from (
			select date_trunc('day', coalesce(r.ended_at, g.started_at)) as day, count(distinct s.player_id) as n
			from round_player_stats s
			join rounds r on r.id = s.round_id
			join games g on g.id = r.game_id
			where coalesce(r.ended_at, g.started_at) >= now() - interval '30 days'
			  and ($1 = '' or g.server_id = nullif($1,'')::uuid)
			group by 1 order by 1
		) t`)
	if err != nil {
		return err
	}
	data.PlayersPerDay = columnChartSVG(playersPerDay, colorScore, "Active players per day")

	hourOfDay, err := dayItems(`
		select lpad(h::text, 2, '0') || ':00', n::int from (
			select extract(hour from coalesce(r.ended_at, g.started_at))::int as h, count(*) as n
			from rounds r join games g on g.id = r.game_id
			where ($1 = '' or g.server_id = nullif($1,'')::uuid)
			group by 1 order by 1
		) t`)
	if err != nil {
		return err
	}
	data.HourOfDay = columnChartSVG(hourOfDay, colorInfo, "Rounds by hour of day")
	busiest := 0
	for _, item := range hourOfDay {
		if item.Value > busiest {
			busiest = item.Value
			data.BusiestHour = item.Label + " UTC"
		}
	}

	mapShare, err := dayItems(`
		select g.map_name, count(*)::int as n
		from rounds r join games g on g.id = r.game_id
		where g.map_name is not null and g.map_name <> ''
		  and ($1 = '' or g.server_id = nullif($1,'')::uuid)
		group by 1 order by 2 desc limit 12`)
	if err != nil {
		return err
	}
	for i := range mapShare {
		mapShare[i].Label = displayCode(mapShare[i].Label)
	}
	data.MapShare = barListHTML(mapShare, colorInfo)

	// KPI row.
	err = s.db.QueryRowContext(ctx, `
		select
			(select count(*) from rounds r join games g on g.id = r.game_id
				where ($1 = '' or g.server_id = nullif($1,'')::uuid)),
			(select count(*) from rounds r join games g on g.id = r.game_id
				where coalesce(r.ended_at, g.started_at) >= now() - interval '24 hours'
				  and ($1 = '' or g.server_id = nullif($1,'')::uuid)),
			(select coalesce(sum(s.kills), 0) from round_player_stats s
				join rounds r on r.id = s.round_id
				join games g on g.id = r.game_id
				where coalesce(r.ended_at, g.started_at) >= now() - interval '24 hours'
				  and ($1 = '' or g.server_id = nullif($1,'')::uuid)),
			(select count(distinct s.player_id) from round_player_stats s
				join rounds r on r.id = s.round_id
				join games g on g.id = r.game_id
				where coalesce(s.is_bot, false) is false
				  and coalesce(r.ended_at, g.started_at) >= now() - interval '7 days'
				  and ($1 = '' or g.server_id = nullif($1,'')::uuid))
	`, serverFilter).Scan(&data.TotalRounds, &data.Rounds24h, &data.Kills24h, &data.Humans7d)
	return err
}

var trendsTemplate = template.Must(template.New("trends").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Trends - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "trends" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Server Trends</strong><span class="muted">activity, population, and rotation over time</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="kpi-grid">
<div class="kpi-card"><span>Total Rounds</span><strong>{{ .TotalRounds }}</strong></div>
<div class="kpi-card gold"><span>Rounds (24h)</span><strong>{{ .Rounds24h }}</strong></div>
<div class="kpi-card red"><span>Kills (24h)</span><strong>{{ .Kills24h }}</strong></div>
<div class="kpi-card green"><span>Human Players (7d)</span><strong>{{ .Humans7d }}</strong></div>
<div class="kpi-card"><span>Busiest Hour</span><strong>{{ if .BusiestHour }}{{ .BusiestHour }}{{ else }}&mdash;{{ end }}</strong></div>
</div>
<div class="grid2">
<section class="panel"><h3>Rounds per Day</h3><div class="chart">{{ .RoundsPerDay }}</div></section>
<section class="panel"><h3>Kills per Day</h3><div class="chart">{{ .KillsPerDay }}</div></section>
<section class="panel"><h3>Active Players per Day</h3><div class="chart">{{ .PlayersPerDay }}</div></section>
<section class="panel"><h3>Rounds by Hour of Day (UTC)</h3><div class="chart">{{ .HourOfDay }}</div></section>
</div>
<div class="grid2">
<section class="panel"><h3>Map Rotation Share (rounds)</h3>{{ .MapShare }}</section>
</div>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
