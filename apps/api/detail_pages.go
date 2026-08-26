package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	roundTimelineWindow = 250
	roundTimelinePage   = 25
)

type codeMapRow struct {
	Name  string
	Count int
}

type codePlayerRow struct {
	ID          string
	DisplayName string
	Count       int
	Extra       int
}

type weaponPageData struct {
	GeneratedAt   time.Time
	DBError       string
	Code          string
	Name          string
	Kills         int
	Teamkills     int
	Users         int
	Rounds        int
	FirstUsed     string
	LastUsed      string
	KillsPerUser  float64
	KillsPerRound float64
	TKRate        float64
	Killers       []codePlayerRow
	Victims       []codePlayerRow
	Maps          []codeMapRow
	Trend         []codeMapRow
	Events        []playerEventRow
}

type vehiclePageData struct {
	GeneratedAt       time.Time
	DBError           string
	Code              string
	Name              string
	Entries           int
	Kills             int
	Teamkills         int
	Users             int
	Rounds            int
	Destroyed         int
	KillsPerEntry     float64
	DestroyedPerEntry float64
	KillsPerUser      float64
	Operators         []codePlayerRow
	Destroyers        []codePlayerRow
	Maps              []codeMapRow
	EventMix          []codeMapRow
	KillMaps          []codeMapRow
	Trend             []codeMapRow
	OperatorBars      template.HTML
	MapBars           template.HTML
	KillBars          template.HTML
	Events            []playerEventRow
}

type roundPageData struct {
	GeneratedAt    time.Time
	DBError        string
	Summary        *roundRow
	Highlights     []keyValueRow
	Timeline       []playerEventRow
	TimelineQuery  string
	TimelinePager  pager
	TimelineWindow int
	Teams          []roundTeamRow
	TeamBars       template.HTML
	Players        []roundPlayerDetailRow
	PlayerBars     template.HTML
	SwingMoments   []swingMomentRow
	MapComparison  []mapComparisonRow
	Narratives     []keyValueRow
	Streaks        []codePlayerRow
	Weapons        []codeMapRow
	WeaponBars     template.HTML
	Vehicles       []codeMapRow
	VehicleBars    template.HTML
	EventTypes     []codeMapRow
	EventBars      template.HTML
	KillPace       []codeMapRow
	KillPaceChart  template.HTML
}

type keyValueRow struct {
	Label  string
	Value  string
	Detail string
}

type roundTeamRow struct {
	TeamID    int
	Players   int
	Score     int
	Kills     int
	Deaths    int
	Teamkills int
	Captures  int
	Heals     int
	Repairs   int
}

type swingMomentRow struct {
	Window  string
	TeamID  int
	Kills   int
	Players string
}

type mapComparisonRow struct {
	Metric  string
	Current string
	Average string
	Delta   string
}

type roundPlayerDetailRow struct {
	ID         string
	Name       string
	IsBot      *bool
	TeamID     *int
	Score      int
	Kills      int
	Deaths     int
	Teamkills  int
	Captures   int
	Heals      int
	Repairs    int
	Rank       *int
	CarryKills float64
	CarryScore float64
	Impact     float64
}

func (s *apiServer) weaponPage(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	// Site-wide per-code aggregation across events (no map filter) — same
	// reasoning as /weapons's wider budget in main.go.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data := weaponPageData{GeneratedAt: time.Now().UTC(), Code: code, Name: displayCode(code)}
	setError := func(err error) {
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}
	}
	setError(s.fetchWeaponDetailSummary(ctx, &data))
	data.KillsPerUser = ratio(data.Kills, data.Users)
	data.KillsPerRound = ratio(data.Kills, data.Rounds)
	data.TKRate = ratio(data.Teamkills, data.Kills+data.Teamkills) * 100
	var err error
	data.Killers, err = s.fetchCodePlayers(ctx, weaponKillersQuery, code, 15)
	setError(err)
	data.Victims, err = s.fetchCodePlayers(ctx, weaponVictimsQuery, code, 10)
	setError(err)
	data.Maps, err = s.fetchCodeMaps(ctx, weaponMapsQuery, code, 15)
	setError(err)
	data.Trend, err = s.fetchCodeMaps(ctx, weaponTrendQuery, code, 60)
	setError(err)
	data.Events, err = s.fetchWeaponEvents(ctx, code, "", "", 80)
	setError(err)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := weaponTemplate.Execute(w, data); err != nil {
		log.Printf("render weapon page failed: %v", err)
	}
}

func (s *apiServer) vehiclePage(w http.ResponseWriter, r *http.Request) {
	code := stripPCOSuffix(strings.TrimSpace(r.URL.Query().Get("code")))
	if code == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	// Site-wide per-code aggregation across events (no map filter) — same
	// reasoning as /vehicles's wider budget in main.go.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data := vehiclePageData{GeneratedAt: time.Now().UTC(), Code: code, Name: displayCode(code)}
	setError := func(err error) {
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}
	}
	setError(s.fetchVehicleDetailSummary(ctx, &data))
	data.KillsPerEntry = ratio(data.Kills, data.Entries)
	data.DestroyedPerEntry = ratio(data.Destroyed, data.Entries)
	data.KillsPerUser = ratio(data.Kills, data.Users)
	var err error
	data.Operators, err = s.fetchCodePlayers(ctx, vehicleOperatorsQuery, code, 15)
	setError(err)
	data.Destroyers, err = s.fetchCodePlayers(ctx, vehicleDestroyersQuery, code, 12)
	setError(err)
	data.Maps, err = s.fetchCodeMaps(ctx, vehicleMapsQuery, code, 15)
	setError(err)
	data.EventMix, err = s.fetchCodeMaps(ctx, vehicleEventMixQuery, code, 12)
	setError(err)
	data.KillMaps, err = s.fetchCodeMaps(ctx, vehicleKillMapsQuery, code, 12)
	setError(err)
	data.Trend, err = s.fetchCodeMaps(ctx, vehicleTrendQuery, code, 60)
	setError(err)
	data.OperatorBars = codePlayerBars(data.Operators, colorKills)
	data.MapBars = codeMapBars(data.Maps, colorInfo)
	data.KillBars = codeMapBars(data.KillMaps, colorKills)
	data.Events, err = s.fetchVehicleEvents(ctx, code, 80)
	setError(err)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := vehicleTemplate.Execute(w, data); err != nil {
		log.Printf("render vehicle page failed: %v", err)
	}
}

