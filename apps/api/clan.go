package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Clan/tag grouping. clan_tag is extracted at ingest time (and backfilled by
// migration 0012) from nickname conventions like "[TAG]Player" or
// "Player-TAG-" — see domain.ExtractClanTag. round_player_stats is small
// (tens of thousands of rows on this project's own install), so these
// aggregates run live with no materialized view, unlike the site-wide
// weapon/vehicle/kit pages.

type clanRow struct {
	Tag     string
	Members int
	Rounds  int
	Kills   int
	Deaths  int
}

type clanOpponentRow struct {
	Tag          string
	RoundsShared int
	Kills        int
	Deaths       int
}

type clansPageData struct {
	GeneratedAt   time.Time
	DBError       string
	ServerFilter  string
	ServerFilters []filterLink
	Clans         []clanRow
	KillBars      template.HTML
}

type clanPageData struct {
	GeneratedAt time.Time
	DBError     string
	Tag         string
	Members     int
	Rounds      int
	Kills       int
	Deaths      int
	Roster      []codePlayerRow
	RosterBars  template.HTML
	Opponents   []clanOpponentRow
}

func (s *apiServer) clansPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	data := clansPageData{GeneratedAt: time.Now().UTC()}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := func(_, value string) string {
			if value == "" {
				return "/clans"
			}
			return "/clans?server=" + queryEscape(value)
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)

		rows, err := s.db.QueryContext(ctx, `
			select p.clan_tag,
			       count(distinct p.id)::int,
			       count(distinct s.round_id)::int,
			       coalesce(sum(s.kills), 0)::int,
			       coalesce(sum(s.deaths), 0)::int
			from round_player_stats s
			join rounds r on r.id = s.round_id
			join games g on g.id = r.game_id
			join players p on p.id = s.player_id and not p.hidden
			where p.clan_tag is not null
			  and ($1 = '' or g.server_id = nullif($1,'')::uuid)
			group by p.clan_tag
			order by 4 desc, p.clan_tag asc
			limit 100
		`, serverFilter)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		} else {
			defer rows.Close()
			for rows.Next() {
				var row clanRow
				if err := rows.Scan(&row.Tag, &row.Members, &row.Rounds, &row.Kills, &row.Deaths); err != nil {
					data.DBError = dbErrorMessage(err)
					break
				}
				data.Clans = append(data.Clans, row)
			}
			if err := rows.Err(); err != nil && data.DBError == "" {
				data.DBError = dbErrorMessage(err)
			}
		}
		data.KillBars = clanBars(data.Clans)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := clansTemplate.Execute(w, data); err != nil {
		log.Printf("render clans page failed: %v", err)
	}
}

func (s *apiServer) clanPage(w http.ResponseWriter, r *http.Request) {
	tag := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("tag")))
	data := clanPageData{GeneratedAt: time.Now().UTC(), Tag: tag}
	switch {
	case s.db == nil:
		data.DBError = "DATABASE_URL is not configured"
	case tag == "":
		data.DBError = "missing ?tag= parameter"
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.fillClanPage(ctx, &data); err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := clanTemplate.Execute(w, data); err != nil {
		log.Printf("render clan page failed: %v", err)
	}
}

func (s *apiServer) fillClanPage(ctx context.Context, data *clanPageData) error {
	err := s.db.QueryRowContext(ctx, `
		select count(distinct p.id)::int, count(distinct s.round_id)::int,
		       coalesce(sum(s.kills), 0)::int, coalesce(sum(s.deaths), 0)::int
		from players p
		join round_player_stats s on s.player_id = p.id
		where p.clan_tag = $1 and not p.hidden
	`, data.Tag).Scan(&data.Members, &data.Rounds, &data.Kills, &data.Deaths)
	if err != nil {
		return err
	}

	rows, err := s.db.QueryContext(ctx, `
		select p.id::text, p.display_name, coalesce(sum(s.kills), 0)::int, count(distinct s.round_id)::int
		from players p
		join round_player_stats s on s.player_id = p.id
		where p.clan_tag = $1 and not p.hidden
		group by p.id, p.display_name
		order by 3 desc, p.display_name asc
		limit 50
	`, data.Tag)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row codePlayerRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Count, &row.Extra); err != nil {
			return err
		}
		data.Roster = append(data.Roster, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	data.RosterBars = codePlayerBars(data.Roster, colorKills)

	opponentRows, err := s.db.QueryContext(ctx, `
		with clan_rounds as (
			select distinct s.round_id
			from round_player_stats s
			join players p on p.id = s.player_id
			where p.clan_tag = $1 and not p.hidden
		)
		select p2.clan_tag, count(distinct s2.round_id)::int,
		       coalesce(sum(s2.kills), 0)::int, coalesce(sum(s2.deaths), 0)::int
		from round_player_stats s2
		join players p2 on p2.id = s2.player_id and not p2.hidden
		where s2.round_id in (select round_id from clan_rounds)
		  and p2.clan_tag is not null and p2.clan_tag <> $1
		group by p2.clan_tag
		order by 2 desc, p2.clan_tag asc
		limit 10
	`, data.Tag)
	if err != nil {
		return err
	}
	defer opponentRows.Close()
	for opponentRows.Next() {
		var row clanOpponentRow
		if err := opponentRows.Scan(&row.Tag, &row.RoundsShared, &row.Kills, &row.Deaths); err != nil {
			return err
		}
		data.Opponents = append(data.Opponents, row)
	}
	return opponentRows.Err()
}

