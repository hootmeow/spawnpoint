package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

type comparePageData struct {
	GeneratedAt time.Time
	DBError     string
	Type        string
	A           string
	B           string
	Rows        []keyValueRow
}

type statusPageData struct {
	GeneratedAt time.Time
	DBError     string
	Role        string
	Rows        []keyValueRow
	Jobs        []keyValueRow
}

func (s *apiServer) comparePage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data := comparePageData{
		GeneratedAt: time.Now().UTC(),
		Type:        strings.TrimSpace(r.URL.Query().Get("type")),
		A:           sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("a"))),
		B:           sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("b"))),
	}
	if data.Type == "" {
		data.Type = "player"
	}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else if data.A != "" && data.B != "" {
		if data.Type == "player" {
			// Substitute nilUUID for a hidden or nonexistent player id, the
			// same treatment /rivalry uses, so this page can't be used to
			// pull a hidden player's name and stats by their raw uuid.
			if ids, err := s.resolvePlayerIDs(ctx, data.A, "", 0); err == nil && len(ids) == 1 {
				data.A = ids[0]
			}
			if ids, err := s.resolvePlayerIDs(ctx, data.B, "", 0); err == nil && len(ids) == 1 {
				data.B = ids[0]
			}
		}
		rows, err := s.fetchCompareRows(ctx, data.Type, data.A, data.B)
		data.Rows = rows
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := compareTemplate.Execute(w, data); err != nil {
		log.Printf("render compare page failed: %v", err)
	}
}

func (s *apiServer) statusPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data := statusPageData{GeneratedAt: time.Now().UTC(), Role: requestAdminIdentity(r).Role}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		rows, err := s.fetchStatusRows(ctx)
		data.Rows = rows
		if err != nil {
			data.DBError = err.Error()
		}
		jobs, err := s.fetchRecentIngestJobs(ctx)
		data.Jobs = jobs
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTemplate.Execute(w, data); err != nil {
		log.Printf("render status page failed: %v", err)
	}
}

