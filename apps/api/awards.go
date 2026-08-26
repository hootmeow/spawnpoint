package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type awardWinner struct {
	ID          string
	DisplayName string
	Value       string
	Rounds      int
}

type awardPanel struct {
	Title   string
	Blurb   string
	Winners []awardWinner
}

type roundRecordRow struct {
	Title       string
	ID          string
	DisplayName string
	Value       int
	MapName     string
	When        string
}

// aggregateAwards defines the career award categories. Value and OrderBy are
// trusted SQL fragments selected by key, never taken from the query string.
// MVExpr is the equivalent expression against award_totals_mv's
// already-summed-per-player columns (migration 0010) — used for the
// default (period=all, bots=all, no server filter) fast path, where it's
// mathematically identical to Value (sum-then-ratio equals ratio-of-sums).
var aggregateAwards = []struct {
	Title     string
	Blurb     string
	Value     string
	MVExpr    string
	Format    string
	MinRounds int
}{
	{"The Butcher", "Most kills", "coalesce(sum(s.kills),0)", "kills", "%.0f", 1},
	{"Sharpshooter", "Best kill/death ratio, 10+ rounds", "round(coalesce(sum(s.kills),0)::numeric / greatest(coalesce(sum(s.deaths),0),1), 2)", "round(kills::numeric / greatest(deaths,1), 2)", "%.2f", 10},
	{"Point Machine", "Highest total score", "coalesce(sum(s.score),0)", "score", "%.0f", 1},
	{"Flag Runner", "Most flag captures", "coalesce(sum(s.captures),0)", "captures", "%.0f", 1},
	{"Field Medic", "Most heals given", "coalesce(sum(s.heals),0)", "heals", "%.0f", 1},
	{"Master Engineer", "Most repairs", "coalesce(sum(s.repairs),0)", "repairs", "%.0f", 1},
	{"Stronghold", "Most flag defences", "coalesce(sum(s.defences),0)", "defences", "%.0f", 1},
	{"Spearhead", "Most attacks", "coalesce(sum(s.attacks),0)", "attacks", "%.0f", 1},
	{"Iron Lung", "Most rounds played", "count(distinct s.round_id)", "rounds", "%.0f", 1},
	{"Cannon Fodder", "Most deaths", "coalesce(sum(s.deaths),0)", "deaths", "%.0f", 1},
	{"Friendly Fire", "Most teamkills", "coalesce(sum(s.teamkills),0)", "teamkills", "%.0f", 1},
}

var roundRecords = []struct {
	Title  string
	Column string
}{
	{"Most kills in one round", "kills"},
	{"Highest score in one round", "score"},
	{"Most captures in one round", "captures"},
	{"Most heals in one round", "heals"},
	{"Most repairs in one round", "repairs"},
	{"Most deaths in one round", "deaths"},
}