func (s *apiServer) roundPage(w http.ResponseWriter, r *http.Request) {
	roundID := sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("id")))
	if roundID == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	timelinePage := pageParam(r, "timeline_page")
	timelineQuery := strings.TrimSpace(r.URL.Query().Get("timeline_q"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	data := roundPageData{
		GeneratedAt:    time.Now().UTC(),
		TimelineQuery:  timelineQuery,
		TimelineWindow: roundTimelineWindow,
	}
	setError := func(err error) {
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}
	}
	var err error
	data.Summary, err = s.fetchRoundByID(ctx, roundID)
	setError(err)
	if data.Summary == nil {
		http.NotFound(w, r)
		return
	}
	data.Players, err = s.fetchRoundPlayers(ctx, roundID)
	setError(err)
	data.Teams, err = s.fetchRoundTeams(ctx, roundID)
	setError(err)
	applyRoundPlayerAnalysis(data.Players, data.Teams)
	data.TeamBars = roundTeamBars(data.Teams)
	data.PlayerBars = roundPlayerImpactBars(data.Players)
	data.Highlights = roundHighlights(data.Players)
	data.Highlights = append(data.Highlights, balanceHighlight(data.Teams))
	timelineOffset := (timelinePage - 1) * roundTimelinePage
	timeline, err := s.fetchRoundTimeline(ctx, roundID, timelineQuery, roundTimelinePage+1, timelineOffset)
	setError(err)
	data.Timeline, data.TimelinePager = paginateRoundTimeline(timeline, roundTimelinePage, timelinePage, roundID, timelineQuery)
	data.SwingMoments, err = s.fetchSwingMoments(ctx, roundID)
	setError(err)
	data.MapComparison, err = s.fetchRoundMapComparison(ctx, roundID)
	setError(err)
	data.Narratives, err = s.fetchRoundNarratives(ctx, roundID)
	setError(err)
	data.Streaks, err = s.fetchRoundStreaks(ctx, roundID)
	setError(err)
	data.Weapons, err = s.fetchCodeMaps(ctx, roundWeaponsQuery, roundID, 15)
	setError(err)
	data.WeaponBars = codeMapBars(data.Weapons, colorKills)
	data.Vehicles, err = s.fetchCodeMaps(ctx, roundVehiclesQuery, roundID, 15)
	setError(err)
	data.VehicleBars = codeMapBars(data.Vehicles, colorInfo)
	data.EventTypes, err = s.fetchCodeMaps(ctx, roundEventTypesQuery, roundID, 12)
	setError(err)
	data.EventBars = codeMapBars(data.EventTypes, colorScore)
	data.KillPace, err = s.fetchCodeMaps(ctx, roundKillPaceQuery, roundID, 20)
	setError(err)
	data.KillPaceChart = roundKillPaceChart(data.KillPace)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := roundTemplate.Execute(w, data); err != nil {
		log.Printf("render round page failed: %v", err)
	}
}

func (s *apiServer) fetchWeaponDetailSummary(ctx context.Context, data *weaponPageData) error {
	return s.db.QueryRowContext(ctx, `
		with vehicle_codes as (
			select code from vehicle_code_list_mv
		)
		select
			count(*) filter (where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills'))::int,
			count(*) filter (where lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int,
			count(distinct coalesce(e.actor_player_id::text, actor_stats.player_id::text, e.actor_slot_id::text))::int,
			count(distinct e.round_id)::int,
			coalesce(to_char(min(coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), ''),
			coalesce(to_char(max(coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '')
		from events e
		left join games g on g.id = e.game_id
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		where e.event_type = 'score'
		and e.vehicle_code is null
		and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and coalesce(e.weapon_code, '') = $1
	`, data.Code).Scan(&data.Kills, &data.Teamkills, &data.Users, &data.Rounds, &data.FirstUsed, &data.LastUsed)
}

func (s *apiServer) fetchVehicleDetailSummary(ctx context.Context, data *vehiclePageData) error {
	// Filters by the regexp_replace(...) expression directly (matching
	// idx_events_vehicle_code_expr from migration 0008) instead of computing
	// it for every vehicle-related event site-wide via a CTE and filtering
	// to one code afterward — that used to materialize the whole
	// all-vehicles candidate set before ever narrowing to $1.
	return s.db.QueryRowContext(ctx, `
		with vehicle_codes as (
			select code from vehicle_code_list_mv
		)
		select
			count(*) filter (where e.event_type = 'enter_vehicle')::int,
			count(*) filter (where e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills'))::int,
			count(*) filter (where e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int,
			count(distinct coalesce(e.actor_player_id, actor_stats.player_id))::int,
			count(distinct e.round_id)::int,
			count(*) filter (where e.raw_event_name = 'destroyVehicle' or e.event_type = 'destroy_vehicle')::int
		from events e
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
		and (
			e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle')
			or (e.event_type = 'score' and e.vehicle_code is not null)
			or (e.event_type = 'score' and coalesce(e.weapon_code, '') in (select code from vehicle_codes))
		)
	`, data.Code).Scan(&data.Entries, &data.Kills, &data.Teamkills, &data.Users, &data.Rounds, &data.Destroyed)
}

const weaponKillersQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select p.id::text, p.display_name, count(*)::int, 0::int
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	join players p on p.id = coalesce(e.actor_player_id, actor_stats.player_id) and not p.hidden
	where e.event_type = 'score' and e.vehicle_code is null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.weapon_code, '') = $1
	group by p.id, p.display_name
	order by count(*) desc, p.display_name asc
	limit $2`

const weaponVictimsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select p.id::text, p.display_name, count(*)::int, 0::int
	from events e
	left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
	join players p on p.id = coalesce(e.target_player_id, target_stats.player_id) and not p.hidden
	where e.event_type = 'score' and e.vehicle_code is null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.weapon_code, '') = $1
	group by p.id, p.display_name
	order by count(*) desc, p.display_name asc
	limit $2`

const weaponMapsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select coalesce(nullif(g.map_name, ''), 'unknown'), count(*)::int
	from events e
	left join games g on g.id = e.game_id
	where e.event_type = 'score' and e.vehicle_code is null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.weapon_code, '') = $1
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

// weaponTrendQuery buckets the same kill/teamkill event set as
// weaponKillersQuery by month (using the same occurred_at-or-derived
// wall-clock expression fetchWeaponDetailSummary uses for FirstUsed/LastUsed)
// so a meta shift after a mod/config change shows up as a usage trend rather
// than only a lifetime total.
const weaponTrendQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select to_char(date_trunc('month', coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval)), 'YYYY-MM'), count(*)::int
	from events e
	left join games g on g.id = e.game_id
	where e.event_type = 'score' and e.vehicle_code is null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	and coalesce(e.weapon_code, '') = $1
	group by 1
	order by 1 asc
	limit $2`

const vehicleOperatorsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select p.id::text, p.display_name,
		count(*) filter (where e.event_type = 'enter_vehicle')::int,
		count(*) filter (where e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills'))::int
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	join players p on p.id = coalesce(e.actor_player_id, actor_stats.player_id) and not p.hidden
	where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
	and (e.event_type = 'enter_vehicle' or (e.event_type = 'score' and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))))
	group by p.id, p.display_name
	order by count(*) filter (where e.event_type = 'score') desc, count(*) filter (where e.event_type = 'enter_vehicle') desc, p.display_name asc
	limit $2`

const vehicleMapsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select coalesce(nullif(g.map_name, ''), 'unknown'), count(*)::int
	from events e
	left join games g on g.id = e.game_id
	where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
	and (e.event_type in ('enter_vehicle', 'destroy_vehicle') or (e.event_type = 'score' and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))))
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

