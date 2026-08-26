package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type leaderRow struct {
	ID          string
	DisplayName string
	IsBot       *bool
	Rounds      int
	Kills       int
	Deaths      int
	Score       int
	Captures    int
	Teamkills   int
	KD          float64
	KPR         float64
}

type filterLink struct {
	Label  string
	URL    string
	Active bool
}

// leaderboardMetrics whitelists the ORDER BY expression per metric so the
// query string can never inject SQL.
var leaderboardMetrics = []struct {
	Key     string
	Label   string
	OrderBy string
}{
	{"kills", "Kills", "kills desc, score desc"},
	{"score", "Score", "score desc, kills desc"},
	{"kd", "K/D", "kd desc, kills desc"},
	{"kpr", "Kills / Round", "kpr desc, kills desc"},
	{"captures", "Captures", "captures desc, score desc"},
	{"teamkills", "Teamkills", "teamkills desc"},
}

var leaderboardPeriods = []struct {
	Key   string
	Label string
	Since time.Duration // zero means all time
}{
	{"all", "All Time", 0},
	{"24h", "Last 24h", 24 * time.Hour},
	{"7d", "7 Days", 7 * 24 * time.Hour},
	{"30d", "30 Days", 30 * 24 * time.Hour},
}

var leaderboardMinRounds = []int{1, 5, 10, 25}

func (s *apiServer) leaderboardPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := strings.TrimSpace(q.Get("metric"))
	if !leaderboardMetricValid(metric) {
		metric = "kills"
	}
	period := strings.TrimSpace(q.Get("period"))
	if !leaderboardPeriodValid(period) {
		period = "all"
	}
	mapName := strings.TrimSpace(q.Get("map"))
	minRounds := 1
	switch q.Get("min_rounds") {
	case "5":
		minRounds = 5
	case "10":
		minRounds = 10
	case "25":
		minRounds = 25
	}
	botFilter := normalizeBotFilter(q.Get("bots"))

	var serverOptions []serverOption
	if s.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		serverOptions, _ = s.fetchServerOptions(ctx)
		cancel()
	}
	serverFilter := normalizeServerFilter(q.Get("server"), serverOptions)

	data := struct {
		GeneratedAt   time.Time
		DBError       string
		Metric        string
		MetricLabel   string
		MetricFilters []filterLink
		PeriodFilters []filterLink
		MapFilters    []filterLink
		MinFilters    []filterLink
		BotFilters    []filterLink
		ServerFilters []filterLink
		Rows          []leaderRow
		RankBase      int
		PageLinks     []filterLink
	}{GeneratedAt: time.Now().UTC(), Metric: metric}
	for _, m := range leaderboardMetrics {
		if m.Key == metric {
			data.MetricLabel = m.Label
		}
	}

	link := func(key, value string) string {
		v := url.Values{}
		set := func(k, val, def string) {
			if k == key {
				val = value
			}
			if val != def && val != "" {
				v.Set(k, val)
			}
		}
		set("metric", metric, "kills")
		set("period", period, "all")
		set("map", mapName, "")
		set("min_rounds", fmt.Sprintf("%d", minRounds), "1")
		set("bots", botFilter, "all")
		set("server", serverFilter, "")
		if enc := v.Encode(); enc != "" {
			return "/leaderboard?" + enc
		}
		return "/leaderboard"
	}

	for _, m := range leaderboardMetrics {
		data.MetricFilters = append(data.MetricFilters, filterLink{m.Label, link("metric", m.Key), m.Key == metric})
	}
	for _, p := range leaderboardPeriods {
		data.PeriodFilters = append(data.PeriodFilters, filterLink{p.Label, link("period", p.Key), p.Key == period})
	}
	for _, n := range leaderboardMinRounds {
		label := fmt.Sprintf("%d+ Rounds", n)
		if n == 1 {
			label = "Any Rounds"
		}
		data.MinFilters = append(data.MinFilters, filterLink{label, link("min_rounds", fmt.Sprintf("%d", n)), n == minRounds})
	}
	for _, b := range []struct{ Key, Label string }{{"all", "Everyone"}, {"humans", "Humans"}, {"bots", "Bots"}} {
		data.BotFilters = append(data.BotFilters, filterLink{b.Label, link("bots", b.Key), b.Key == botFilter})
	}
	data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)

	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		maps, err := s.fetchMapNames(ctx)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
		data.MapFilters = append(data.MapFilters, filterLink{"All Maps", link("map", ""), mapName == ""})
		for _, name := range maps {
			data.MapFilters = append(data.MapFilters, filterLink{displayCode(name), link("map", name), name == mapName})
		}

		rows, err := s.fetchLeaderboard(ctx, metric, mapName, period, minRounds, botFilter, serverFilter, 100)
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}

		// The top 100 come back in one cheap query; the page slices it into
		// 25-row views so the standings table stays a single screen.
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
			base := link("", "")
			for p := 1; p <= pages; p++ {
				u := base
				if p > 1 {
					if strings.Contains(u, "?") {
						u += fmt.Sprintf("&page=%d", p)
					} else {
						u += fmt.Sprintf("?page=%d", p)
					}
				}
				data.PageLinks = append(data.PageLinks, filterLink{fmt.Sprintf("%d", p), u, p == page})
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := leaderboardTemplate.Execute(w, data); err != nil {
		log.Printf("render leaderboard failed: %v", err)
	}
}

func leaderboardMetricValid(metric string) bool {
	for _, m := range leaderboardMetrics {
		if m.Key == metric {
			return true
		}
	}
	return false
}

func leaderboardPeriodValid(period string) bool {
	for _, p := range leaderboardPeriods {
		if p.Key == period {
			return true
		}
	}
	return false
}

