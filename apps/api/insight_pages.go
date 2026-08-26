package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const mapsTileLimit = 8

type mapsPageData struct {
	GeneratedAt   time.Time
	DBError       string
	ServerFilter  string
	ServerFilters []filterLink
	Maps          []mapMetaRow
	TopMaps       []mapMetaRow
}

type mapMetaRow struct {
	Name        string
	Rounds      int
	Team1Wins   int
	Team2Wins   int
	Kills       int
	AvgDuration int
	Players     int
}

type mapPageData struct {
	GeneratedAt   time.Time
	DBError       string
	Name          string
	BotFilter     string
	Window        string
	ServerFilter  string
	ServerFilters []filterLink
	Summary       *mapMetaRow
	Players       []codePlayerRow
	Weapons       []codeMapRow
	Vehicles      []codeMapRow
	Teams         []codeMapRow
	Modes         []codeMapRow
	Events        []codeMapRow
	Extremes      []keyValueRow
	Recent        []roundRow
	RecentBars    template.HTML
	PlayerBars    template.HTML
	WeaponBars    template.HTML
	VehicleBars   template.HTML
	KillHeat      template.HTML
	DeathHeat     template.HTML
}

type kitMetricRow struct {
	Code                string
	Pickups             int
	Users               int
	Rounds              int
	Maps                int
	KillsByCarriers     int
	DeathsByCarriers    int
	TeamkillsByCarriers int
	PickupsPerUser      float64
	PickupsPerRound     float64
	TKRate              float64
}

type kitsPageData struct {
	GeneratedAt   time.Time
	DBError       string
	ServerFilter  string
	ServerFilters []filterLink
	Kits          []kitMetricRow
	DemandBars    template.HTML
	UserBars      template.HTML
	MapBars       template.HTML
	TopUsers      []codePlayerRow
	TopMaps       []codeMapRow
}

type rivalryPageData struct {
	GeneratedAt time.Time
	DBError     string
	A           string
	B           string
	AName       string
	BName       string
	AKills      int
	BKills      int
	Weapons     []codeMapRow
	Maps        []codeMapRow
	Trend       []seriesPoint
	Events      []playerEventRow
}

func (s *apiServer) mapsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data := mapsPageData{GeneratedAt: time.Now().UTC()}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := func(_, value string) string {
			if value == "" {
				return "/maps"
			}
			return "/maps?server=" + queryEscape(value)
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		rows, err := s.fetchMapMeta(ctx, "", serverFilter)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
		data.Maps = rows
		if len(rows) > mapsTileLimit {
			data.TopMaps = rows[:mapsTileLimit]
		} else {
			data.TopMaps = rows
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := mapsTemplate.Execute(w, data); err != nil {
		log.Printf("render maps page failed: %v", err)
	}
}

func (s *apiServer) mapPage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Redirect(w, r, "/maps", http.StatusFound)
		return
	}
	botFilter := normalizeBotFilter(r.URL.Query().Get("bots"))
	window := normalizeWindowFilter(r.URL.Query().Get("window"))
	// This page runs ~11 sequential queries against a busy map (hundreds of
	// thousands of matching events); on modest hardware a filtered view can
	// approach 10s even with the map-query indexes in migration 0006, so the
	// budget is more generous than other pages to avoid spurious
	// "canceling statement due to user request" errors.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	data := mapPageData{GeneratedAt: time.Now().UTC(), Name: name, BotFilter: botFilter, Window: window}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := mapTemplate.Execute(w, data); err != nil {
			log.Printf("render map page failed: %v", err)
		}
		return
	}
	setError := func(err error) {
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}
	}
	serverOptions, _ := s.fetchServerOptions(ctx)
	serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
	data.ServerFilter = serverFilter
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
		set("name", name, "")
		set("bots", botFilter, "all")
		set("window", window, "all")
		set("server", serverFilter, "")
		return "/map?" + v.Encode()
	}
	data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
	meta, err := s.fetchMapMeta(ctx, name, serverFilter)
	setError(err)
	if len(meta) > 0 {
		data.Summary = &meta[0]
	}
	data.Players, err = s.fetchMapTopPlayersFiltered(ctx, name, 15, botFilter, window, serverFilter)
	setError(err)

	// The default view (no filters, no server split — the overwhelmingly
	// common case, since every map link on the site points here) reads
	// vehicles/events/heatmaps from migration 0007's map_summary_mv instead
	// of re-scanning `events` on every request. Any filter or server split
	// falls back to the live, indexed (migration 0006) queries.
	isDefault := botFilter == "all" && window == "all" && serverFilter == ""
	var mvOK bool
	if isDefault {
		var mvVehicles, mvEvents []codeMapRow
		var mvKillPoints, mvDeathPoints []worldPoint
		mvVehicles, mvEvents, mvKillPoints, mvDeathPoints, mvOK, err = s.fetchMapSummaryMV(ctx, name)
		setError(err)
		if mvOK {
			data.Vehicles = mvVehicles
			data.Events = mvEvents
			data.KillHeat = heatmapSVG(mvKillPoints, colorKills)
			data.DeathHeat = heatmapSVG(mvDeathPoints, colorDeaths)
		}
	}

	if isDefault {
		data.Weapons, err = s.fetchMapCodeMaps(ctx, mapTopWeaponsQuery, name, 15, serverFilter)
	} else {
		data.Weapons, err = s.fetchMapCodeMapsFiltered(ctx, mapTopWeaponsFilteredQuery, name, 15, botFilter, window, serverFilter)
	}
	setError(err)
	if !mvOK {
		if isDefault {
			data.Vehicles, err = s.fetchMapCodeMaps(ctx, mapTopVehiclesQuery, name, 15, serverFilter)
		} else {
			data.Vehicles, err = s.fetchMapCodeMapsFiltered(ctx, mapTopVehiclesFilteredQuery, name, 15, botFilter, window, serverFilter)
		}
		setError(err)
	}
	data.Teams, err = s.fetchMapCodeMaps(ctx, mapTeamPressureQuery, name, 6, serverFilter)
	setError(err)
	data.Modes, err = s.fetchMapCodeMaps(ctx, mapModeSplitQuery, name, 8, serverFilter)
	setError(err)
	if !mvOK {
		if isDefault {
			data.Events, err = s.fetchMapCodeMaps(ctx, mapEventMixQuery, name, 12, serverFilter)
		} else {
			data.Events, err = s.fetchMapCodeMapsFiltered(ctx, mapEventMixFilteredQuery, name, 12, botFilter, window, serverFilter)
		}
		setError(err)
	}
	data.Extremes, err = s.fetchMapExtremes(ctx, name, serverFilter)
	setError(err)
	data.Recent, err = s.fetchMapRoundsFiltered(ctx, name, 20, botFilter, window, serverFilter)
	setError(err)
	if !mvOK {
		killPoints, err := s.fetchMapPositions(ctx, name, []string{"kill", "kills"}, serverFilter)
		setError(err)
		data.KillHeat = heatmapSVG(killPoints, colorKills)
		deathPoints, err := s.fetchMapPositions(ctx, name, []string{"death", "deathnomsg"}, serverFilter)
		setError(err)
		data.DeathHeat = heatmapSVG(deathPoints, colorDeaths)
	}
	data.RecentBars = roundKillBars(data.Recent)
	data.PlayerBars = codePlayerBars(data.Players, colorKills)
	data.WeaponBars = codeMapBars(data.Weapons, colorKills)
	data.VehicleBars = codeMapBars(data.Vehicles, colorInfo)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := mapTemplate.Execute(w, data); err != nil {
		log.Printf("render map page failed: %v", err)
	}
}