func (s *apiServer) awardsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	period := strings.TrimSpace(q.Get("period"))
	if !leaderboardPeriodValid(period) {
		period = "all"
	}
	botFilter := normalizeBotFilter(q.Get("bots"))

	var serverOptions []serverOption
	if s.db != nil {
		optCtx, optCancel := context.WithTimeout(r.Context(), 5*time.Second)
		serverOptions, _ = s.fetchServerOptions(optCtx)
		optCancel()
	}
	serverFilter := normalizeServerFilter(q.Get("server"), serverOptions)

	data := struct {
		GeneratedAt   time.Time
		DBError       string
		PeriodFilters []filterLink
		BotFilters    []filterLink
		ServerFilters []filterLink
		Panels        []awardPanel
		VehicleAce    awardPanel
		Records       []roundRecordRow
	}{GeneratedAt: time.Now().UTC()}

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
		set("period", period, "all")
		set("bots", botFilter, "all")
		set("server", serverFilter, "")
		if enc := v.Encode(); enc != "" {
			return "/awards?" + enc
		}
		return "/awards"
	}
	for _, p := range leaderboardPeriods {
		data.PeriodFilters = append(data.PeriodFilters, filterLink{p.Label, link("period", p.Key), p.Key == period})
	}
	for _, b := range []struct{ Key, Label string }{{"all", "Everyone"}, {"humans", "Humans"}, {"bots", "Bots"}} {
		data.BotFilters = append(data.BotFilters, filterLink{b.Label, link("bots", b.Key), b.Key == botFilter})
	}
	data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)

	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		// This page runs 11 aggregate-award queries plus a vehicle-ace and
		// round-records pass, all site-wide (no map filter); on a busy
		// install that easily exceeds a tight budget, so it gets the same
		// wider margin as /map and /kits.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		setError := func(err error) {
			if err != nil && data.DBError == "" {
				data.DBError = dbErrorMessage(err)
			}
		}

		since := awardsSince(period)
		// award_totals_mv / vehicle_ace_totals_mv (migration 0010) cover
		// exactly the default view — no period/bot/server filter — where
		// they're refreshed after every ingest instead of re-aggregated on
		// every page load.
		isDefault := period == "all" && botFilter == "all" && serverFilter == ""
		for _, award := range aggregateAwards {
			var winners []awardWinner
			var err error
			var mvOK bool
			if isDefault {
				winners, mvOK, err = s.fetchAggregateAwardFromMV(ctx, award.MVExpr, award.Format, award.MinRounds)
			}
			if !mvOK {
				winners, err = s.fetchAggregateAward(ctx, award.Value, award.Format, award.MinRounds, since, botFilter, serverFilter)
			}
			setError(err)
			data.Panels = append(data.Panels, awardPanel{Title: award.Title, Blurb: award.Blurb, Winners: winners})
		}

		var vehicleWinners []awardWinner
		var vehicleMVOK bool
		var err error
		if isDefault {
			vehicleWinners, vehicleMVOK, err = s.fetchVehicleAceAwardFromMV(ctx)
		}
		if !vehicleMVOK {
			vehicleWinners, err = s.fetchVehicleAceAward(ctx, since, botFilter, serverFilter)
		}
		setError(err)
		data.VehicleAce = awardPanel{Title: "Vehicle Ace", Blurb: "Most kills from vehicles and mounted guns", Winners: vehicleWinners}

		for _, record := range roundRecords {
			row, err := s.fetchRoundRecord(ctx, record.Title, record.Column, since, botFilter, serverFilter)
			setError(err)
			if row != nil {
				data.Records = append(data.Records, *row)
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := awardsTemplate.Execute(w, data); err != nil {
		log.Printf("render awards failed: %v", err)
	}
}

func awardsSince(period string) interface{} {
	for _, p := range leaderboardPeriods {
		if p.Key == period && p.Since > 0 {
			return time.Now().UTC().Add(-p.Since)
		}
	}
	return nil
}

// awardBotCondition filters round rows by player kind. Rows from logs without
// roundstats have no is_bot flag; they count as human, matching the site's
// other pages.
const awardBotCondition = `(
	$2 = 'all'
	or ($2 = 'bots' and s.is_bot is true)
	or ($2 = 'humans' and coalesce(s.is_bot, false) is false)
)`

func (s *apiServer) fetchAggregateAward(ctx context.Context, valueExpr string, format string, minRounds int, since interface{}, botFilter string, serverFilter string) ([]awardWinner, error) {
	query := fmt.Sprintf(`
		select p.id::text, p.display_name, %s::float8 as value, count(distinct s.round_id)::int as rounds
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		join players p on p.id = s.player_id and not p.hidden
		where ($1::timestamptz is null or coalesce(r.ended_at, g.started_at) >= $1)
		  and ($4 = '' or g.server_id = nullif($4,'')::uuid)
		  and %s
		group by p.id, p.display_name
		having count(distinct s.round_id) >= $3 and %s > 0
		order by value desc, p.display_name asc
		limit 3
	`, valueExpr, awardBotCondition, valueExpr)
	rows, err := s.db.QueryContext(ctx, query, since, botFilter, minRounds, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var winners []awardWinner
	for rows.Next() {
		var winner awardWinner
		var value float64
		if err := rows.Scan(&winner.ID, &winner.DisplayName, &value, &winner.Rounds); err != nil {
			return nil, err
		}
		winner.Value = fmt.Sprintf(format, value)
		winners = append(winners, winner)
	}
	return winners, rows.Err()
}

func (s *apiServer) fetchVehicleAceAward(ctx context.Context, since interface{}, botFilter string, serverFilter string) ([]awardWinner, error) {
	rows, err := s.db.QueryContext(ctx, `
		select p.id::text, p.display_name, count(*)::int as value
		from events e
		join round_player_stats s on s.round_id = e.round_id and s.slot_id = e.actor_slot_id
		join rounds r on r.id = e.round_id
		join games g on g.id = r.game_id
		join players p on p.id = s.player_id and not p.hidden
		where e.event_type = 'score'
		  and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
		  and e.vehicle_code is not null and e.vehicle_code not like '%Soldier'
		  and ($1::timestamptz is null or coalesce(r.ended_at, g.started_at) >= $1)
		  and ($3 = '' or g.server_id = nullif($3,'')::uuid)
		  and `+awardBotCondition+`
		group by p.id, p.display_name
		order by value desc, p.display_name asc
		limit 3
	`, since, botFilter, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var winners []awardWinner
	for rows.Next() {
		var winner awardWinner
		var value int
		if err := rows.Scan(&winner.ID, &winner.DisplayName, &value); err != nil {
			return nil, err
		}
		winner.Value = fmt.Sprintf("%d", value)
		winners = append(winners, winner)
	}
	return winners, rows.Err()
}

// fetchAggregateAwardFromMV / fetchVehicleAceAwardFromMV are the fast paths
// for the default /awards view: award_totals_mv / vehicle_ace_totals_mv
// (migration 0010) precompute exactly these aggregations. ok is false
// (falls back to the live query) on an older install that hasn't applied
// that migration yet.
func (s *apiServer) fetchAggregateAwardFromMV(ctx context.Context, mvExpr string, format string, minRounds int) ([]awardWinner, bool, error) {
	query := fmt.Sprintf(`
		select player_id::text, display_name, (%s)::float8 as value, rounds
		from award_totals_mv
		where rounds >= $1 and (%s) > 0
		order by value desc, display_name asc
		limit 3
	`, mvExpr, mvExpr)
	rows, err := s.db.QueryContext(ctx, query, minRounds)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	var winners []awardWinner
	for rows.Next() {
		var winner awardWinner
		var value float64
		if err := rows.Scan(&winner.ID, &winner.DisplayName, &value, &winner.Rounds); err != nil {
			return nil, true, err
		}
		winner.Value = fmt.Sprintf(format, value)
		winners = append(winners, winner)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return winners, true, nil
}

func (s *apiServer) fetchVehicleAceAwardFromMV(ctx context.Context) ([]awardWinner, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select player_id::text, display_name, value
		from vehicle_ace_totals_mv
		order by value desc, display_name asc
		limit 3
	`)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	var winners []awardWinner
	for rows.Next() {
		var winner awardWinner
		var value int
		if err := rows.Scan(&winner.ID, &winner.DisplayName, &value); err != nil {
			return nil, true, err
		}
		winner.Value = fmt.Sprintf("%d", value)
		winners = append(winners, winner)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return winners, true, nil
}

func (s *apiServer) fetchRoundRecord(ctx context.Context, title string, column string, since interface{}, botFilter string, serverFilter string) (*roundRecordRow, error) {
	query := fmt.Sprintf(`
		select p.id::text, p.display_name, s.%s::int,
			coalesce(nullif(g.map_name, ''), 'unknown'),
			to_char(coalesce(r.ended_at, g.started_at), 'YYYY-MM-DD')
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		join players p on p.id = s.player_id and not p.hidden
		where ($1::timestamptz is null or coalesce(r.ended_at, g.started_at) >= $1)
		  and ($3 = '' or g.server_id = nullif($3,'')::uuid)
		  and %s
		  and s.%s > 0
		order by s.%s desc, coalesce(r.ended_at, g.started_at) asc
		limit 1
	`, column, awardBotCondition, column, column)
	row := roundRecordRow{Title: title}
	err := s.db.QueryRowContext(ctx, query, since, botFilter, serverFilter).
		Scan(&row.ID, &row.DisplayName, &row.Value, &row.MapName, &row.When)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

var awardsTemplate = template.Must(template.New("awards").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Awards - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "awards" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Awards</strong><span class="muted">medal standings across all recorded battles</span></div>
<div class="filters">{{ range .PeriodFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>
<div class="filters">{{ range .BotFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}

<h2>Career Medals</h2>
<div class="grid2">
{{ range .Panels }}{{ template "awardPanel" . }}{{ end }}
{{ template "awardPanel" .VehicleAce }}
</div>

<h2>Round Records</h2>
<table><thead><tr><th>Record</th><th>Player</th><th>Value</th><th>Map</th><th>Date</th></tr></thead><tbody>
{{ range .Records }}<tr><td>{{ .Title }}</td><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Value }}</td><td><a class="link" href="{{ mapURL .MapName }}">{{ displayCode .MapName }}</a></td><td class="mono">{{ .When }}</td></tr>
{{ else }}<tr><td colspan="5" class="muted">No round records for these filters.</td></tr>{{ end }}
</tbody></table>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>

{{ define "awardPanel" }}<section class="panel"><h3>{{ .Title }}</h3><div class="award-blurb">{{ .Blurb }}</div>
<div class="award-list">
{{ range $i, $w := .Winners }}<div class="award-row"><span class="medal {{ if eq $i 0 }}g{{ else if eq $i 1 }}s{{ else }}b{{ end }}">{{ rank $i }}</span><a class="link who" href="{{ playerURL $w.ID "all" }}">{{ $w.DisplayName }}</a><span class="val">{{ $w.Value }}</span></div>
{{ else }}<div class="muted">No qualifying players.</div>{{ end }}
</div></section>{{ end }}`))
