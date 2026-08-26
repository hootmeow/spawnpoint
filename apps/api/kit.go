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

type kitPageData struct {
	GeneratedAt      time.Time
	DBError          string
	Code             string
	ServerFilter     string
	ServerFilters    []filterLink
	Pickups          int
	Users            int
	Rounds           int
	Maps             int
	CarrierKills     int
	CarrierDeaths    int
	CarrierTeamkills int
	Carriers         []codePlayerRow
	MapRows          []codeMapRow
	CarrierBars      template.HTML
	MapBars          template.HTML
	PickupsPerDay    template.HTML
}

func (s *apiServer) kitPage(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	data := kitPageData{GeneratedAt: time.Now().UTC(), Code: code}
	switch {
	case s.db == nil:
		data.DBError = "DATABASE_URL is not configured"
	case code == "":
		data.DBError = "missing ?code= parameter"
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := func(_, value string) string {
			v := url.Values{}
			v.Set("code", code)
			if value != "" {
				v.Set("server", value)
			}
			return "/kit?" + v.Encode()
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		if err := s.fillKitPage(ctx, &data, serverFilter); err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := kitTemplate.Execute(w, data); err != nil {
		log.Printf("render kit page failed: %v", err)
	}
}

const kitCarriersQuery = `
	with pickups as (
		select e.round_id, coalesce(e.actor_player_id, st.player_id) as player_id
		from events e
		left join round_player_stats st on st.round_id = e.round_id and st.slot_id = e.actor_slot_id
		where e.event_type = 'pickup_kit' and e.kit_code = $1
		  and ($3 = '' or e.server_id = nullif($3,'')::uuid)
	)
	select p.id::text, p.display_name, count(*)::int, count(distinct pi.round_id)::int
	from pickups pi join players p on p.id = pi.player_id and not p.hidden
	group by p.id, p.display_name
	order by count(*) desc, p.display_name asc
	limit $2`

const kitMapsQuery = `
	select coalesce(g.map_name, 'unknown'), count(*)::int
	from events e join games g on g.id = e.game_id
	where e.event_type = 'pickup_kit' and e.kit_code = $1
	  and ($3 = '' or e.server_id = nullif($3,'')::uuid)
	group by 1 order by 2 desc limit $2`

func (s *apiServer) fillKitPage(ctx context.Context, data *kitPageData, serverFilter string) error {
	err := s.db.QueryRowContext(ctx, `
		with pickups as (
			select e.kit_code, e.round_id, e.game_id, coalesce(e.actor_player_id, st.player_id) as player_id
			from events e
			left join round_player_stats st on st.round_id = e.round_id and st.slot_id = e.actor_slot_id
			where e.event_type = 'pickup_kit' and e.kit_code = $1
			  and ($2 = '' or e.server_id = nullif($2,'')::uuid)
		),
		carriers as (
			select distinct round_id, player_id from pickups where player_id is not null
		),
		carrier_stats as (
			select
				coalesce(sum(s.kills), 0)::int as kills,
				coalesce(sum(s.deaths), 0)::int as deaths,
				coalesce(sum(s.teamkills), 0)::int as teamkills
			from carriers c
			join round_player_stats s on s.round_id = c.round_id and s.player_id = c.player_id
		)
		select
			count(*)::int,
			count(distinct p.player_id)::int,
			count(distinct p.round_id)::int,
			count(distinct coalesce(g.map_name, 'unknown'))::int,
			(select kills from carrier_stats),
			(select deaths from carrier_stats),
			(select teamkills from carrier_stats)
		from pickups p
		left join games g on g.id = p.game_id
	`, data.Code, serverFilter).Scan(&data.Pickups, &data.Users, &data.Rounds, &data.Maps, &data.CarrierKills, &data.CarrierDeaths, &data.CarrierTeamkills)
	if err != nil {
		return err
	}

	data.Carriers, err = s.fetchCodePlayersServer(ctx, kitCarriersQuery, data.Code, 15, serverFilter)
	if err != nil {
		return err
	}
	data.CarrierBars = codePlayerBars(data.Carriers, colorScore)

	data.MapRows, err = s.fetchCodeMapsServer(ctx, kitMapsQuery, data.Code, 12, serverFilter)
	if err != nil {
		return err
	}
	data.MapBars = codeMapBars(data.MapRows, colorInfo)

	rows, err := s.db.QueryContext(ctx, `
		select to_char(date_trunc('day', coalesce(e.occurred_at, g.started_at)), 'MM-DD'), count(*)::int
		from events e
		left join games g on g.id = e.game_id
		where e.event_type = 'pickup_kit' and e.kit_code = $1
		  and coalesce(e.occurred_at, g.started_at) is not null
		  and ($2 = '' or e.server_id = nullif($2,'')::uuid)
		group by date_trunc('day', coalesce(e.occurred_at, g.started_at))
		order by date_trunc('day', coalesce(e.occurred_at, g.started_at))
		limit 60
	`, data.Code, serverFilter)
	if err != nil {
		return err
	}
	defer rows.Close()
	var perDay []barItem
	for rows.Next() {
		var item barItem
		if err := rows.Scan(&item.Label, &item.Value); err != nil {
			return err
		}
		perDay = append(perDay, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	data.PickupsPerDay = columnChartSVG(perDay, colorScore, "Kit pickups per day")
	return nil
}

var kitTemplate = template.Must(template.New("kit").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{ displayCode .Code }} Kit - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "kits" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>{{ displayCode .Code }}</strong><span class="muted">kit</span></div>
<div class="section-tabs"><a href="/kits{{ if .ServerFilter }}?server={{ query .ServerFilter }}{{ end }}">&larr; All Kits</a></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="kpi-grid">
<div class="kpi-card green"><span>Pickups</span><strong>{{ .Pickups }}</strong></div>
<div class="kpi-card"><span>Unique Carriers</span><strong>{{ .Users }}</strong></div>
<div class="kpi-card"><span>Rounds Seen</span><strong>{{ .Rounds }}</strong></div>
<div class="kpi-card"><span>Maps Seen</span><strong>{{ .Maps }}</strong></div>
<div class="kpi-card gold"><span>Carrier Kills</span><strong>{{ .CarrierKills }}</strong></div>
<div class="kpi-card red"><span>Carrier Deaths</span><strong>{{ .CarrierDeaths }}</strong></div>
<div class="kpi-card"><span>Carrier K/D</span><strong>{{ formatKD .CarrierKills .CarrierDeaths }}</strong></div>
<div class="kpi-card"><span>Carrier TK Rate</span><strong>{{ printf "%.1f" (tkRate .CarrierTeamkills .CarrierKills) }}%</strong></div>
</div>
<div class="grid2">
<section class="panel"><h3>Pickups per Day</h3><div class="chart">{{ .PickupsPerDay }}</div></section>
<section class="panel"><h3>Pickup Maps</h3>{{ .MapBars }}</section>
</div>
<div class="grid2">
<section class="panel"><h3>Top Carriers</h3><table><thead><tr><th>Player</th><th>Pickups</th><th>Rounds</th></tr></thead><tbody>
{{ range .Carriers }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Count }}</td><td>{{ .Extra }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No carriers recorded.</td></tr>{{ end }}
</tbody></table></section>
<section class="panel"><h3>Carrier Demand</h3>{{ .CarrierBars }}</section>
</div>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