func (s *apiServer) rivalryPage(w http.ResponseWriter, r *http.Request) {
	a := sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("a")))
	b := sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("b")))
	if a == "" || b == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if s.db == nil {
		data := rivalryPageData{GeneratedAt: time.Now().UTC(), A: a, B: b, DBError: "DATABASE_URL is not configured"}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := rivalryTemplate.Execute(w, data); err != nil {
			log.Printf("render rivalry page failed: %v", err)
		}
		return
	}
	// A hidden player must not be reachable via the rivalry page just because
	// their raw id is known; substitute nilUUID the same way resolvePlayerIDs
	// does elsewhere so the page renders as "not found" instead of leaking
	// the name and stats.
	if ids, err := s.resolvePlayerIDs(ctx, a, "", 0); err == nil && len(ids) == 1 {
		a = ids[0]
	}
	if ids, err := s.resolvePlayerIDs(ctx, b, "", 0); err == nil && len(ids) == 1 {
		b = ids[0]
	}
	data := rivalryPageData{GeneratedAt: time.Now().UTC(), A: a, B: b}
	setError := func(err error) {
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}
	}
	setError(s.fetchRivalrySummary(ctx, &data))
	var err error
	data.Weapons, err = s.fetchCodeMaps(ctx, rivalryWeaponsQuery, a+","+b, 12)
	setError(err)
	data.Maps, err = s.fetchCodeMaps(ctx, rivalryMapsQuery, a+","+b, 12)
	setError(err)
	data.Trend, err = s.fetchRivalryTrend(ctx, a+","+b, 24)
	setError(err)
	data.Events, err = s.fetchRivalryEvents(ctx, a, b, 80)
	setError(err)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := rivalryTemplate.Execute(w, data); err != nil {
		log.Printf("render rivalry page failed: %v", err)
	}
}

func (s *apiServer) kitsPage(w http.ResponseWriter, r *http.Request) {
	// fetchKitMetrics aggregates every pickup_kit event site-wide (no map
	// filter) joined back to round_player_stats; on a busy install that's a
	// multi-second query even after indexing, so this page gets the same
	// wider budget as /map instead of racing a tight timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data := kitsPageData{GeneratedAt: time.Now().UTC()}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := func(_, value string) string {
			if value == "" {
				return "/kits"
			}
			return "/kits?server=" + queryEscape(value)
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		setError := func(err error) {
			if err != nil && data.DBError == "" {
				data.DBError = dbErrorMessage(err)
			}
		}
		var err error
		data.Kits, err = s.fetchKitMetrics(ctx, 50, serverFilter)
		setError(err)
		if fromMV, ok, mvErr := s.fetchKitTopUsersFromMV(ctx, 15); serverFilter == "" && ok {
			data.TopUsers, err = fromMV, mvErr
		} else {
			data.TopUsers, err = s.fetchCodePlayersServer(ctx, kitTopUsersQuery, "", 15, serverFilter)
		}
		setError(err)
		if fromMV, ok, mvErr := s.fetchKitTopMapsFromMV(ctx, 15); serverFilter == "" && ok {
			data.TopMaps, err = fromMV, mvErr
		} else {
			data.TopMaps, err = s.fetchCodeMapsServer(ctx, kitTopMapsQuery, "", 15, serverFilter)
		}
		setError(err)
		data.DemandBars = kitMetricBars(data.Kits, "pickups", colorScore)
		data.UserBars = kitMetricBars(data.Kits, "users", colorInfo)
		data.MapBars = codeMapBars(data.TopMaps, colorKills)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := kitsTemplate.Execute(w, data); err != nil {
		log.Printf("render kits page failed: %v", err)
	}
}

func (s *apiServer) supportPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data := struct {
		GeneratedAt   time.Time
		DBError       string
		ServerFilters []filterLink
		Rows          []codePlayerRow
	}{GeneratedAt: time.Now().UTC()}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		link := func(_, value string) string {
			if value == "" {
				return "/support"
			}
			return "/support?server=" + queryEscape(value)
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		rows, err := s.fetchCodePlayersServer(ctx, supportLeadersQuery, "", 50, serverFilter)
		data.Rows = rows
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := supportTemplate.Execute(w, data); err != nil {
		log.Printf("render support page failed: %v", err)
	}
}

func normalizeWindowFilter(value string) string {
	switch value {
	case "24h", "7d":
		return value
	default:
		return "all"
	}
}

type mvCodeCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// fetchMapSummaryMV reads migration 0007's map_summary_mv for the default
// (all bots, all time, all servers) /map view. ok is false whenever the row
// isn't there yet — no games ingested for this map, the view hasn't
// finished its first refresh, or an older install hasn't applied migration
// 0007 — and the caller should fall back to the live queries.
func (s *apiServer) fetchMapSummaryMV(ctx context.Context, name string) (vehicles []codeMapRow, events []codeMapRow, killPoints []worldPoint, deathPoints []worldPoint, ok bool, err error) {
	var vehiclesJSON, eventsJSON, killJSON, deathJSON []byte
	err = s.db.QueryRowContext(ctx, `
		select vehicles, events, kill_points, death_points from map_summary_mv where map_name = $1
	`, name).Scan(&vehiclesJSON, &eventsJSON, &killJSON, &deathJSON)
	if err != nil {
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "does not exist") {
			return nil, nil, nil, nil, false, nil
		}
		return nil, nil, nil, nil, false, err
	}
	var vItems, eItems []mvCodeCount
	if err = json.Unmarshal(vehiclesJSON, &vItems); err != nil {
		return nil, nil, nil, nil, false, err
	}
	if err = json.Unmarshal(eventsJSON, &eItems); err != nil {
		return nil, nil, nil, nil, false, err
	}
	for _, it := range vItems {
		vehicles = append(vehicles, codeMapRow{Name: it.Name, Count: it.Count})
	}
	for _, it := range eItems {
		events = append(events, codeMapRow{Name: it.Name, Count: it.Count})
	}
	var killLocs, deathLocs []string
	if err = json.Unmarshal(killJSON, &killLocs); err != nil {
		return nil, nil, nil, nil, false, err
	}
	if err = json.Unmarshal(deathJSON, &deathLocs); err != nil {
		return nil, nil, nil, nil, false, err
	}
	for _, loc := range killLocs {
		if p, ok := parseWorldPoint(loc); ok {
			killPoints = append(killPoints, p)
		}
	}
	for _, loc := range deathLocs {
		if p, ok := parseWorldPoint(loc); ok {
			deathPoints = append(deathPoints, p)
		}
	}
	return vehicles, events, killPoints, deathPoints, true, nil
}