func (s *apiServer) fetchMapNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select map_name from games where map_name is not null and map_name <> '' group by map_name order by count(*) desc limit 24`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *apiServer) fetchLeaderboard(ctx context.Context, metric, mapName, period string, minRounds int, botFilter string, serverFilter string, limit int) ([]leaderRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	orderBy := leaderboardMetrics[0].OrderBy
	for _, m := range leaderboardMetrics {
		if m.Key == metric {
			orderBy = m.OrderBy
		}
	}
	var since interface{}
	for _, p := range leaderboardPeriods {
		if p.Key == period && p.Since > 0 {
			since = time.Now().UTC().Add(-p.Since)
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		select id::text, display_name, is_bot, rounds, kills, deaths, score, captures, teamkills, kd, kpr
		from (
			select
				p.id,
				p.display_name,
				case
					when bool_or(s.is_bot is true) then true
					when bool_or(s.is_bot is false) then false
					else null
				end as is_bot,
				count(distinct s.round_id)::int as rounds,
				coalesce(sum(s.kills), 0)::int as kills,
				coalesce(sum(s.deaths), 0)::int as deaths,
				coalesce(sum(s.score), 0)::int as score,
				coalesce(sum(s.captures), 0)::int as captures,
				coalesce(sum(s.teamkills), 0)::int as teamkills,
				round(coalesce(sum(s.kills), 0)::numeric / greatest(coalesce(sum(s.deaths), 0), 1), 2)::float8 as kd,
				round(coalesce(sum(s.kills), 0)::numeric / greatest(count(distinct s.round_id), 1), 2)::float8 as kpr
			from round_player_stats s
			join rounds r on r.id = s.round_id
			join games g on g.id = r.game_id
			join players p on p.id = s.player_id and not p.hidden
			where ($1 = '' or g.map_name = $1)
			  and ($2::timestamptz is null or coalesce(r.ended_at, g.started_at) >= $2)
			  and ($6 = '' or g.server_id = nullif($6,'')::uuid)
			group by p.id, p.display_name
			having count(distinct s.round_id) >= $3
		) totals
		where (
			$4 = 'all'
			or ($4 = 'bots' and is_bot is true)
			or ($4 = 'humans' and coalesce(is_bot, false) is false)
		)
		order by `+orderBy+`, display_name asc
		limit $5
	`, mapName, since, minRounds, botFilter, limit, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []leaderRow
	for rows.Next() {
		var row leaderRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.IsBot, &row.Rounds, &row.Kills, &row.Deaths, &row.Score, &row.Captures, &row.Teamkills, &row.KD, &row.KPR); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

var leaderboardTemplate = template.Must(template.New("leaderboard").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Leaderboard - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "leaderboard" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Leaderboard</strong><span class="muted">top 100 by {{ .MetricLabel }}</span></div>
<div class="filter-groups">
<div class="filter-group"><span>Sort by</span><div class="filters">{{ range .MetricFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div></div>
<div class="filter-group"><span>Period</span><div class="filters">{{ range .PeriodFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div></div>
<div class="filter-group"><span>Map</span><div class="filters">{{ range .MapFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div></div>
<div class="filter-group"><span>Min rounds</span><div class="filters">{{ range .MinFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div></div>
<div class="filter-group"><span>Players</span><div class="filters">{{ range .BotFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div></div>
{{ if .ServerFilters }}<div class="filter-group"><span>Server</span><div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div></div>{{ end }}
</div>
<table><thead><tr><th class="rank-col">#</th><th>Player</th><th>Type</th><th>Rounds</th><th{{ if eq .Metric "kills" }} class="lb-active"{{ end }}>Kills</th><th>Deaths</th><th{{ if eq .Metric "kd" }} class="lb-active"{{ end }}>K/D</th><th{{ if eq .Metric "kpr" }} class="lb-active"{{ end }}>Kills/Round</th><th{{ if eq .Metric "score" }} class="lb-active"{{ end }}>Score</th><th{{ if eq .Metric "captures" }} class="lb-active"{{ end }}>Captures</th><th{{ if eq .Metric "teamkills" }} class="lb-active"{{ end }}>Teamkills</th></tr></thead><tbody>
{{ range $i, $row := .Rows }}{{ $rank := add $.RankBase (rank $i) }}<tr><td class="rank-col">{{ if le $rank 3 }}<span class="medal {{ if eq $rank 1 }}g{{ else if eq $rank 2 }}s{{ else }}b{{ end }}">{{ $rank }}</span>{{ else }}{{ $rank }}{{ end }}</td><td><a class="link" href="{{ playerURL $row.ID "all" }}">{{ $row.DisplayName }}</a></td><td>{{ playerKind $row.IsBot }}</td><td>{{ $row.Rounds }}</td><td{{ if eq $.Metric "kills" }} class="lb-active"{{ end }}>{{ $row.Kills }}</td><td>{{ $row.Deaths }}</td><td{{ if eq $.Metric "kd" }} class="lb-active"{{ end }}>{{ printf "%.2f" $row.KD }}</td><td{{ if eq $.Metric "kpr" }} class="lb-active"{{ end }}>{{ printf "%.2f" $row.KPR }}</td><td{{ if eq $.Metric "score" }} class="lb-active"{{ end }}>{{ $row.Score }}</td><td{{ if eq $.Metric "captures" }} class="lb-active"{{ end }}>{{ $row.Captures }}</td><td{{ if eq $.Metric "teamkills" }} class="lb-active"{{ end }}>{{ $row.Teamkills }}</td></tr>
{{ else }}<tr><td colspan="11" class="muted">No players match these filters.</td></tr>{{ end }}
</tbody></table>
{{ if .PageLinks }}<div class="pager"><span>Page</span>{{ range .PageLinks }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