// vehicleTrendQuery mirrors weaponTrendQuery for vehicle usage/entries by
// month.
const vehicleTrendQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select to_char(date_trunc('month', coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval)), 'YYYY-MM'), count(*)::int
	from events e
	left join games g on g.id = e.game_id
	where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
	and (e.event_type in ('enter_vehicle', 'destroy_vehicle') or (e.event_type = 'score' and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))))
	group by 1
	order by 1 asc
	limit $2`

const vehicleKillMapsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select coalesce(nullif(g.map_name, ''), 'unknown'), count(*)::int
	from events e
	left join games g on g.id = e.game_id
	where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
	and e.event_type = 'score'
	and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

const vehicleEventMixQuery = `
	select
		case
			when e.event_type = 'score' then lower(coalesce(e.params->>'score_type', 'score'))
			else e.event_type
		end,
		count(*)::int
	from events e
	where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
	and (e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle') or e.event_type = 'score')
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

const vehicleDestroyersQuery = `
	select p.id::text, p.display_name, count(*)::int, 0::int
	from events e
	left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
	join players p on p.id = coalesce(e.actor_player_id, actor_stats.player_id) and not p.hidden
	where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $1
	and (e.event_type = 'destroy_vehicle' or e.raw_event_name = 'destroyVehicle')
	group by p.id, p.display_name
	order by count(*) desc, p.display_name asc
	limit $2`

const roundWeaponsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select e.weapon_code, count(*)::int
	from events e
	where e.round_id = nullif($1,'')::uuid
	and e.event_type = 'score'
	and e.vehicle_code is null
	and nullif(e.weapon_code, '') is not null
	and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

const roundVehiclesQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', ''), count(*)::int
	from events e
	where e.round_id = nullif($1,'')::uuid
	and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown') not like '%Soldier'
	and (e.event_type in ('enter_vehicle', 'destroy_vehicle') or (e.event_type = 'score' and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))))
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

const roundEventTypesQuery = `
	select
		case
			when event_type = 'score' then 'combat'
			when event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle') then 'vehicle'
			when event_type in ('begin_heal', 'end_heal') then 'medical'
			when event_type in ('begin_repair', 'end_repair') then 'repair'
			when event_type in ('pickup_kit', 'spawn') then event_type
			else event_type
		end,
		count(*)::int
	from events
	where round_id = nullif($1,'')::uuid
	group by 1
	order by count(*) desc, 1 asc
	limit $2`

const roundKillPaceQuery = `
	select
		to_char((floor(timestamp_seconds / 30) * 30 || ' seconds')::interval, 'MI:SS') || '-' ||
			to_char(((floor(timestamp_seconds / 30) * 30 + 29) || ' seconds')::interval, 'MI:SS') as bucket,
		count(*)::int
	from events
	where round_id = nullif($1,'')::uuid
	and event_type = 'score'
	and lower(coalesce(params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
	group by floor(timestamp_seconds / 30)
	order by floor(timestamp_seconds / 30)
	limit $2`