func clanBars(rows []clanRow) template.HTML {
	items := make([]barItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, barItem{Label: row.Tag, Value: row.Kills, URL: clanURL(row.Tag)})
	}
	return barListHTML(items, colorKills)
}

func clanURL(tag string) string {
	v := url.Values{}
	v.Set("tag", tag)
	return "/clan?" + v.Encode()
}

var clansTemplate = template.Must(template.New("clans").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Clans - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "clans" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Clans</strong><span class="muted">grouped by [TAG]/-TAG-/=TAG=/{TAG} nickname prefixes and suffixes</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="grid2">
<section class="panel"><h3>Kills by Clan</h3>{{ .KillBars }}</section>
<section class="panel"><h3>Standings</h3><table><thead><tr><th>Clan</th><th>Members</th><th>Rounds</th><th>Kills</th><th>Deaths</th><th>K/D</th></tr></thead><tbody>
{{ range .Clans }}<tr><td><a class="link" href="{{ clanURL .Tag }}">{{ .Tag }}</a></td><td>{{ .Members }}</td><td>{{ .Rounds }}</td><td>{{ .Kills }}</td><td>{{ .Deaths }}</td><td>{{ formatKD .Kills .Deaths }}</td></tr>
{{ else }}<tr><td colspan="6" class="muted">No clan tags found yet &mdash; players need a nickname like [TAG]Name to show up here.</td></tr>{{ end }}
</tbody></table></section>
</div>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))

var clanTemplate = template.Must(template.New("clan").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{ .Tag }} Clan - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "clans" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>{{ .Tag }}</strong><span class="muted">clan</span></div>
<div class="section-tabs"><a href="/clans">&larr; All Clans</a></div>
<div class="kpi-grid">
<div class="kpi-card"><span>Members</span><strong>{{ .Members }}</strong></div>
<div class="kpi-card"><span>Rounds</span><strong>{{ .Rounds }}</strong></div>
<div class="kpi-card gold"><span>Kills</span><strong>{{ .Kills }}</strong></div>
<div class="kpi-card red"><span>Deaths</span><strong>{{ .Deaths }}</strong></div>
<div class="kpi-card"><span>K/D</span><strong>{{ formatKD .Kills .Deaths }}</strong></div>
</div>
<div class="grid2">
<section class="panel"><h3>Roster</h3><table><thead><tr><th>Player</th><th>Kills</th><th>Rounds</th></tr></thead><tbody>
{{ range .Roster }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Count }}</td><td>{{ .Extra }}</td></tr>
{{ else }}<tr><td colspan="3" class="muted">No members.</td></tr>{{ end }}
</tbody></table></section>
<section class="panel"><h3>Roster Kills</h3>{{ .RosterBars }}</section>
</div>
<section class="panel"><h3>Frequent Opponents</h3><span class="muted">other clans sharing the most rounds with {{ .Tag }}, and their combined stats in those rounds</span><table><thead><tr><th>Clan</th><th>Rounds Together</th><th>Their Kills</th><th>Their Deaths</th></tr></thead><tbody>
{{ range .Opponents }}<tr><td><a class="link" href="{{ clanURL .Tag }}">{{ .Tag }}</a></td><td>{{ .RoundsShared }}</td><td>{{ .Kills }}</td><td>{{ .Deaths }}</td></tr>
{{ else }}<tr><td colspan="4" class="muted">No overlapping clans yet.</td></tr>{{ end }}
</tbody></table></section>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
