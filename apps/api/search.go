package main

import (
	"context"
	"database/sql"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type searchPageData struct {
	GeneratedAt time.Time
	DBError     string
	Query       string
	Players     []searchResultRow
	Maps        []searchResultRow
	Rounds      []searchResultRow
	Weapons     []searchResultRow
	Vehicles    []searchResultRow
}

type searchResultRow struct {
	Title  string
	Detail string
	Href   string
}

func (s *apiServer) searchPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := searchPageData{GeneratedAt: time.Now().UTC(), Query: q}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else if q != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		setError := func(err error) {
			if err != nil && data.DBError == "" {
				data.DBError = dbErrorMessage(err)
			}
		}
		var err error
		data.Players, err = s.fetchSearchRows(ctx, searchPlayersQuery, q, 8, playerSearchHref)
		setError(err)
		data.Maps, err = s.fetchSearchRows(ctx, searchMapsQuery, q, 8, mapSearchHref)
		setError(err)
		data.Rounds, err = s.fetchSearchRows(ctx, searchRoundsQuery, q, 8, roundSearchHref)
		setError(err)
		data.Weapons, err = s.fetchSearchRows(ctx, searchWeaponsQuery, q, 8, weaponSearchHref)
		setError(err)
		data.Vehicles, err = s.fetchSearchRows(ctx, searchVehiclesQuery, q, 8, vehicleSearchHref)
		setError(err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := searchTemplate.Execute(w, data); err != nil {
		log.Printf("render search page failed: %v", err)
	}
}

func (s *apiServer) fetchSearchRows(ctx context.Context, query string, term string, limit int, href func(string) string) ([]searchResultRow, error) {
	rows, err := s.db.QueryContext(ctx, query, term, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []searchResultRow
	for rows.Next() {
		var id sql.NullString
		var title sql.NullString
		var detail sql.NullString
		if err := rows.Scan(&id, &title, &detail); err != nil {
			return nil, err
		}
		key := strings.TrimSpace(id.String)
		if key == "" {
			key = strings.TrimSpace(title.String)
		}
		result = append(result, searchResultRow{
			Title:  title.String,
			Detail: detail.String,
			Href:   href(key),
		})
	}
	return result, rows.Err()
}

func playerSearchHref(id string) string {
	return "/player?id=" + url.QueryEscape(id)
}

func mapSearchHref(name string) string {
	return mapURL(name)
}

func roundSearchHref(id string) string {
	return roundURL(id)
}

func weaponSearchHref(code string) string {
	return weaponURL(code)
}

func vehicleSearchHref(code string) string {
	return vehicleURL(code)
}

const searchPlayersQuery = `
	select p.id::text, p.display_name,
		coalesce(sum(s.kills), 0)::text || ' kills / ' || coalesce(sum(s.rounds), 0)::text || ' rounds'
	from players p
	left join (
		select player_id, count(distinct round_id)::int rounds, sum(kills)::int kills
		from round_player_stats
		group by player_id
	) s on s.player_id = p.id
	where p.display_name ilike '%' || $1 || '%' and not p.hidden
	group by p.id, p.display_name
	order by coalesce(sum(s.kills), 0) desc, p.display_name asc
	limit $2`

const searchMapsQuery = `
	select coalesce(g.map_name, ''), coalesce(g.map_name, ''),
		count(distinct r.id)::text || ' rounds / ' || coalesce(sum(s.kills), 0)::text || ' kills'
	from games g
	left join rounds r on r.game_id = g.id
	left join round_player_stats s on s.round_id = r.id
	where coalesce(g.map_name, '') <> '' and coalesce(g.map_name, '') ilike '%' || $1 || '%'
	group by coalesce(g.map_name, '')
	order by count(distinct r.id) desc, coalesce(g.map_name, '') asc
	limit $2`

const searchRoundsQuery = `
	select r.id::text,
		coalesce(g.map_name, 'unknown'),
		coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), 'round') || ' / ' || coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI UTC'), '')
	from rounds r
	join games g on g.id = r.game_id
	left join raw_log_files raw on raw.id = g.raw_log_file_id
	where coalesce(g.map_name, '') ilike '%' || $1 || '%'
	   or ($1 <> '' and encode(raw.content_sha256, 'hex') ilike $1 || '%')
	   or r.id::text = lower($1)
	order by coalesce(r.ended_at, g.started_at) desc
	limit $2`

const searchWeaponsQuery = `
	select code, code, count::text || ' score events'
	from (
		select coalesce(e.weapon_code, '') as code, count(*)::int
		from events e
		where e.event_type = 'score'
		  and coalesce(e.weapon_code, '') <> ''
		  and coalesce(e.weapon_code, '') ilike '%' || $1 || '%'
		group by coalesce(e.weapon_code, '')
		order by count(*) desc, coalesce(e.weapon_code, '') asc
		limit $2
	) x`

const searchVehiclesQuery = `
	select code, code, count::text || ' vehicle events'
	from (
		select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') as code, count(*)::int
		from events e
		where (e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle') or (e.event_type = 'score' and coalesce(e.vehicle_code, '') <> ''))
		  and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')) <> ''
		  and regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') ilike '%' || $1 || '%'
		group by 1
		order by count(*) desc, 1 asc
		limit $2
	) x`

var searchTemplate = template.Must(template.New("search").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Search - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Search</strong><span class="muted">players, maps, rounds, weapons, and vehicles</span></div>
<form class="search" method="get" action="/search"><input type="search" name="q" value="{{ .Query }}" placeholder="Search the site"><button type="submit">Search</button></form>
{{ if .Query }}
<div class="grid2">
{{ template "searchGroup" dict "Title" "Players" "Rows" .Players }}
{{ template "searchGroup" dict "Title" "Maps" "Rows" .Maps }}
{{ template "searchGroup" dict "Title" "Rounds" "Rows" .Rounds }}
{{ template "searchGroup" dict "Title" "Weapons" "Rows" .Weapons }}
{{ template "searchGroup" dict "Title" "Vehicles" "Rows" .Vehicles }}
</div>
{{ else }}<section class="panel"><h3>Ready</h3><p class="muted">Enter a player, map, round id, weapon, or vehicle code.</p></section>{{ end }}
</main>{{ siteFooter }}</body></html>`))

func init() {
	template.Must(searchTemplate.New("searchGroup").Parse(`<section class="panel"><h3>{{ .Title }}</h3><div class="metric-list">{{ range .Rows }}<a class="metric-row" href="{{ .Href }}"><span>Open</span><strong>&rarr;</strong><em>{{ .Title }}</em><small>{{ .Detail }}</small></a>{{ else }}<div class="muted">No matches.</div>{{ end }}</div></section>`))
}