func mapFilterURL(name string, botFilter string, window string, serverFilter string) string {
	values := url.Values{}
	values.Set("name", name)
	values.Set("bots", normalizeBotFilter(botFilter))
	values.Set("window", normalizeWindowFilter(window))
	if serverFilter != "" {
		values.Set("server", serverFilter)
	}
	return "/map?" + values.Encode()
}

func (s *apiServer) fetchMapMeta(ctx context.Context, name string, serverFilter string) ([]mapMetaRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(nullif(g.map_name, ''), 'unknown'),
			count(distinct r.id)::int,
			count(distinct r.id) filter (where r.winning_team_id = 1)::int,
			count(distinct r.id) filter (where r.winning_team_id = 2)::int,
			coalesce(sum(s.kills), 0)::int,
			coalesce(avg(extract(epoch from (r.ended_at - g.started_at))), 0)::int,
			count(distinct s.player_id)::int
		from games g
		join rounds r on r.game_id = g.id
		left join round_player_stats s on s.round_id = r.id
		where ($1 = '' or g.map_name = $1)
		  and ($2 = '' or g.server_id = nullif($2,'')::uuid)
		group by 1
		order by count(distinct r.id) desc, 1 asc
	`, name, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []mapMetaRow
	for rows.Next() {
		var row mapMetaRow
		if err := rows.Scan(&row.Name, &row.Rounds, &row.Team1Wins, &row.Team2Wins, &row.Kills, &row.AvgDuration, &row.Players); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchMapRounds(ctx context.Context, name string, limit int) ([]roundRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), ''), r.id::text, coalesce(g.map_name, ''), coalesce(g.game_mode, ''), r.round_index,
			(select count(*) from events e where e.round_id = r.id)::int,
			(select count(distinct player_id) from round_player_stats s where s.round_id = r.id)::int,
			(select coalesce(sum(kills), 0) from round_player_stats s where s.round_id = r.id)::int,
			r.winning_team_id,
			coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::int,
			coalesce(r.end_tickets::text, '{}'),
			to_char(g.started_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'),
			coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '')
		from rounds r
		join games g on g.id = r.game_id
		left join raw_log_files raw on raw.id = g.raw_log_file_id
		where g.map_name = $1
		order by g.started_at desc
		limit $2
	`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []roundRow
	for rows.Next() {
		var row roundRow
		if err := rows.Scan(&row.RoundHash, &row.ID, &row.MapName, &row.GameMode, &row.RoundIndex, &row.EventCount, &row.PlayerCount, &row.Kills, &row.WinningTeamID, &row.Duration, &row.EndTickets, &row.StartedAt, &row.EndedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchMapRoundsFiltered(ctx context.Context, name string, limit int, botFilter string, window string, serverFilter string) ([]roundRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), ''), r.id::text, coalesce(g.map_name, ''), coalesce(g.game_mode, ''), r.round_index,
			(select count(*) from events e where e.round_id = r.id)::int,
			(select count(distinct player_id) from round_player_stats s where s.round_id = r.id and ($3 = 'all' or ($3 = 'bots' and s.is_bot is true) or ($3 = 'humans' and coalesce(s.is_bot, false) is false)))::int,
			(select coalesce(sum(kills), 0) from round_player_stats s where s.round_id = r.id and ($3 = 'all' or ($3 = 'bots' and s.is_bot is true) or ($3 = 'humans' and coalesce(s.is_bot, false) is false)))::int,
			r.winning_team_id,
			coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::int,
			coalesce(r.end_tickets::text, '{}'),
			to_char(g.started_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'),
			coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '')
		from rounds r
		join games g on g.id = r.game_id
		left join raw_log_files raw on raw.id = g.raw_log_file_id
		where g.map_name = $1
		  and ($4 = 'all' or ($4 = '24h' and coalesce(r.ended_at, g.started_at) >= now() - interval '24 hours') or ($4 = '7d' and coalesce(r.ended_at, g.started_at) >= now() - interval '7 days'))
		  and ($5 = '' or g.server_id = nullif($5,'')::uuid)
		order by g.started_at desc
		limit $2
	`, name, limit, botFilter, window, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []roundRow
	for rows.Next() {
		var row roundRow
		if err := rows.Scan(&row.RoundHash, &row.ID, &row.MapName, &row.GameMode, &row.RoundIndex, &row.EventCount, &row.PlayerCount, &row.Kills, &row.WinningTeamID, &row.Duration, &row.EndTickets, &row.StartedAt, &row.EndedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchMapTopPlayersFiltered(ctx context.Context, name string, limit int, botFilter string, window string, serverFilter string) ([]codePlayerRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select p.id::text, p.display_name, coalesce(sum(s.kills), 0)::int, coalesce(sum(s.score), 0)::int
		from round_player_stats s
		join rounds r on r.id=s.round_id
		join games g on g.id=r.game_id
		join players p on p.id=s.player_id and not p.hidden
		where g.map_name = $1
		  and ($3 = 'all' or ($3 = 'bots' and s.is_bot is true) or ($3 = 'humans' and coalesce(s.is_bot, false) is false))
		  and ($4 = 'all' or ($4 = '24h' and coalesce(r.ended_at, g.started_at) >= now() - interval '24 hours') or ($4 = '7d' and coalesce(r.ended_at, g.started_at) >= now() - interval '7 days'))
		  and ($5 = '' or g.server_id = nullif($5,'')::uuid)
		group by p.id, p.display_name
		order by coalesce(sum(s.kills), 0) desc, coalesce(sum(s.score), 0) desc
		limit $2
	`, name, limit, botFilter, window, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []codePlayerRow
	for rows.Next() {
		var row codePlayerRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Count, &row.Extra); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchMapCodeMaps(ctx context.Context, query string, name string, limit int, serverFilter string) ([]codeMapRow, error) {
	rows, err := s.db.QueryContext(ctx, query, name, limit, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []codeMapRow
	for rows.Next() {
		var row codeMapRow
		if err := rows.Scan(&row.Name, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchMapCodeMapsFiltered(ctx context.Context, query string, name string, limit int, botFilter string, window string, serverFilter string) ([]codeMapRow, error) {
	rows, err := s.db.QueryContext(ctx, query, name, limit, botFilter, window, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []codeMapRow
	for rows.Next() {
		var row codeMapRow
		if err := rows.Scan(&row.Name, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchRivalrySummary(ctx context.Context, data *rivalryPageData) error {
	return s.db.QueryRowContext(ctx, `
		with names as (
			select
				(select display_name from players where id = nullif($1,'')::uuid) as a_name,
				(select display_name from players where id = nullif($2,'')::uuid) as b_name
		),
		kills as (
			select
				count(*) filter (where coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $1 and coalesce(e.target_player_id::text, target_stats.player_id::text) = $2)::int as a_kills,
				count(*) filter (where coalesce(e.actor_player_id::text, actor_stats.player_id::text) = $2 and coalesce(e.target_player_id::text, target_stats.player_id::text) = $1)::int as b_kills
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
			where e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		)
		select coalesce(n.a_name, ''), coalesce(n.b_name, ''), k.a_kills, k.b_kills from names n, kills k
	`, data.A, data.B).Scan(&data.AName, &data.BName, &data.AKills, &data.BKills)
}

func (s *apiServer) fetchRivalryEvents(ctx context.Context, a string, b string, limit int) ([]playerEventRow, error) {
	rows, err := s.db.QueryContext(ctx, eventSelectSQL+`
		where (
			(coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = $2 and coalesce(e.target_player_id::text, target_slot_stats.player_id::text) = $3)
			or (coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = $3 and coalesce(e.target_player_id::text, target_slot_stats.player_id::text) = $2)
		)
		and e.event_type = 'score'
		order by coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) desc, e.sequence_no desc
		limit $1
	`, limit, a, b)
	if err != nil {
		return nil, err
	}
	return scanPlayerEvents(rows)
}

func (s *apiServer) fetchMapExtremes(ctx context.Context, name string, serverFilter string) ([]keyValueRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with round_metrics as (
			select
				coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), 'round') as label,
				coalesce(sum(s.kills), 0)::int as kills
			from rounds r
			join games g on g.id = r.game_id
			left join raw_log_files raw on raw.id = g.raw_log_file_id
			left join round_player_stats s on s.round_id = r.id
			where g.map_name = $1
			  and ($2 = '' or g.server_id = nullif($2,'')::uuid)
			group by r.id, raw.content_sha256, r.round_index, g.started_at
		)
		select 'Highest Kill Round', label, kills::text || ' kills' from round_metrics order by kills desc, label limit 1
	`, name, serverFilter)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	more, err := s.fetchMapExtremeRows(ctx, name, serverFilter)
	if err != nil {
		return nil, err
	}
	return append(result, more...), nil
}

func (s *apiServer) fetchMapExtremeRows(ctx context.Context, name string, serverFilter string) ([]keyValueRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with round_metrics as (
			select
				coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), 'round') as label,
				coalesce(sum(s.kills), 0)::int as kills,
				coalesce(count(distinct s.player_id), 0)::int as players,
				coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::int as duration,
				(select count(*) from events e where e.round_id = r.id)::int as events
			from rounds r
			join games g on g.id = r.game_id
			left join raw_log_files raw on raw.id = g.raw_log_file_id
			left join round_player_stats s on s.round_id = r.id
			where g.map_name = $1
			  and ($2 = '' or g.server_id = nullif($2,'')::uuid)
			group by r.id, raw.content_sha256, r.round_index, g.started_at
		),
		longest as (select 'Longest Round' as label, label as value, duration::text || ' seconds' as detail from round_metrics order by duration desc limit 1),
		busiest as (select 'Most Events' as label, label as value, events::text || ' events' as detail from round_metrics order by events desc limit 1),
		fullest as (select 'Most Players' as label, label as value, players::text || ' players' as detail from round_metrics order by players desc limit 1)
		select label, value, detail from longest
		union all select label, value, detail from busiest
		union all select label, value, detail from fullest
	`, name, serverFilter)
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

func (s *apiServer) fetchKitMetrics(ctx context.Context, limit int, serverFilter string) ([]kitMetricRow, error) {
	if serverFilter == "" {
		if rows, ok, err := s.fetchKitMetricsFromMV(ctx, limit); ok {
			return rows, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		with pickups as (
			select e.kit_code, e.round_id, e.game_id, coalesce(e.actor_player_id, actor_stats.player_id) as player_id
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			where e.event_type = 'pickup_kit' and e.kit_code is not null
			  and ($2 = '' or e.server_id = nullif($2,'')::uuid)
		),
		carriers as (
			select distinct kit_code, round_id, player_id from pickups where player_id is not null
		),
		stats as (
			select
				c.kit_code,
				coalesce(sum(s.kills), 0)::int as kills,
				coalesce(sum(s.deaths), 0)::int as deaths,
				coalesce(sum(s.teamkills), 0)::int as teamkills
			from carriers c
			join round_player_stats s on s.round_id = c.round_id and s.player_id = c.player_id
			group by c.kit_code
		)
		select
			p.kit_code,
			count(*)::int,
			count(distinct p.player_id)::int,
			count(distinct p.round_id)::int,
			count(distinct coalesce(g.map_name, 'unknown'))::int,
			coalesce(max(stats.kills), 0)::int,
			coalesce(max(stats.deaths), 0)::int,
			coalesce(max(stats.teamkills), 0)::int
		from pickups p
		left join games g on g.id = p.game_id
		left join stats on stats.kit_code = p.kit_code
		group by p.kit_code
		order by count(*) desc, p.kit_code asc
		limit $1
	`, limit, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []kitMetricRow
	for rows.Next() {
		var row kitMetricRow
		if err := rows.Scan(&row.Code, &row.Pickups, &row.Users, &row.Rounds, &row.Maps, &row.KillsByCarriers, &row.DeathsByCarriers, &row.TeamkillsByCarriers); err != nil {
			return nil, err
		}
		row.PickupsPerUser = ratio(row.Pickups, row.Users)
		row.PickupsPerRound = ratio(row.Pickups, row.Rounds)
		row.TKRate = tkRate(row.TeamkillsByCarriers, row.KillsByCarriers)
		result = append(result, row)
	}
	return result, rows.Err()
}

// fetchKitMetricsFromMV is the fast path for the unfiltered /kits page:
// kit_totals_mv (migration 0009) precomputes exactly this aggregation. ok is
// false (falls back to the live query) on an older install that hasn't
// applied that migration yet.
func (s *apiServer) fetchKitMetricsFromMV(ctx context.Context, limit int) ([]kitMetricRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select kit_code, pickups, users, rounds, maps, kills_by_carriers, deaths_by_carriers, teamkills_by_carriers
		from kit_totals_mv
		order by pickups desc, kit_code asc
		limit $1
	`, limit)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	var result []kitMetricRow
	for rows.Next() {
		var row kitMetricRow
		if err := rows.Scan(&row.Code, &row.Pickups, &row.Users, &row.Rounds, &row.Maps, &row.KillsByCarriers, &row.DeathsByCarriers, &row.TeamkillsByCarriers); err != nil {
			return nil, true, err
		}
		row.PickupsPerUser = ratio(row.Pickups, row.Users)
		row.PickupsPerRound = ratio(row.Pickups, row.Rounds)
		row.TKRate = tkRate(row.TeamkillsByCarriers, row.KillsByCarriers)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

// fetchKitTopUsersFromMV / fetchKitTopMapsFromMV are the fast paths for the
// unfiltered /kits side panels: kit_top_users_mv / kit_top_maps_mv
// (migration 0009) precompute exactly these aggregations. ok is false
// (falls back to the live query) on an older install that hasn't applied
// that migration yet.
func (s *apiServer) fetchKitTopUsersFromMV(ctx context.Context, limit int) ([]codePlayerRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select player_id::text, display_name, pickups, rounds
		from kit_top_users_mv
		order by pickups desc, rounds desc, display_name asc
		limit $1
	`, limit)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	var result []codePlayerRow
	for rows.Next() {
		var row codePlayerRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Count, &row.Extra); err != nil {
			return nil, true, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func (s *apiServer) fetchKitTopMapsFromMV(ctx context.Context, limit int) ([]codeMapRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select map_name, pickups from kit_top_maps_mv order by pickups desc, map_name asc limit $1
	`, limit)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	var result []codeMapRow
	for rows.Next() {
		var row codeMapRow
		if err := rows.Scan(&row.Name, &row.Count); err != nil {
			return nil, true, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func (s *apiServer) fetchCodePlayersServer(ctx context.Context, query string, code string, limit int, serverFilter string) ([]codePlayerRow, error) {
	rows, err := s.db.QueryContext(ctx, query, code, limit, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []codePlayerRow
	for rows.Next() {
		var row codePlayerRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Count, &row.Extra); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchCodeMapsServer(ctx context.Context, query string, code string, limit int, serverFilter string) ([]codeMapRow, error) {
	rows, err := s.db.QueryContext(ctx, query, code, limit, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []codeMapRow
	for rows.Next() {
		var row codeMapRow
		if err := rows.Scan(&row.Name, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

const mapTopPlayersQuery = `
	select p.id::text, p.display_name, coalesce(sum(s.kills), 0)::int, coalesce(sum(s.score), 0)::int
	from round_player_stats s join rounds r on r.id=s.round_id join games g on g.id=r.game_id join players p on p.id=s.player_id and not p.hidden
	where g.map_name = $1
	group by p.id, p.display_name
	order by coalesce(sum(s.kills), 0) desc, coalesce(sum(s.score), 0) desc
	limit $2`

const mapTopWeaponsQuery = `
	select e.weapon_code, count(*)::int
	from events e join games g on g.id=e.game_id
	where g.map_name = $1 and e.event_type='score' and e.vehicle_code is null
	and nullif(e.weapon_code, '') is not null
	and ($3 = '' or g.server_id = nullif($3,'')::uuid)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	group by 1 order by count(*) desc limit $2`

// mapTopVehiclesQuery is a UNION ALL of two branches (enter/destroy events,
// and vehicle-tagged score events) rather than a single OR, so each branch
// can use its own narrow partial index (idx_events_vehicle_enter_destroy /
// idx_events_vehicle_score) via a nested loop keyed off games instead of a
// sequential scan over the whole events table.
const mapTopVehiclesQuery = `
	with combined as (
		select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code
		from events e join games g on g.id=e.game_id
		where g.map_name = $1 and e.event_type in ('enter_vehicle','destroy_vehicle')
		and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
		and ($3 = '' or g.server_id = nullif($3,'')::uuid)
		and lower(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), '')) not similar to '%(dummy|camera|wheel)%'
		union all
		select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code
		from events e join games g on g.id=e.game_id
		where g.map_name = $1 and e.event_type='score' and e.vehicle_code is not null
		and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
		and ($3 = '' or g.server_id = nullif($3,'')::uuid)
		and lower(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), '')) not similar to '%(dummy|camera|wheel)%'
	)
	select code, count(*)::int from combined group by 1 order by count(*) desc limit $2`

const mapTeamPressureQuery = `
	select case when s.team_id=1 then 'Axis' when s.team_id=2 then 'Allies' else 'Unknown' end,
		coalesce(sum(s.kills), 0)::int
	from round_player_stats s join rounds r on r.id=s.round_id join games g on g.id=r.game_id
	where g.map_name = $1
	and ($3 = '' or g.server_id = nullif($3,'')::uuid)
	group by 1 order by coalesce(sum(s.kills), 0) desc limit $2`

const mapModeSplitQuery = `
	select coalesce(nullif(g.game_mode, ''), 'unknown'), count(distinct r.id)::int
	from rounds r join games g on g.id=r.game_id
	where g.map_name = $1
	and ($3 = '' or g.server_id = nullif($3,'')::uuid)
	group by 1 order by count(distinct r.id) desc, 1 asc limit $2`

const mapEventMixQuery = `
	select event_type, count(*)::int
	from events e join games g on g.id=e.game_id
	where g.map_name = $1
	and ($3 = '' or g.server_id = nullif($3,'')::uuid)
	group by event_type order by count(*) desc, event_type asc limit $2`

const mapTopWeaponsFilteredQuery = `
	select e.weapon_code, count(*)::int
	from events e
	join games g on g.id=e.game_id
	left join rounds r on r.id=e.round_id
	left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
	where g.map_name = $1
	  and e.event_type='score'
	  and e.vehicle_code is null
	  and nullif(e.weapon_code, '') is not null
	  and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	  and ($3 = 'all' or ($3 = 'bots' and actor_stats.is_bot is true) or ($3 = 'humans' and coalesce(actor_stats.is_bot, false) is false))
	  and ($4 = 'all' or ($4 = '24h' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '24 hours') or ($4 = '7d' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '7 days'))
	  and ($5 = '' or g.server_id = nullif($5,'')::uuid)
	group by 1 order by count(*) desc limit $2`

const mapTopVehiclesFilteredQuery = `
	with combined as (
		select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code
		from events e
		join games g on g.id=e.game_id
		left join rounds r on r.id=e.round_id
		left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
		where g.map_name = $1
		  and e.event_type in ('enter_vehicle','destroy_vehicle')
		  and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
		  and lower(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), '')) not similar to '%(dummy|camera|wheel)%'
		  and ($3 = 'all' or ($3 = 'bots' and actor_stats.is_bot is true) or ($3 = 'humans' and coalesce(actor_stats.is_bot, false) is false))
		  and ($4 = 'all' or ($4 = '24h' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '24 hours') or ($4 = '7d' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '7 days'))
		  and ($5 = '' or g.server_id = nullif($5,'')::uuid)
		union all
		select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as code
		from events e
		join games g on g.id=e.game_id
		left join rounds r on r.id=e.round_id
		left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
		where g.map_name = $1
		  and e.event_type='score' and e.vehicle_code is not null
		  and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
		  and lower(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), '')) not similar to '%(dummy|camera|wheel)%'
		  and ($3 = 'all' or ($3 = 'bots' and actor_stats.is_bot is true) or ($3 = 'humans' and coalesce(actor_stats.is_bot, false) is false))
		  and ($4 = 'all' or ($4 = '24h' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '24 hours') or ($4 = '7d' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '7 days'))
		  and ($5 = '' or g.server_id = nullif($5,'')::uuid)
	)
	select code, count(*)::int from combined group by 1 order by count(*) desc limit $2`

const mapEventMixFilteredQuery = `
	select event_type, count(*)::int
	from events e
	join games g on g.id=e.game_id
	left join rounds r on r.id=e.round_id
	left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
	where g.map_name = $1
	  and ($3 = 'all' or ($3 = 'bots' and actor_stats.is_bot is true) or ($3 = 'humans' and coalesce(actor_stats.is_bot, false) is false))
	  and ($4 = 'all' or ($4 = '24h' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '24 hours') or ($4 = '7d' and coalesce(e.occurred_at, r.ended_at, g.started_at) >= now() - interval '7 days'))
	  and ($5 = '' or g.server_id = nullif($5,'')::uuid)
	group by event_type order by count(*) desc, event_type asc limit $2`

const rivalryWeaponsQuery = `
	with ids as (select split_part($1, ',', 1) a, split_part($1, ',', 2) b)
	select e.weapon_code, count(*)::int
	from events e
	cross join ids
	left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
	left join round_player_stats target_stats on target_stats.round_id=e.round_id and target_stats.slot_id=e.target_slot_id
	where ((coalesce(e.actor_player_id::text, actor_stats.player_id::text)=ids.a and coalesce(e.target_player_id::text, target_stats.player_id::text)=ids.b)
	or (coalesce(e.actor_player_id::text, actor_stats.player_id::text)=ids.b and coalesce(e.target_player_id::text, target_stats.player_id::text)=ids.a))
	and nullif(e.weapon_code, '') is not null
	group by 1 order by count(*) desc limit $2`

const rivalryMapsQuery = `
	with ids as (select split_part($1, ',', 1) a, split_part($1, ',', 2) b)
	select coalesce(g.map_name, 'unknown'), count(*)::int
	from events e
	join games g on g.id=e.game_id
	cross join ids
	left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
	left join round_player_stats target_stats on target_stats.round_id=e.round_id and target_stats.slot_id=e.target_slot_id
	where ((coalesce(e.actor_player_id::text, actor_stats.player_id::text)=ids.a and coalesce(e.target_player_id::text, target_stats.player_id::text)=ids.b)
	or (coalesce(e.actor_player_id::text, actor_stats.player_id::text)=ids.b and coalesce(e.target_player_id::text, target_stats.player_id::text)=ids.a))
	group by 1 order by count(*) desc limit $2`

// rivalryTrendQuery mirrors the kills CTE in fetchRivalrySummary but buckets
// by month (using the same occurred_at-or-derived wall-clock expression used
// for weapon/vehicle usage trends) so the rivalry page can show whether one
// side has been pulling ahead recently rather than only a lifetime total.
const rivalryTrendQuery = `
	with ids as (select split_part($1, ',', 1) a, split_part($1, ',', 2) b)
	select
		to_char(date_trunc('month', coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval)), 'YYYY-MM') as month,
		count(*) filter (where coalesce(e.actor_player_id::text, actor_stats.player_id::text) = ids.a and coalesce(e.target_player_id::text, target_stats.player_id::text) = ids.b)::int as a_kills,
		count(*) filter (where coalesce(e.actor_player_id::text, actor_stats.player_id::text) = ids.b and coalesce(e.target_player_id::text, target_stats.player_id::text) = ids.a)::int as b_kills
	from events e
	cross join ids
	join games g on g.id = e.game_id
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
	where e.event_type = 'score'
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and (
		(coalesce(e.actor_player_id::text, actor_stats.player_id::text) = ids.a and coalesce(e.target_player_id::text, target_stats.player_id::text) = ids.b)
		or (coalesce(e.actor_player_id::text, actor_stats.player_id::text) = ids.b and coalesce(e.target_player_id::text, target_stats.player_id::text) = ids.a)
	)
	group by 1
	order by 1 asc
	limit $2`

func (s *apiServer) fetchRivalryTrend(ctx context.Context, ids string, limit int) ([]seriesPoint, error) {
	rows, err := s.db.QueryContext(ctx, rivalryTrendQuery, ids, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []seriesPoint
	for rows.Next() {
		var row seriesPoint
		if err := rows.Scan(&row.Label, &row.Kills, &row.Deaths); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

const kitEffectivenessQuery = `
	select coalesce(kit_code, 'unknown'), count(*)::int
	from events
	where $1 = '' and event_type='pickup_kit' and kit_code is not null
	group by 1 order by count(*) desc limit $2`

const kitTopUsersQuery = `
	select p.id::text, p.display_name, count(*)::int, count(distinct e.round_id)::int
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
	join players p on p.id=coalesce(e.actor_player_id, actor_stats.player_id) and not p.hidden
	where $1 = '' and e.event_type='pickup_kit' and e.kit_code is not null
	and ($3 = '' or e.server_id = nullif($3,'')::uuid)
	group by p.id, p.display_name
	order by count(*) desc, count(distinct e.round_id) desc, p.display_name asc
	limit $2`

const kitTopMapsQuery = `
	select coalesce(g.map_name, 'unknown'), count(*)::int
	from events e join games g on g.id=e.game_id
	where $1 = '' and e.event_type='pickup_kit' and e.kit_code is not null
	and ($3 = '' or e.server_id = nullif($3,'')::uuid)
	group by 1 order by count(*) desc, 1 asc limit $2`

const supportLeadersQuery = `
	select p.id::text, p.display_name, coalesce(sum(s.heals + s.repairs), 0)::int, coalesce(sum(s.captures), 0)::int
	from round_player_stats s
	join rounds r on r.id=s.round_id
	join games g on g.id=r.game_id
	join players p on p.id=s.player_id and not p.hidden
	where $1 = ''
	and ($3 = '' or g.server_id = nullif($3,'')::uuid)
	group by p.id, p.display_name
	order by coalesce(sum(s.heals + s.repairs), 0) desc, coalesce(sum(s.captures), 0) desc
	limit $2`

var mapsTemplate = template.Must(template.New("maps").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Maps - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "maps" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Maps</strong><span class="muted">click a map for win splits, players, weapons, vehicles, and recent rounds</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="section-heading"><h2>Top Maps</h2><span class="muted">by rounds played{{ if gt (len .Maps) (len .TopMaps) }} &middot; {{ len .Maps }} in rotation, <a href="#map-metrics">full list below</a>{{ end }}</span></div>
{{ if .TopMaps }}
<div class="map-tiles">
{{ range $i, $m := .TopMaps }}
  <a class="map-tile card-link" href="{{ mapFilterURL $m.Name "all" "all" $.ServerFilter }}">
    <div class="map-tile-top"><span class="map-rank">{{ add $i 1 }}</span><span class="map-name">{{ displayCode $m.Name }}</span></div>
    <strong class="map-rounds">{{ $m.Rounds }}<small>rounds</small></strong>
    <div class="map-tile-meta">{{ $m.Kills }} kills &middot; {{ $m.Players }} players &middot; {{ $m.AvgDuration }}s avg</div>
    <div class="map-split">
      <div class="map-split-bar"><span class="axis" style="width:{{ pct $m.Team1Wins $m.Rounds }}%"></span><span class="allies" style="width:{{ pct $m.Team2Wins $m.Rounds }}%"></span></div>
      <div class="map-split-labels"><span><i class="sw axis"></i>Axis {{ $m.Team1Wins }}</span><span>Allies {{ $m.Team2Wins }}<i class="sw allies"></i></span></div>
    </div>
  </a>
{{ end }}
</div>
{{ else }}
  <div class="panel muted">No maps found.</div>
{{ end }}
<h2 id="map-metrics">Map Metrics</h2>
<table><thead><tr><th>Map</th><th>Rounds</th><th>Axis Wins</th><th>Allies Wins</th><th>Kills</th><th>Kills/Round</th><th>Avg Duration</th><th>Players</th></tr></thead><tbody>
{{ range .Maps }}<tr><td><a class="link" href="{{ mapFilterURL .Name "all" "all" $.ServerFilter }}">{{ displayCode .Name }}</a> <span class="muted mono">{{ .Name }}</span></td><td><a class="link" href="{{ mapFilterURL .Name "all" "all" $.ServerFilter }}">{{ .Rounds }}</a></td><td>{{ .Team1Wins }}</td><td>{{ .Team2Wins }}</td><td>{{ .Kills }}</td><td>{{ printf "%.1f" (rate .Kills .Rounds) }}</td><td>{{ .AvgDuration }}s</td><td>{{ .Players }}</td></tr>{{ end }}
</tbody></table>
</main>{{ siteFooter }}</body></html>`))

var mapTemplate = template.Must(template.New("map").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{ displayCode .Name }} - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "maps" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>{{ displayCode .Name }}</strong></div>
{{ if .Summary }}<div class="kpi-grid"><div class="kpi-card"><span>Rounds</span><strong>{{ .Summary.Rounds }}</strong></div><div class="kpi-card gold"><span>Kills</span><strong>{{ .Summary.Kills }}</strong><small>{{ printf "%.1f" (rate .Summary.Kills .Summary.Rounds) }} per round</small></div><div class="kpi-card"><span>Players</span><strong>{{ .Summary.Players }}</strong></div><div class="kpi-card"><span>Axis Wins</span><strong>{{ .Summary.Team1Wins }}</strong><small>{{ printf "%.2f" (rate .Summary.Team1Wins .Summary.Rounds) }} win share</small></div><div class="kpi-card"><span>Allies Wins</span><strong>{{ .Summary.Team2Wins }}</strong><small>{{ printf "%.2f" (rate .Summary.Team2Wins .Summary.Rounds) }} win share</small></div><div class="kpi-card green"><span>Avg Duration</span><strong>{{ .Summary.AvgDuration }}s</strong></div></div>{{ end }}
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="filters">
  <a class="{{ selectedFilter .BotFilter "all" }}" href="{{ mapFilterURL .Name "all" .Window .ServerFilter }}">All</a>
  <a class="{{ selectedFilter .BotFilter "humans" }}" href="{{ mapFilterURL .Name "humans" .Window .ServerFilter }}">Humans</a>
  <a class="{{ selectedFilter .BotFilter "bots" }}" href="{{ mapFilterURL .Name "bots" .Window .ServerFilter }}">Bots</a>
</div>
<div class="filters">
  <a class="{{ selectedFilter .Window "all" }}" href="{{ mapFilterURL .Name .BotFilter "all" .ServerFilter }}">All time</a>
  <a class="{{ selectedFilter .Window "24h" }}" href="{{ mapFilterURL .Name .BotFilter "24h" .ServerFilter }}">24h</a>
  <a class="{{ selectedFilter .Window "7d" }}" href="{{ mapFilterURL .Name .BotFilter "7d" .ServerFilter }}">7d</a>
</div>
<div class="grid2">
<section class="panel"><h3>Kill Origins</h3><div class="award-blurb">where kills are scored from &middot; brighter&thinsp;=&thinsp;hotter</div>{{ if .KillHeat }}<div class="chart">{{ .KillHeat }}</div>{{ else }}<div class="muted">Not enough recorded positions for this map yet.</div>{{ end }}</section>
<section class="panel"><h3>Death Zones</h3><div class="award-blurb">where players die &middot; brighter&thinsp;=&thinsp;hotter</div>{{ if .DeathHeat }}<div class="chart">{{ .DeathHeat }}</div>{{ else }}<div class="muted">Not enough recorded positions for this map yet.</div>{{ end }}</section>
</div>
<div class="grid2">
<section class="panel"><h3>Player Pressure</h3>{{ .PlayerBars }}</section>
<section class="panel"><h3>Weapon Pressure</h3>{{ .WeaponBars }}</section>
<section class="panel"><h3>Vehicle Pressure</h3>{{ .VehicleBars }}</section>
</div>
<div class="grid2">
<section class="panel"><h3>Recent Round Flow</h3>{{ .RecentBars }}</section>
<section class="panel"><h3>Team Kill Split</h3>{{ template "mapCountTable" .Teams }}</section>
<section class="panel"><h3>Game Modes</h3>{{ template "mapCountTable" .Modes }}</section>
<section class="panel"><h3>Event Mix</h3>{{ template "mapCountTable" .Events }}</section>
</div>
<div class="grid2">
<section class="panel"><h3>Best Players</h3>{{ template "operatorTable" .Players }}</section>
<section class="panel"><h3>Round Extremes</h3><table><thead><tr><th>Metric</th><th>Round</th><th>Detail</th></tr></thead><tbody>{{ range .Extremes }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No extremes.</td></tr>{{ end }}</tbody></table></section>
</div>
<h2>Weapon Meta</h2><table><thead><tr><th>Weapon</th><th>Kills</th><th>Kills/Round</th></tr></thead><tbody>{{ range .Weapons }}<tr><td><a class="link" href="{{ weaponURL .Name }}">{{ displayCode .Name }}</a> <span class="muted mono">{{ .Name }}</span></td><td>{{ .Count }}</td><td>{{ if $.Summary }}{{ printf "%.2f" (rate .Count $.Summary.Rounds) }}{{ else }}0.00{{ end }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No weapons.</td></tr>{{ end }}</tbody></table>
<h2>Vehicle Meta</h2><table><thead><tr><th>Vehicle</th><th>Events</th><th>Events/Round</th></tr></thead><tbody>{{ range .Vehicles }}<tr><td><a class="link" href="{{ vehicleURL .Name }}">{{ displayCode .Name }}</a> <span class="muted mono">{{ .Name }}</span></td><td>{{ .Count }}</td><td>{{ if $.Summary }}{{ printf "%.2f" (rate .Count $.Summary.Rounds) }}{{ else }}0.00{{ end }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No vehicles.</td></tr>{{ end }}</tbody></table>
<h2>Recent Rounds</h2><table><thead><tr><th>Round</th><th>Kills</th><th>Players</th><th>Winner</th><th>Duration</th><th>Ended</th></tr></thead><tbody>{{ range .Recent }}<tr><td><a class="link" href="{{ roundURL .ID }}"><span class="mono">{{ .RoundHash }}</span></a></td><td>{{ .Kills }}</td><td>{{ .PlayerCount }}</td><td>{{ teamName .WinningTeamID }}</td><td>{{ .Duration }}s</td><td>{{ .EndedAt }}</td></tr>{{ else }}<tr><td colspan="6" class="muted">No rounds.</td></tr>{{ end }}</tbody></table>
</main>{{ siteFooter }}</body></html>`))

var rivalryTemplate = template.Must(template.New("rivalry").Funcs(detailFuncMap()).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Rivalry - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head><body>{{ siteHeader "players" }}<main>{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}<div class="page-title"><strong>{{ .AName }} vs {{ .BName }}</strong><span class="muted">head-to-head</span></div><div class="kpi-grid"><div class="kpi-card gold"><span>{{ .AName }} Kills</span><strong>{{ .AKills }}</strong></div><div class="kpi-card red"><span>{{ .BName }} Kills</span><strong>{{ .BKills }}</strong></div></div><div class="grid2"><section class="panel"><h3>Weapons</h3>{{ template "mapCountTable" .Weapons }}</section><section class="panel"><h3>Maps</h3>{{ template "mapCountTable" .Maps }}</section></div><section class="panel"><h3>Kills Trend (Monthly)</h3>{{ performanceChartSVG .Trend }}<div class="legend"><span><i style="background:#bc861f"></i>{{ .AName }}</span><span><i style="background:#d05e55"></i>{{ .BName }}</span></div></section>{{ template "eventTable" .Events }}</main>{{ siteFooter }}</body></html>`))

var kitsTemplate = template.Must(template.New("kits").Funcs(detailFuncMap()).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Kits - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head><body>{{ siteHeader "kits" }}<main>{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}<div class="page-title"><strong>Kits</strong><span class="muted">pickup demand, carrier output, role spread, and map pressure</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<div class="grid2"><section class="panel"><h3>Pickup Demand</h3>{{ .DemandBars }}</section><section class="panel"><h3>Unique Users</h3>{{ .UserBars }}</section><section class="panel"><h3>Pickup Maps</h3>{{ .MapBars }}</section></div>
<div class="grid2"><section class="panel"><h3>Top Kit Users</h3><table><thead><tr><th>Player</th><th>Pickups</th><th>Rounds</th></tr></thead><tbody>{{ range .TopUsers }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Count }}</td><td>{{ .Extra }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No kit users.</td></tr>{{ end }}</tbody></table></section><section class="panel"><h3>Kit Pickup Maps</h3>{{ template "mapCountTable" .TopMaps }}</section></div>
<h2>Kit Metrics</h2><table><thead><tr><th>Kit</th><th>Pickups</th><th>Users</th><th>Rounds</th><th>Maps</th><th>Pickups/User</th><th>Pickups/Round</th><th>Carrier Kills</th><th>Carrier Deaths</th><th>Carrier K/D</th><th>Carrier TK Rate</th></tr></thead><tbody>{{ range .Kits }}<tr><td><a class="link" href="/kit?code={{ query .Code }}{{ if $.ServerFilter }}&server={{ query $.ServerFilter }}{{ end }}">{{ displayCode .Code }}</a></td><td>{{ .Pickups }}</td><td>{{ .Users }}</td><td>{{ .Rounds }}</td><td>{{ .Maps }}</td><td>{{ printf "%.2f" .PickupsPerUser }}</td><td>{{ printf "%.2f" .PickupsPerRound }}</td><td>{{ .KillsByCarriers }}</td><td>{{ .DeathsByCarriers }}</td><td>{{ formatKD .KillsByCarriers .DeathsByCarriers }}</td><td>{{ printf "%.1f" .TKRate }}%</td></tr>{{ else }}<tr><td colspan="11" class="muted">No kit pickup rows.</td></tr>{{ end }}</tbody></table>
</main>{{ siteFooter }}</body></html>`))

var supportTemplate = template.Must(template.New("support").Funcs(detailFuncMap()).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Support - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head><body>{{ siteHeader "support" }}<main>{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}<div class="page-title"><strong>Support Dashboard</strong><span class="muted">heals, repairs, captures</span></div>{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}<table><thead><tr><th>Player</th><th>Support Actions</th><th>Captures</th></tr></thead><tbody>{{ range .Rows }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Count }}</td><td>{{ .Extra }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No support rows.</td></tr>{{ end }}</tbody></table></main>{{ siteFooter }}</body></html>`))