func (s *apiServer) fetchCodePlayers(ctx context.Context, query string, code string, limit int) ([]codePlayerRow, error) {
	rows, err := s.db.QueryContext(ctx, query, code, limit)
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

func (s *apiServer) fetchCodeMaps(ctx context.Context, query string, code string, limit int) ([]codeMapRow, error) {
	rows, err := s.db.QueryContext(ctx, query, code, limit)
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

func (s *apiServer) fetchVehicleEvents(ctx context.Context, code string, limit int) ([]playerEventRow, error) {
	rows, err := s.db.QueryContext(ctx, eventSelectSQL+`
		where regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') = $2
		and (e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle') or e.event_type = 'score')
		order by coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) desc, e.sequence_no desc
		limit $1
	`, limit, code)
	if err != nil {
		return nil, err
	}
	return scanPlayerEvents(rows)
}

func (s *apiServer) fetchRoundTimeline(ctx context.Context, roundID string, search string, limit int, offset int) ([]playerEventRow, error) {
	rows, err := s.db.QueryContext(ctx, eventSelectSQL+`
		where e.round_id = nullif($2,'')::uuid
		  and (
		    (e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills', 'capture', 'captures'))
		    or e.event_type = 'destroy_vehicle'
		  )
		  and e.id in (
			select recent.id
			from events recent
			where recent.round_id = nullif($2,'')::uuid
			  and (
				(recent.event_type = 'score' and lower(coalesce(recent.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills', 'capture', 'captures'))
				or recent.event_type = 'destroy_vehicle'
			  )
			order by recent.sequence_no desc
			limit `+strconv.Itoa(roundTimelineWindow)+`
		  )
		  and (
			$3 = ''
			or lower(concat_ws(' ',
				e.event_type,
				e.raw_event_name,
				coalesce(actor.display_name, ''),
				coalesce(target.display_name, ''),
				coalesce(e.weapon_code, ''),
				coalesce(e.vehicle_code, ''),
				coalesce(e.kit_code, ''),
				coalesce(e.chat_text, ''),
				coalesce((e.params - 'keyhash')::text, '{}')
			)) like '%' || lower($3) || '%'
		  )
		order by e.sequence_no desc
		limit $1 offset $4
	`, limit, roundID, search, offset)
	if err != nil {
		return nil, err
	}
	result, err := scanPlayerEvents(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

func (s *apiServer) fetchRoundByID(ctx context.Context, roundID string) (*roundRow, error) {
	row := s.db.QueryRowContext(ctx, `
		with event_totals as (
			select round_id, count(*)::int as event_count
			from events where round_id = nullif($1,'')::uuid group by round_id
		),
		player_totals as (
			select round_id, count(distinct player_id)::int as player_count, coalesce(sum(kills), 0)::int as kills
			from round_player_stats where round_id = nullif($1,'')::uuid group by round_id
		)
		select
			coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), ''), r.id::text, coalesce(g.map_name, ''), coalesce(g.game_mode, ''), r.round_index,
			coalesce(et.event_count, 0), coalesce(pt.player_count, 0), coalesce(pt.kills, 0), r.winning_team_id,
			r.victory_type,
			coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::int,
			coalesce(r.end_tickets::text, '{}'),
			to_char(g.started_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'),
			coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '')
		from rounds r
		join games g on g.id = r.game_id
		left join raw_log_files raw on raw.id = g.raw_log_file_id
		left join event_totals et on et.round_id = r.id
		left join player_totals pt on pt.round_id = r.id
		where r.id = nullif($1,'')::uuid
	`, roundID)
	var result roundRow
	if err := row.Scan(&result.RoundHash, &result.ID, &result.MapName, &result.GameMode, &result.RoundIndex, &result.EventCount, &result.PlayerCount, &result.Kills, &result.WinningTeamID, &result.VictoryType, &result.Duration, &result.EndTickets, &result.StartedAt, &result.EndedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

func (s *apiServer) fetchRoundPlayers(ctx context.Context, roundID string) ([]roundPlayerDetailRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select p.id::text, p.display_name, s.is_bot, s.team_id, s.score, s.kills, s.deaths, s.teamkills, s.captures, s.heals, s.repairs, s.rank_in_round
		from round_player_stats s
		join players p on p.id = s.player_id
		where s.round_id = nullif($1,'')::uuid
		order by s.score desc, s.kills desc, s.deaths asc, p.display_name asc
	`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []roundPlayerDetailRow
	for rows.Next() {
		var row roundPlayerDetailRow
		var isBot sql.NullBool
		var teamID sql.NullInt64
		var rank sql.NullInt64
		if err := rows.Scan(&row.ID, &row.Name, &isBot, &teamID, &row.Score, &row.Kills, &row.Deaths, &row.Teamkills, &row.Captures, &row.Heals, &row.Repairs, &rank); err != nil {
			return nil, err
		}
		if isBot.Valid {
			row.IsBot = &isBot.Bool
		}
		if teamID.Valid {
			value := int(teamID.Int64)
			row.TeamID = &value
		}
		if rank.Valid {
			value := int(rank.Int64)
			row.Rank = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchRoundTeams(ctx context.Context, roundID string) ([]roundTeamRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(team_id, 0)::int,
			count(distinct player_id)::int,
			coalesce(sum(score), 0)::int,
			coalesce(sum(kills), 0)::int,
			coalesce(sum(deaths), 0)::int,
			coalesce(sum(teamkills), 0)::int,
			coalesce(sum(captures), 0)::int,
			coalesce(sum(heals), 0)::int,
			coalesce(sum(repairs), 0)::int
		from round_player_stats
		where round_id = nullif($1,'')::uuid
		group by 1
		order by 1
	`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []roundTeamRow
	for rows.Next() {
		var row roundTeamRow
		if err := rows.Scan(&row.TeamID, &row.Players, &row.Score, &row.Kills, &row.Deaths, &row.Teamkills, &row.Captures, &row.Heals, &row.Repairs); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchSwingMoments(ctx context.Context, roundID string) ([]swingMomentRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with kill_events as (
			select
				floor(e.timestamp_seconds / 20)::int as bucket,
				coalesce(actor_stats.team_id, 0)::int as team_id,
				coalesce(actor.display_name, 'Unknown') as actor_name
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			left join players actor on actor.id = coalesce(e.actor_player_id, actor_stats.player_id) and not actor.hidden
			where e.round_id = nullif($1,'')::uuid
			and e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
		)
		select
			to_char((bucket * 20 || ' seconds')::interval, 'MI:SS') || '-' || to_char(((bucket * 20 + 19) || ' seconds')::interval, 'MI:SS'),
			team_id,
			count(*)::int,
			string_agg(distinct actor_name, ', ' order by actor_name)
		from kill_events
		group by bucket, team_id
		having count(*) >= 3
		order by count(*) desc, bucket asc
		limit 8
	`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []swingMomentRow
	for rows.Next() {
		var row swingMomentRow
		if err := rows.Scan(&row.Window, &row.TeamID, &row.Kills, &row.Players); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchRoundMapComparison(ctx context.Context, roundID string) ([]mapComparisonRow, error) {
	row := s.db.QueryRowContext(ctx, `
		with current_round as (
			select
				r.id,
				g.map_name,
				coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::float8 as duration_seconds,
				(select count(*) from events e where e.round_id = r.id)::float8 as events,
				(select count(distinct player_id) from round_player_stats s where s.round_id = r.id)::float8 as players,
				(select coalesce(sum(kills), 0) from round_player_stats s where s.round_id = r.id)::float8 as kills
			from rounds r
			join games g on g.id = r.game_id
			where r.id = nullif($1,'')::uuid
		),
		map_rounds as (
			select
				r.id,
				coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::float8 as duration_seconds,
				(select count(*) from events e where e.round_id = r.id)::float8 as events,
				(select count(distinct player_id) from round_player_stats s where s.round_id = r.id)::float8 as players,
				(select coalesce(sum(kills), 0) from round_player_stats s where s.round_id = r.id)::float8 as kills
			from rounds r
			join games g on g.id = r.game_id
			join current_round cr on cr.map_name = g.map_name
		)
		select
			cr.kills, coalesce(avg(mr.kills), 0),
			cr.duration_seconds, coalesce(avg(mr.duration_seconds), 0),
			cr.players, coalesce(avg(mr.players), 0),
			cr.events, coalesce(avg(mr.events), 0)
		from current_round cr
		left join map_rounds mr on true
		group by cr.kills, cr.duration_seconds, cr.players, cr.events
	`, roundID)

	var kills, avgKills, duration, avgDuration, players, avgPlayers, events, avgEvents float64
	if err := row.Scan(&kills, &avgKills, &duration, &avgDuration, &players, &avgPlayers, &events, &avgEvents); err != nil {
		return nil, err
	}
	return []mapComparisonRow{
		comparisonRow("Kills", kills, avgKills, "%.0f"),
		comparisonRow("Duration", duration, avgDuration, "%.0fs"),
		comparisonRow("Players", players, avgPlayers, "%.0f"),
		comparisonRow("Events", events, avgEvents, "%.0f"),
	}, nil
}

func (s *apiServer) fetchRoundNarratives(ctx context.Context, roundID string) ([]keyValueRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with first_blood as (
			select
				'First Blood' as label,
				coalesce(actor.display_name, 'Unknown') as value,
				coalesce(to_char((e.timestamp_seconds || ' seconds')::interval, 'MI:SS'), '') || ' vs ' || coalesce(target.display_name, 'Unknown') as detail
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
			left join round_player_stats target_stats on target_stats.round_id=e.round_id and target_stats.slot_id=e.target_slot_id
			left join players actor on actor.id=coalesce(e.actor_player_id, actor_stats.player_id) and not actor.hidden
			left join players target on target.id=coalesce(e.target_player_id, target_stats.player_id) and not target.hidden
			where e.round_id = nullif($1,'')::uuid and e.event_type='score' and lower(coalesce(e.params->>'score_type','')) in ('kill','kills')
			order by e.timestamp_seconds, e.sequence_no
			limit 1
		),
		opening as (
			select 'Opening 30s' as label,
				case when coalesce(actor_stats.team_id, 0)=1 then 'Axis' when coalesce(actor_stats.team_id, 0)=2 then 'Allies' else 'Unknown' end as value,
				count(*)::text || ' kills' as detail
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
			where e.round_id = nullif($1,'')::uuid and e.timestamp_seconds <= 30 and e.event_type='score' and lower(coalesce(e.params->>'score_type','')) in ('kill','kills')
			group by actor_stats.team_id
			order by count(*) desc
			limit 1
		),
		comeback_stream as (
			select
				e.sequence_no,
				coalesce(actor_stats.team_id, 0) as team_id,
				sum(case when coalesce(actor_stats.team_id, 0)=1 then 1 else 0 end) over (order by e.timestamp_seconds, e.sequence_no) as team1_kills,
				sum(case when coalesce(actor_stats.team_id, 0)=2 then 1 else 0 end) over (order by e.timestamp_seconds, e.sequence_no) as team2_kills
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
			where e.round_id = nullif($1,'')::uuid and e.event_type='score' and lower(coalesce(e.params->>'score_type','')) in ('kill','kills')
		),
		winner as (
			select coalesce(winning_team_id, 0) as team_id from rounds where id = nullif($1,'')::uuid
		),
		comeback as (
			select 'Comeback' as label,
				case when w.team_id=0 then 'Unknown winner'
					when w.team_id=1 and min(team1_kills - team2_kills) < 0 then 'Axis'
					when w.team_id=2 and min(team2_kills - team1_kills) < 0 then 'Allies'
					else 'No comeback'
				end as value,
				case when w.team_id=1 then 'worst deficit ' || min(team1_kills - team2_kills)::text
					when w.team_id=2 then 'worst deficit ' || min(team2_kills - team1_kills)::text
					else '' end as detail
			from comeback_stream, winner w
			group by w.team_id
		)
		select label, value, detail from first_blood
		union all select label, value, detail from opening
		union all select label, value, detail from comeback
	`, roundID)
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

func (s *apiServer) fetchRoundStreaks(ctx context.Context, roundID string) ([]codePlayerRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with combat as (
			select
				coalesce(e.actor_player_id, actor_stats.player_id) as actor_id,
				coalesce(actor.display_name, 'Unknown') as actor_name,
				e.timestamp_seconds,
				e.sequence_no
			from events e
			left join round_player_stats actor_stats on actor_stats.round_id=e.round_id and actor_stats.slot_id=e.actor_slot_id
			left join players actor on actor.id=coalesce(e.actor_player_id, actor_stats.player_id) and not actor.hidden
			where e.round_id = nullif($1,'')::uuid and e.event_type='score' and lower(coalesce(e.params->>'score_type','')) in ('kill','kills')
		),
		marked as (
			select *,
				case when lag(actor_id) over (order by timestamp_seconds, sequence_no) = actor_id then 0 else 1 end as new_run
			from combat
		),
		runs as (
			select actor_id, actor_name, sum(new_run) over (order by timestamp_seconds, sequence_no) as run_id
			from marked
		),
		run_lengths as (
			select actor_id, actor_name, count(*)::int as streak
			from runs
			group by actor_id, actor_name, run_id
		),
		totals as (
			select actor_id, count(*)::int as total_kills from combat group by actor_id
		)
		select coalesce(r.actor_id::text, ''), r.actor_name, max(r.streak)::int, coalesce(max(t.total_kills), 0)::int
		from run_lengths r
		left join totals t on t.actor_id = r.actor_id
		group by r.actor_id, r.actor_name
		order by max(r.streak) desc, max(t.total_kills) desc, r.actor_name asc
		limit 10
	`, roundID)
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

func applyRoundPlayerAnalysis(players []roundPlayerDetailRow, teams []roundTeamRow) {
	teamKills := map[int]int{}
	teamScore := map[int]int{}
	for _, team := range teams {
		teamKills[team.TeamID] = team.Kills
		teamScore[team.TeamID] = team.Score
	}
	for i := range players {
		teamID := 0
		if players[i].TeamID != nil {
			teamID = *players[i].TeamID
		}
		players[i].CarryKills = ratio(players[i].Kills, teamKills[teamID]) * 100
		players[i].CarryScore = ratio(players[i].Score, teamScore[teamID]) * 100
		players[i].Impact = playerImpact(players[i])
	}
}

func playerImpact(player roundPlayerDetailRow) float64 {
	return float64(player.Score) +
		float64(player.Kills*2) -
		float64(player.Deaths) -
		float64(player.Teamkills*2) +
		float64(player.Captures*3) +
		float64(player.Heals)/4 +
		float64(player.Repairs)/2
}

func balanceHighlight(teams []roundTeamRow) keyValueRow {
	if len(teams) < 2 {
		return keyValueRow{Label: "Balance", Value: "Unknown", Detail: "need two teams"}
	}
	a, b := teams[0], teams[1]
	scoreGap := absInt(a.Score - b.Score)
	killGap := absInt(a.Kills - b.Kills)
	playerGap := absInt(a.Players - b.Players)
	rating := 100 - minInt(80, scoreGap+killGap*2+playerGap*5)
	return keyValueRow{Label: "Balance", Value: fmt.Sprintf("%d/100", rating), Detail: fmt.Sprintf("score gap %d, kill gap %d", scoreGap, killGap)}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func paginateRoundTimeline(rows []playerEventRow, size int, page int, roundID string, search string) ([]playerEventRow, pager) {
	hasMore := len(rows) > size
	if hasMore {
		rows = rows[:size]
	}
	link := func(p int) string {
		values := url.Values{}
		values.Set("id", roundID)
		if strings.TrimSpace(search) != "" {
			values.Set("timeline_q", strings.TrimSpace(search))
		}
		if p > 1 {
			values.Set("timeline_page", strconv.Itoa(p))
		}
		return "/round?" + values.Encode() + "#combat-timeline"
	}
	result := pager{Page: page}
	if page > 1 {
		result.PrevURL = link(page - 1)
	}
	if hasMore {
		result.NextURL = link(page + 1)
	}
	return rows, result
}

func roundHighlights(players []roundPlayerDetailRow) []keyValueRow {
	if len(players) == 0 {
		return nil
	}
	topScore := players[0]
	topKills := players[0]
	topDeaths := players[0]
	topSupport := players[0]
	topImpact := players[0]
	for _, player := range players[1:] {
		if player.Score > topScore.Score {
			topScore = player
		}
		if player.Kills > topKills.Kills {
			topKills = player
		}
		if player.Deaths > topDeaths.Deaths {
			topDeaths = player
		}
		if player.Heals+player.Repairs > topSupport.Heals+topSupport.Repairs {
			topSupport = player
		}
		if player.Impact > topImpact.Impact {
			topImpact = player
		}
	}
	return []keyValueRow{
		{Label: "Top Score", Value: topScore.Name, Detail: fmt.Sprintf("%d score", topScore.Score)},
		{Label: "Most Kills", Value: topKills.Name, Detail: fmt.Sprintf("%d kills", topKills.Kills)},
		{Label: "Most Deaths", Value: topDeaths.Name, Detail: fmt.Sprintf("%d deaths", topDeaths.Deaths)},
		{Label: "Support", Value: topSupport.Name, Detail: fmt.Sprintf("%d heals / %d repairs", topSupport.Heals, topSupport.Repairs)},
		{Label: "Contribution", Value: topImpact.Name, Detail: fmt.Sprintf("%.1f rating", topImpact.Impact)},
	}
}

func roundTeamBars(teams []roundTeamRow) template.HTML {
	items := make([]barItem, 0, len(teams))
	for _, team := range teams {
		items = append(items, barItem{Label: teamName(team.TeamID), Value: team.Kills})
	}
	return barListHTML(items, colorKills)
}

func roundPlayerImpactBars(players []roundPlayerDetailRow) template.HTML {
	ranked := append([]roundPlayerDetailRow(nil), players...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Impact == ranked[j].Impact {
			return ranked[i].Name < ranked[j].Name
		}
		return ranked[i].Impact > ranked[j].Impact
	})
	items := make([]barItem, 0, minInt(len(ranked), 10))
	for i, player := range ranked {
		if i >= 10 {
			break
		}
		value := int(player.Impact + 0.5)
		if value < 0 {
			value = 0
		}
		items = append(items, barItem{Label: player.Name, Value: value, URL: playerURL(player.ID, "all")})
	}
	return barListHTML(items, colorScore)
}

func roundKillPaceChart(rows []codeMapRow) template.HTML {
	items := make([]barItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, barItem{Label: row.Name, Value: row.Count})
	}
	return columnChartSVG(items, colorKills, "Round kill pace")
}

const eventSelectSQL = `
	select
		coalesce(to_char(e.occurred_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), to_char(g.started_at + (e.timestamp_seconds || ' seconds')::interval, 'YYYY-MM-DD HH24:MI:SS "UTC"')),
		coalesce(g.map_name, ''),
		coalesce(r.round_index, 0),
		e.event_type,
		e.raw_event_name,
		coalesce(actor.id::text, ''),
		coalesce(actor.display_name, ''),
		case when actor_slot_stats.is_bot is true then 'Bot' when actor_slot_stats.is_bot is false then 'Human' else '' end,
		coalesce(target.id::text, ''),
		coalesce(target.display_name, ''),
		case when target_slot_stats.is_bot is true then 'Bot' when target_slot_stats.is_bot is false then 'Human' else '' end,
		coalesce(e.weapon_code, ''),
		coalesce(e.vehicle_code, ''),
		coalesce(e.kit_code, ''),
		coalesce(e.chat_text, ''),
		coalesce((e.params - 'keyhash')::text, '{}')
	from events e
	left join rounds r on r.id = e.round_id
	left join games g on g.id = e.game_id
	left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
	left join round_player_stats target_slot_stats on target_slot_stats.round_id = e.round_id and target_slot_stats.slot_id = e.target_slot_id
	left join players actor on actor.id = coalesce(e.actor_player_id, actor_slot_stats.player_id) and not actor.hidden
	left join players target on target.id = coalesce(e.target_player_id, target_slot_stats.player_id) and not target.hidden
`

func scanPlayerEvents(rows *sql.Rows) ([]playerEventRow, error) {
	defer rows.Close()
	result := []playerEventRow{}
	for rows.Next() {
		var row playerEventRow
		if err := rows.Scan(&row.When, &row.MapName, &row.RoundIndex, &row.EventType, &row.RawEventName, &row.ActorID, &row.ActorName, &row.ActorKind, &row.TargetID, &row.TargetName, &row.TargetKind, &row.WeaponCode, &row.VehicleCode, &row.KitCode, &row.ChatText, &row.Params); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var weaponTemplate = template.Must(template.New("weapon").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{ .Name }} - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "weapons" }}
<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>{{ .Name }}</strong><span class="muted mono">{{ .Code }}</span></div>
<div class="cards">
  <div class="card gold"><span>Kills</span><strong>{{ .Kills }}</strong></div><div class="card"><span>Teamkills</span><strong>{{ .Teamkills }}</strong></div>
  <div class="card"><span>Users</span><strong>{{ .Users }}</strong></div><div class="card"><span>Rounds</span><strong>{{ .Rounds }}</strong></div>
  <div class="card green"><span>Kills / User</span><strong>{{ printf "%.2f" .KillsPerUser }}</strong></div><div class="card green"><span>Kills / Round</span><strong>{{ printf "%.2f" .KillsPerRound }}</strong></div>
  <div class="card red"><span>TK Rate</span><strong>{{ printf "%.1f" .TKRate }}%</strong></div>
  <div class="card"><span>First Used</span><strong>{{ .FirstUsed }}</strong></div><div class="card"><span>Last Used</span><strong>{{ .LastUsed }}</strong></div>
</div>
<div class="grid2">
<section class="panel"><h3>Top Killers</h3>{{ template "playerCountTable" .Killers }}</section>
<section class="panel"><h3>Top Victims</h3>{{ template "playerCountTable" .Victims }}</section>
<section class="panel"><h3>Kills By Map</h3>{{ template "mapCountTable" .Maps }}</section>
</div>
<section class="panel"><h3>Usage Trend (Monthly)</h3>{{ codeMapColumnChart .Trend }}</section>
{{ template "eventTable" .Events }}
</main>{{ siteFooter }}</body></html>`))

var vehicleTemplate = template.Must(template.New("vehicle").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{ .Name }} - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "vehicles" }}
<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>{{ .Name }}</strong><span class="muted mono">{{ .Code }}</span></div>
<div class="cards">
  <div class="card"><span>Entries</span><strong>{{ .Entries }}</strong></div><div class="card gold"><span>Kills</span><strong>{{ .Kills }}</strong></div>
  <div class="card"><span>Teamkills</span><strong>{{ .Teamkills }}</strong></div><div class="card"><span>Users</span><strong>{{ .Users }}</strong></div>
  <div class="card"><span>Rounds</span><strong>{{ .Rounds }}</strong></div><div class="card red"><span>Destroyed</span><strong>{{ .Destroyed }}</strong></div>
  <div class="card green"><span>Kills / Entry</span><strong>{{ printf "%.2f" .KillsPerEntry }}</strong></div><div class="card red"><span>Destroyed / Entry</span><strong>{{ printf "%.2f" .DestroyedPerEntry }}</strong></div>
  <div class="card green"><span>Kills / User</span><strong>{{ printf "%.2f" .KillsPerUser }}</strong></div>
</div>
<div class="grid2">
<section class="panel"><h3>Operator Pressure</h3>{{ .OperatorBars }}</section>
<section class="panel"><h3>Usage By Map</h3>{{ .MapBars }}</section>
<section class="panel"><h3>Kills By Map</h3>{{ .KillBars }}</section>
</div>
<div class="grid2">
<section class="panel"><h3>Top Operators</h3>{{ template "operatorTable" .Operators }}</section>
<section class="panel"><h3>Destroyers</h3>{{ template "playerCountTable" .Destroyers }}</section>
<section class="panel"><h3>Event Mix</h3>{{ template "mapCountTable" .EventMix }}</section>
</div>
<h2>Map Effectiveness</h2><table><thead><tr><th>Map</th><th>Activity Events</th></tr></thead><tbody>{{ range .Maps }}<tr><td><a class="link" href="{{ mapURL .Name }}">{{ displayCode .Name }}</a> <span class="muted mono">{{ .Name }}</span></td><td>{{ .Count }}</td></tr>{{ else }}<tr><td colspan="2" class="muted">No map rows.</td></tr>{{ end }}</tbody></table>
<h2>Kill Maps</h2><table><thead><tr><th>Map</th><th>Kills</th><th>Kills/Round</th></tr></thead><tbody>{{ range .KillMaps }}<tr><td><a class="link" href="{{ mapURL .Name }}">{{ displayCode .Name }}</a> <span class="muted mono">{{ .Name }}</span></td><td>{{ .Count }}</td><td>{{ printf "%.2f" (rate .Count $.Rounds) }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No kill map rows.</td></tr>{{ end }}</tbody></table>
<section class="panel"><h3>Usage Trend (Monthly)</h3>{{ codeMapColumnChart .Trend }}</section>
{{ template "eventTable" .Events }}
</main>{{ siteFooter }}</body></html>`))

var roundTemplate = template.Must(template.New("round").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Round - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "rounds" }}
<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>{{ displayCode .Summary.MapName }}</strong><span class="muted mono">{{ .Summary.RoundHash }}</span></div>
<div class="round-overview">
<section class="panel round-summary"><h3>Round Summary</h3>
  <div class="round-kpis">
    <div class="round-stat-card"><span>Players</span><strong>{{ .Summary.PlayerCount }}</strong></div>
    <div class="round-stat-card gold"><span>Kills</span><strong>{{ .Summary.Kills }}</strong></div>
    <div class="round-stat-card"><span>Events</span><strong>{{ .Summary.EventCount }}</strong></div>
    <div class="round-stat-card green text"><span>Outcome</span><strong>{{ roundOutcome .Summary.WinningTeamID .Summary.VictoryType }}</strong></div>
    <div class="round-stat-card"><span>Duration</span><strong>{{ .Summary.Duration }}s</strong></div>
    <div class="round-stat-card text"><span>Ended</span><strong>{{ .Summary.EndedAt }}</strong></div>
  </div>
</section>
<section class="panel round-standouts"><h3>Round Standouts</h3><div class="round-highlights">
{{ range .Highlights }}<div class="round-highlight"><span>{{ .Label }}</span><strong>{{ .Value }}</strong><small>{{ .Detail }}</small></div>{{ else }}<div class="muted">No standouts.</div>{{ end }}
</div></section>
</div>
<section class="panel round-kill-pace"><h3>Kill Pace</h3><div class="chart">{{ .KillPaceChart }}</div></section>
<div class="round-chart-grid">
<section class="panel"><h3>Team Kill Split</h3>{{ .TeamBars }}</section>
<section class="panel"><h3>Player Contribution</h3>{{ .PlayerBars }}</section>
<section class="panel"><h3>Event Mix</h3>{{ .EventBars }}</section>
<section class="panel"><h3>Weapon Impact</h3>{{ .WeaponBars }}</section>
<section class="panel"><h3>Vehicle Activity</h3>{{ .VehicleBars }}</section>
</div>
<div class="round-detail-grid">
<section class="panel round-wide"><h3>Team Box Score</h3><table><thead><tr><th>Team</th><th>Players</th><th>Score</th><th>Kills</th><th>Deaths</th><th>K/D</th><th>TK</th><th>Captures</th><th>Heals</th><th>Repairs</th></tr></thead><tbody>
{{ range .Teams }}<tr><td>{{ teamName .TeamID }}</td><td>{{ .Players }}</td><td>{{ .Score }}</td><td>{{ .Kills }}</td><td>{{ .Deaths }}</td><td>{{ formatKD .Kills .Deaths }}</td><td>{{ .Teamkills }}</td><td>{{ .Captures }}</td><td>{{ .Heals }}</td><td>{{ .Repairs }}</td></tr>{{ else }}<tr><td colspan="10" class="muted">No team stats.</td></tr>{{ end }}
</tbody></table></section>
<section class="panel"><h3>Map Average Comparison</h3><table><thead><tr><th>Metric</th><th>This Round</th><th>Map Avg</th><th>Delta</th></tr></thead><tbody>{{ range .MapComparison }}<tr><td>{{ .Metric }}</td><td>{{ .Current }}</td><td>{{ .Average }}</td><td>{{ .Delta }}</td></tr>{{ else }}<tr><td colspan="4" class="muted">No map comparison available.</td></tr>{{ end }}</tbody></table></section>
<section class="panel"><h3>Round Narrative</h3><table><thead><tr><th>Moment</th><th>Result</th><th>Detail</th></tr></thead><tbody>{{ range .Narratives }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No narrative moments.</td></tr>{{ end }}</tbody></table></section>
<section class="panel"><h3>Swing Moments</h3><table><thead><tr><th>Window</th><th>Team</th><th>Kills</th><th>Players</th></tr></thead><tbody>{{ range .SwingMoments }}<tr><td>{{ .Window }}</td><td>{{ teamName .TeamID }}</td><td>{{ .Kills }}</td><td class="wrap">{{ .Players }}</td></tr>{{ else }}<tr><td colspan="4" class="muted">No 20-second multi-kill swings detected.</td></tr>{{ end }}</tbody></table></section>
<section class="panel"><h3>Streak Timeline</h3><table><thead><tr><th>Player</th><th>Best Run</th><th>Total Kills</th></tr></thead><tbody>{{ range .Streaks }}<tr><td>{{ if .ID }}<a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a>{{ else }}{{ .DisplayName }}{{ end }}</td><td>{{ .Count }}</td><td>{{ .Extra }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No kill streaks.</td></tr>{{ end }}</tbody></table></section>
</div>
<section class="panel round-table-panel"><div class="section-heading"><h3>Players</h3><span class="muted">{{ len .Players }} participants</span></div><table><thead><tr><th>Player</th><th>Type</th><th>Team</th><th>Contribution</th><th>Carry Kills</th><th>Carry Score</th><th>Score</th><th>Kills</th><th>Deaths</th><th>TK</th><th>Captures</th><th>Heals</th><th>Repairs</th></tr></thead><tbody>
{{ range .Players }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .Name }}</a></td><td>{{ playerKind .IsBot }}</td><td>{{ if .TeamID }}{{ teamName .TeamID }}{{ else }}<span class="muted">n/a</span>{{ end }}</td><td>{{ printf "%.1f" .Impact }}</td><td>{{ printf "%.1f" .CarryKills }}%</td><td>{{ printf "%.1f" .CarryScore }}%</td><td>{{ .Score }}</td><td>{{ .Kills }}</td><td>{{ .Deaths }}</td><td>{{ .Teamkills }}</td><td>{{ .Captures }}</td><td>{{ .Heals }}</td><td>{{ .Repairs }}</td></tr>{{ else }}<tr><td colspan="13" class="muted">No player stats for this round.</td></tr>{{ end }}
</tbody></table></section>
<section class="panel" id="combat-timeline">
<div class="section-heading"><h3>Combat Timeline</h3><span class="muted">Latest {{ .TimelineWindow }} events</span></div>
<form class="timeline-search" method="get" action="/round#combat-timeline">
<input type="hidden" name="id" value="{{ .Summary.ID }}">
<input type="search" name="timeline_q" value="{{ .TimelineQuery }}" placeholder="Search timeline">
<button type="submit">Search</button>
{{ if .TimelineQuery }}<a class="action-link" href="{{ roundURL .Summary.ID }}#combat-timeline">Clear</a>{{ end }}
</form>
<div class="timeline">
{{ range .Timeline }}<div class="timeline-row"><div class="mono muted">{{ .When }}</div><div class="timeline-kind">{{ displayCode .EventType }}</div><div class="timeline-summary">{{ eventSummary . }}</div><div class="timeline-meta">{{ if .WeaponCode }}{{ displayCode .WeaponCode }}{{ end }}{{ if .VehicleCode }}{{ if .WeaponCode }} / {{ end }}{{ displayCode .VehicleCode }}{{ end }}{{ if .KitCode }}{{ if or .WeaponCode .VehicleCode }} / {{ end }}{{ displayCode .KitCode }}{{ end }}</div></div>{{ else }}<div class="muted">No timeline events match.</div>{{ end }}
</div>
<div class="pager">{{ if .TimelinePager.PrevURL }}<a href="{{ .TimelinePager.PrevURL }}">&larr; Newer</a>{{ end }}<span>Page {{ .TimelinePager.Page }}</span>{{ if .TimelinePager.NextURL }}<a href="{{ .TimelinePager.NextURL }}">Older &rarr;</a>{{ end }}</div>
</section>
</main>{{ siteFooter }}</body></html>`))

// teamName maps BF1942 engine team ids to faction names: 1 is Axis, 2 is
// Allies. Accepts int or *int so templates can pass either directly.
func teamName(v interface{}) string {
	var id int
	switch t := v.(type) {
	case int:
		id = t
	case *int:
		if t == nil {
			return "Unknown"
		}
		id = *t
	default:
		return "Unknown"
	}
	switch id {
	case 0:
		return "Unknown"
	case 1:
		return "Axis"
	case 2:
		return "Allies"
	}
	return fmt.Sprintf("Team %d", id)
}

// roundOutcome renders "Allies — time limit" style outcome text from the
// winning team and the engine's endOfRound victoryType code.
func roundOutcome(winningTeamID *int, victoryType *int) string {
	winner := "Unknown"
	if winningTeamID != nil {
		if *winningTeamID == 0 {
			winner = "Draw"
		} else {
			winner = teamName(*winningTeamID)
		}
	}
	if label := victoryTypeLabel(victoryType); label != "" {
		return winner + " — " + label
	}
	return winner
}

// victoryTypeLabel maps the engine's endOfRound victoryType codes.
func victoryTypeLabel(victoryType *int) string {
	if victoryType == nil {
		return ""
	}
	switch *victoryType {
	case 1:
		return "tickets depleted"
	case 2:
		return "all flags captured"
	case 3:
		return "objectives completed"
	case 4:
		return "time limit"
	default:
		return fmt.Sprintf("victory type %d", *victoryType)
	}
}

func detailFuncMap() template.FuncMap {
	return template.FuncMap{
		"css": cssFunc, "favicon": faviconFunc,
		"displayCode":         displayCode,
		"eventSummary":        eventSummary,
		"participantCell":     participantCell,
		"pct":                 pct,
		"playerKind":          playerKind,
		"playerURL":           playerURL,
		"mapURL":              mapURL,
		"mapFilterURL":        mapFilterURL,
		"query":               queryEscape,
		"rank":                func(i int) int { return i + 1 },
		"add":                 func(a int, b int) int { return a + b },
		"teamName":            teamName,
		"roundOutcome":        roundOutcome,
		"roundURL":            roundURL,
		"selectedFilter":      selectedFilter,
		"formatKD":            formatKD,
		"rate":                ratio,
		"siteHeader":          siteHeader,
		"siteFooter":          siteFooter,
		"adminNav":            adminNav,
		"deref":               func(b *bool) bool { return b != nil && *b },
		"tkRate":              tkRate,
		"vehicleURL":          vehicleURL,
		"weaponURL":           weaponURL,
		"clanURL":             clanURL,
		"dict":                dict,
		"codeMapColumnChart":  codeMapColumnChart,
		"performanceChartSVG": performanceChartSVG,
	}
}

func dict(values ...any) map[string]any {
	result := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			continue
		}
		result[key] = values[i+1]
	}
	return result
}

func ratio(numerator int, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func tkRate(teamkills int, kills int) float64 {
	return ratio(teamkills, kills+teamkills) * 100
}

func comparisonRow(metric string, current float64, average float64, format string) mapComparisonRow {
	return mapComparisonRow{
		Metric:  metric,
		Current: fmt.Sprintf(format, current),
		Average: fmt.Sprintf(format, average),
		Delta:   fmt.Sprintf(format, current-average),
	}
}

func pct(value int, total int) int {
	if total <= 0 || value <= 0 {
		return 0
	}
	percent := value * 100 / total
	if percent < 2 {
		return 2
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func init() {
	template.Must(weaponTemplate.New("playerCountTable").Parse(playerCountTableHTML))
	template.Must(weaponTemplate.New("operatorTable").Parse(operatorTableHTML))
	template.Must(weaponTemplate.New("mapCountTable").Parse(mapCountTableHTML))
	template.Must(weaponTemplate.New("eventTable").Parse(eventTableHTML))
	template.Must(vehicleTemplate.New("playerCountTable").Parse(playerCountTableHTML))
	template.Must(vehicleTemplate.New("operatorTable").Parse(operatorTableHTML))
	template.Must(vehicleTemplate.New("mapCountTable").Parse(mapCountTableHTML))
	template.Must(vehicleTemplate.New("eventTable").Parse(eventTableHTML))
	template.Must(roundTemplate.New("playerCountTable").Parse(playerCountTableHTML))
	template.Must(roundTemplate.New("operatorTable").Parse(operatorTableHTML))
	template.Must(roundTemplate.New("mapCountTable").Parse(mapCountTableHTML))
	template.Must(roundTemplate.New("eventTable").Parse(eventTableHTML))
	for _, tmpl := range []*template.Template{mapsTemplate, mapTemplate, rivalryTemplate, kitsTemplate, supportTemplate} {
		template.Must(tmpl.New("playerCountTable").Parse(playerCountTableHTML))
		template.Must(tmpl.New("operatorTable").Parse(operatorTableHTML))
		template.Must(tmpl.New("mapCountTable").Parse(mapCountTableHTML))
		template.Must(tmpl.New("eventTable").Parse(eventTableHTML))
	}
}

const playerCountTableHTML = `<table><thead><tr><th>Player</th><th>Count</th></tr></thead><tbody>{{ range . }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Count }}</td></tr>{{ else }}<tr><td colspan="2" class="muted">No rows.</td></tr>{{ end }}</tbody></table>`
const operatorTableHTML = `<table><thead><tr><th>Player</th><th>Entries</th><th>Kills</th><th>Kills / Entry</th></tr></thead><tbody>{{ range . }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Count }}</td><td>{{ .Extra }}</td><td>{{ printf "%.2f" (rate .Extra .Count) }}</td></tr>{{ else }}<tr><td colspan="4" class="muted">No rows.</td></tr>{{ end }}</tbody></table>`
const mapCountTableHTML = `<table><thead><tr><th>Name</th><th>Count</th></tr></thead><tbody>{{ range . }}<tr><td>{{ displayCode .Name }} <span class="muted mono">{{ .Name }}</span></td><td>{{ .Count }}</td></tr>{{ else }}<tr><td colspan="2" class="muted">No rows.</td></tr>{{ end }}</tbody></table>`
const eventTableHTML = `<h2>Recent Events</h2><table><thead><tr><th>When</th><th>Map</th><th>Event</th><th>Summary</th><th>Actor</th><th>Target</th><th>Weapon</th><th>Vehicle</th></tr></thead><tbody>{{ range . }}<tr><td>{{ .When }}</td><td><a class="link" href="{{ mapURL .MapName }}">{{ displayCode .MapName }}</a></td><td>{{ .EventType }} <span class="muted mono">{{ .RawEventName }}</span></td><td class="wrap">{{ eventSummary . }}</td><td>{{ participantCell .ActorID .ActorName .ActorKind "all" }}</td><td>{{ participantCell .TargetID .TargetName .TargetKind "all" }}</td><td>{{ if .WeaponCode }}<a class="link" href="{{ weaponURL .WeaponCode }}">{{ displayCode .WeaponCode }}</a>{{ else }}{{ displayCode .WeaponCode }}{{ end }}</td><td>{{ if .VehicleCode }}<a class="link" href="{{ vehicleURL .VehicleCode }}">{{ displayCode .VehicleCode }}</a>{{ else }}{{ displayCode .VehicleCode }}{{ end }}</td></tr>{{ else }}<tr><td colspan="8" class="muted">No recent events.</td></tr>{{ end }}</tbody></table>`