func (s *apiServer) fetchRecentIngestJobs(ctx context.Context) ([]keyValueRow, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `select to_regclass('public.ingest_jobs') is not null`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return []keyValueRow{{Label: "Ingest Jobs", Value: "not installed", Detail: "apply migration 0003_ingest_jobs_security.sql"}}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		select
			status,
			original_name,
			coalesce(error_message, '') ||
				case when rounds > 0 or events > 0 then ' rounds=' || rounds::text || ' events=' || events::text else '' end
		from ingest_jobs
		order by started_at desc
		limit 12
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []keyValueRow
	for rows.Next() {
		var row keyValueRow
		if err := rows.Scan(&row.Label, &row.Value, &row.Detail); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchStatusRows(ctx context.Context) ([]keyValueRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with latest as (
			select coalesce(to_char(max(coalesce(r.ended_at, g.started_at)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as last_round
			from rounds r join games g on g.id = r.game_id
		)
		select 'Database', current_database(), 'connected'
		union all select 'Rounds', count(*)::text, (select last_round from latest) from rounds
		union all select 'Players', count(*)::text, 'known player ids' from players
		union all select 'Events', count(*)::text, 'raw combat/objective feed' from events
		union all select 'Raw Logs', count(*)::text, coalesce(max(original_name), '') from raw_log_files
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []keyValueRow
	for rows.Next() {
		var row keyValueRow
		if err := rows.Scan(&row.Label, &row.Value, &row.Detail); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchCompareRows(ctx context.Context, compareType string, a string, b string) ([]keyValueRow, error) {
	switch compareType {
	case "weapon":
		return s.fetchCompareQuery(ctx, weaponCompareQuery, a, b)
	case "vehicle":
		return s.fetchCompareQuery(ctx, vehicleCompareQuery, stripPCOSuffix(a), stripPCOSuffix(b))
	case "map":
		return s.fetchCompareQuery(ctx, mapCompareQuery, a, b)
	default:
		return s.fetchCompareQuery(ctx, playerCompareQuery, a, b)
	}
}

func (s *apiServer) fetchCompareQuery(ctx context.Context, query string, a string, b string) ([]keyValueRow, error) {
	rows, err := s.db.QueryContext(ctx, query, a, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []keyValueRow
	for rows.Next() {
		var row keyValueRow
		if err := rows.Scan(&row.Label, &row.Value, &row.Detail); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

const playerCompareQuery = `
	with stats as (
		select p.id::text as id, p.display_name as name, count(distinct s.round_id)::int rounds,
			coalesce(sum(s.score), 0)::int score, coalesce(sum(s.kills), 0)::int kills,
			coalesce(sum(s.deaths), 0)::int deaths, coalesce(sum(s.teamkills), 0)::int teamkills,
			coalesce(sum(s.heals + s.repairs), 0)::int support
		from players p left join round_player_stats s on s.player_id = p.id
		where p.id::text in ($1, $2)
		group by p.id, p.display_name
	)
	select 'Name', coalesce((select name from stats where id=$1), $1), coalesce((select name from stats where id=$2), $2)
	union all select 'Rounds', coalesce((select rounds::text from stats where id=$1), '0'), coalesce((select rounds::text from stats where id=$2), '0')
	union all select 'Score', coalesce((select score::text from stats where id=$1), '0'), coalesce((select score::text from stats where id=$2), '0')
	union all select 'Kills', coalesce((select kills::text from stats where id=$1), '0'), coalesce((select kills::text from stats where id=$2), '0')
	union all select 'Deaths', coalesce((select deaths::text from stats where id=$1), '0'), coalesce((select deaths::text from stats where id=$2), '0')
	union all select 'Teamkills', coalesce((select teamkills::text from stats where id=$1), '0'), coalesce((select teamkills::text from stats where id=$2), '0')
	union all select 'Support', coalesce((select support::text from stats where id=$1), '0'), coalesce((select support::text from stats where id=$2), '0')`

const weaponCompareQuery = `
	with stats as (
		select coalesce(weapon_code, 'unknown') code,
			count(*) filter (where lower(coalesce(params->>'score_type','')) in ('kill','kills'))::int kills,
			count(*) filter (where lower(coalesce(params->>'score_type','')) in ('tk','teamkill','teamkills'))::int teamkills,
			count(distinct round_id)::int rounds,
			count(distinct coalesce(actor_player_id::text, actor_slot_id::text))::int users
		from events
		where event_type='score' and coalesce(weapon_code, '') in ($1, $2)
		group by coalesce(weapon_code, 'unknown')
	)
	select 'Weapon', $1, $2
	union all select 'Kills', coalesce((select kills::text from stats where code=$1),'0'), coalesce((select kills::text from stats where code=$2),'0')
	union all select 'Teamkills', coalesce((select teamkills::text from stats where code=$1),'0'), coalesce((select teamkills::text from stats where code=$2),'0')
	union all select 'Users', coalesce((select users::text from stats where code=$1),'0'), coalesce((select users::text from stats where code=$2),'0')
	union all select 'Rounds', coalesce((select rounds::text from stats where code=$1),'0'), coalesce((select rounds::text from stats where code=$2),'0')`

const vehicleCompareQuery = `
	with stats as (
		select regexp_replace(coalesce(nullif(vehicle_code,''), nullif(weapon_code,''), 'unknown'), '_PCO[0-9]+$', '') code,
			count(*) filter (where event_type='enter_vehicle')::int entries,
			count(*) filter (where event_type='score' and lower(coalesce(params->>'score_type','')) in ('kill','kills'))::int kills,
			count(*) filter (where event_type in ('destroy_vehicle') or raw_event_name='destroyVehicle')::int destroyed,
			count(distinct round_id)::int rounds,
			count(distinct coalesce(actor_player_id::text, actor_slot_id::text))::int users
		from events
		where regexp_replace(coalesce(nullif(vehicle_code,''), nullif(weapon_code,''), 'unknown'), '_PCO[0-9]+$', '') in ($1, $2)
		group by 1
	)
	select 'Vehicle', $1, $2
	union all select 'Entries', coalesce((select entries::text from stats where code=$1),'0'), coalesce((select entries::text from stats where code=$2),'0')
	union all select 'Kills', coalesce((select kills::text from stats where code=$1),'0'), coalesce((select kills::text from stats where code=$2),'0')
	union all select 'Destroyed', coalesce((select destroyed::text from stats where code=$1),'0'), coalesce((select destroyed::text from stats where code=$2),'0')
	union all select 'Users', coalesce((select users::text from stats where code=$1),'0'), coalesce((select users::text from stats where code=$2),'0')
	union all select 'Rounds', coalesce((select rounds::text from stats where code=$1),'0'), coalesce((select rounds::text from stats where code=$2),'0')`

const mapCompareQuery = `
	with stats as (
		select coalesce(g.map_name, 'unknown') name, count(distinct r.id)::int rounds,
			coalesce(sum(s.kills), 0)::int kills, count(distinct s.player_id)::int players,
			count(distinct r.id) filter (where r.winning_team_id=1)::int team1_wins,
			count(distinct r.id) filter (where r.winning_team_id=2)::int team2_wins
		from games g join rounds r on r.game_id=g.id left join round_player_stats s on s.round_id=r.id
		where coalesce(g.map_name, 'unknown') in ($1, $2)
		group by 1
	)
	select 'Map', $1, $2
	union all select 'Rounds', coalesce((select rounds::text from stats where name=$1),'0'), coalesce((select rounds::text from stats where name=$2),'0')
	union all select 'Kills', coalesce((select kills::text from stats where name=$1),'0'), coalesce((select kills::text from stats where name=$2),'0')
	union all select 'Players', coalesce((select players::text from stats where name=$1),'0'), coalesce((select players::text from stats where name=$2),'0')
	union all select 'Axis Wins', coalesce((select team1_wins::text from stats where name=$1),'0'), coalesce((select team1_wins::text from stats where name=$2),'0')
	union all select 'Allies Wins', coalesce((select team2_wins::text from stats where name=$1),'0'), coalesce((select team2_wins::text from stats where name=$2),'0')`

var compareTemplate = template.Must(template.New("compare").Funcs(detailFuncMap()).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Compare - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head><body>{{ siteHeader "compare" }}<main>{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}<div class="page-title"><strong>Compare</strong><span class="muted">players, weapons, vehicles, and maps side by side</span></div><form class="search" method="get" action="/compare"><input type="search" name="type" value="{{ .Type }}" placeholder="player, weapon, vehicle, map"><input type="search" name="a" value="{{ .A }}" placeholder="First id or code"><input type="search" name="b" value="{{ .B }}" placeholder="Second id or code"><button type="submit">Compare</button></form><div class="section-tabs"><a href="/compare?type=player">Players</a><a href="/compare?type=weapon">Weapons</a><a href="/compare?type=vehicle">Vehicles</a><a href="/compare?type=map">Maps</a></div><table><thead><tr><th>Metric</th><th>{{ displayCode .A }}</th><th>{{ displayCode .B }}</th></tr></thead><tbody>{{ range .Rows }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">Enter two ids or codes to compare.</td></tr>{{ end }}</tbody></table></main>{{ siteFooter }}</body></html>`))

var statusTemplate = template.Must(template.New("status").Funcs(detailFuncMap()).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Status - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head><body>{{ siteHeader "" }}<main>{{ adminNav "status" .Role }}{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}<div class="page-title"><strong>Status</strong><span class="muted">database and ingest health snapshot</span></div><table><thead><tr><th>Area</th><th>Value</th><th>Detail</th></tr></thead><tbody>{{ range .Rows }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No status rows.</td></tr>{{ end }}</tbody></table><h2>Recent Ingest Jobs</h2><table><thead><tr><th>Status</th><th>File</th><th>Detail</th></tr></thead><tbody>{{ range .Jobs }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td class="wrap">{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No ingest jobs.</td></tr>{{ end }}</tbody></table></main>{{ siteFooter }}</body></html>`))

func init() {
	for _, tmpl := range []*template.Template{compareTemplate, statusTemplate} {
		template.Must(tmpl.New("playerCountTable").Parse(playerCountTableHTML))
		template.Must(tmpl.New("operatorTable").Parse(operatorTableHTML))
		template.Must(tmpl.New("mapCountTable").Parse(mapCountTableHTML))
		template.Must(tmpl.New("eventTable").Parse(eventTableHTML))
	}
}
