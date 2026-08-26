package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hootmeow/spawnpoint/internal/domain"
)

type pairCountRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Count       int    `json:"count"`
}

type mapStatRow struct {
	MapName string `json:"map_name"`
	Rounds  int    `json:"rounds"`
	Kills   int    `json:"kills"`
	Deaths  int    `json:"deaths"`
	Score   int    `json:"score"`
}

type playerRoundRow struct {
	When    string `json:"when"`
	MapName string `json:"map_name"`
	Kills   int    `json:"kills"`
	Deaths  int    `json:"deaths"`
	Score   int    `json:"score"`
	TeamID  *int   `json:"team_id,omitempty"`
}

type killFeedRow struct {
	When       string
	Type       string
	OpponentID string
	Opponent   string
	Weapon     string
	MapName    string
}

type weaponBreakdownRow struct {
	Weapon string
	Kills  int
	Deaths int
}

type killDistributionRow struct {
	Average     float64
	Median      float64
	Max         int
	Consistency float64
}

type playerPageData struct {
	GeneratedAt        time.Time
	DBError            string
	Summary            *playerSummaryRow
	KD                 string
	Distribution       killDistributionRow
	FirstBloods        int
	LongestKillStreak  int
	LongestDeathStreak int
	PerformanceChart   template.HTML
	KDChart            template.HTML
	WinLossChart       template.HTML
	ActivityChart      template.HTML
	Wins               int
	Losses             int
	MapBars            template.HTML
	WeaponBars         template.HTML
	KitBars            template.HTML
	VehicleBars        template.HTML
	Victims            []pairCountRow
	Nemeses            []pairCountRow
	Maps               []mapStatRow
	Rounds             []playerRoundRow
	KillFeed           []killFeedRow
	WeaponBreakdown    []weaponBreakdownRow
	VehicleBreakdown   []weaponBreakdownRow
	FormTrends         []keyValueRow
	SimilarPlayers     []playerSummaryRow
	BestMaps           []keyValueRow
	ToughMaps          []keyValueRow
	BestWeapons        []keyValueRow
	ProblemWeapons     []keyValueRow
	Aliases            []string
	IdentityLinked     bool
	PlaySeconds        int
	Medals             []medalCount
	RoundRows          []playerRoundRow
	RoundsPager        pager
	FeedPager          pager
}

// pager carries prev/next links for a paginated table; empty URLs hide the
// corresponding link.
type pager struct {
	Page    int
	PrevURL string
	NextURL string
}

type medalCount struct {
	Category string
	Count    int
}

func (s *apiServer) playerPage(w http.ResponseWriter, r *http.Request) {
	playerID := sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("id")))
	if playerID == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	// This page runs a couple dozen queries (perf chart, weapon/kit/vehicle
	// breakdowns, victims/nemeses, medal case, aliases); most are cheap
	// player_id-indexed lookups, but the weapon/vehicle breakdowns share the
	// same site-wide vehicle_code_list_mv-dependent cost as /weapons and
	// /vehicles. Widened for the same reason as those pages.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data := playerPageData{GeneratedAt: time.Now().UTC()}
	setError := func(err error) {
		if err != nil && data.DBError == "" {
			data.DBError = dbErrorMessage(err)
		}
	}

	summary, err := s.fetchPlayerByID(ctx, playerID)
	setError(err)
	if summary == nil {
		http.NotFound(w, r)
		return
	}
	data.Summary = summary
	data.KD = formatKD(summary.Kills, summary.Deaths)
	data.KDChart = donutChartSVG(summary.Kills, summary.Deaths)

	rounds, err := s.fetchPlayerRounds(ctx, playerID, 40, 0)
	setError(err)
	data.Rounds = rounds
	points := make([]seriesPoint, 0, len(rounds))
	for i := len(rounds) - 1; i >= 0; i-- {
		round := rounds[i]
		points = append(points, seriesPoint{Label: shortWhen(round.When), Kills: round.Kills, Deaths: round.Deaths, Score: round.Score})
	}
	data.PerformanceChart = performanceChartSVG(points)

	breakdown, breakdownOK, err := s.fetchPlayerBreakdownMV(ctx, playerID)
	setError(err)

	var weaponBreakdown, vehicleBreakdown []weaponBreakdownRow
	if breakdownOK {
		weaponItems := make([]barItem, 0, len(breakdown.Weapons))
		for i, weapon := range breakdown.Weapons {
			weaponBreakdown = append(weaponBreakdown, weaponBreakdownRow{Weapon: weapon.Code, Kills: weapon.Kills, Deaths: weapon.Deaths})
			if i < 12 && weapon.Kills > 0 {
				weaponItems = append(weaponItems, barItem{Label: weapon.Code, Value: weapon.Kills})
			}
		}
		data.WeaponBars = barListHTML(weaponItems, colorKills)

		kitItems := make([]barItem, 0, 8)
		for i, kit := range breakdown.KitPickups {
			if i >= 8 {
				break
			}
			kitItems = append(kitItems, barItem{Label: kit.Code, Value: kit.Count})
		}
		data.KitBars = barListHTML(kitItems, colorScore)

		vehicleItems := make([]barItem, 0, 8)
		for i, vehicle := range breakdown.VehiclePickups {
			if i >= 8 {
				break
			}
			vehicleItems = append(vehicleItems, barItem{Label: vehicle.Code, Value: vehicle.Count})
		}
		data.VehicleBars = barListHTML(vehicleItems, colorInfo)
		for _, vehicle := range breakdown.Vehicles {
			vehicleBreakdown = append(vehicleBreakdown, weaponBreakdownRow{Weapon: vehicle.Code, Kills: vehicle.Kills, Deaths: vehicle.Deaths})
		}

		for i, victim := range breakdown.Victims {
			if i >= 8 {
				break
			}
			data.Victims = append(data.Victims, pairCountRow{ID: victim.ID, DisplayName: victim.Name, Count: victim.Count})
		}
		for i, nemesis := range breakdown.Nemeses {
			if i >= 8 {
				break
			}
			data.Nemeses = append(data.Nemeses, pairCountRow{ID: nemesis.ID, DisplayName: nemesis.Name, Count: nemesis.Count})
		}
	} else {
		weapons, wErr := s.fetchWeaponStats(ctx, 12, playerID, "", "")
		setError(wErr)
		weaponItems := make([]barItem, 0, len(weapons))
		for _, weapon := range weapons {
			if weapon.Kills+weapon.Teamkills == 0 {
				continue
			}
			weaponItems = append(weaponItems, barItem{Label: weapon.WeaponCode, Value: weapon.Kills + weapon.Teamkills})
		}
		data.WeaponBars = barListHTML(weaponItems, colorKills)

		kits, kErr := s.fetchPlayerCodeCounts(ctx, playerID, "pickup_kit", "kit_code", 8)
		setError(kErr)
		data.KitBars = barListHTML(kits, colorScore)

		vehicles, vErr := s.fetchPlayerCodeCounts(ctx, playerID, "enter_vehicle", "vehicle_code", 8)
		setError(vErr)
		data.VehicleBars = barListHTML(vehicles, colorInfo)

		data.Victims, err = s.fetchKillPairs(ctx, playerID, true, 8)
		setError(err)
		data.Nemeses, err = s.fetchKillPairs(ctx, playerID, false, 8)
		setError(err)
		weaponBreakdown, err = s.fetchPlayerWeaponBreakdown(ctx, playerID)
		setError(err)
		vehicleBreakdown, err = s.fetchPlayerVehicleBreakdown(ctx, playerID)
		setError(err)
	}

	data.Maps, err = s.fetchPlayerMapStats(ctx, playerID)
	setError(err)
	mapItems := make([]barItem, 0, len(data.Maps))
	for _, row := range data.Maps {
		mapItems = append(mapItems, barItem{Label: displayCode(row.MapName), Value: row.Kills})
	}
	data.MapBars = barListHTML(mapItems, colorKills)
	// Long tables are paginated at 10 rows so profile pages stay short and
	// the queries stay cheap; page numbers come from ?feed= and ?rp=.
	const tablePage = 10
	feedPage := pageParam(r, "feed")
	roundsPage := pageParam(r, "rp")

	feed, err := s.fetchPlayerKillFeed(ctx, playerID, tablePage+1, (feedPage-1)*tablePage)
	setError(err)
	data.KillFeed, data.FeedPager = paginate(feed, tablePage, feedPage, playerID, "feed", roundsPage, "rp", "killfeed")

	roundRows, err := s.fetchPlayerRounds(ctx, playerID, tablePage+1, (roundsPage-1)*tablePage)
	setError(err)
	data.RoundRows, data.RoundsPager = paginate(roundRows, tablePage, roundsPage, playerID, "rp", feedPage, "feed", "rounds")
	data.WeaponBreakdown = weaponBreakdown
	data.VehicleBreakdown = vehicleBreakdown
	data.BestMaps, data.ToughMaps = playerMapHighlights(data.Maps)
	data.BestWeapons, data.ProblemWeapons = playerWeaponHighlights(data.WeaponBreakdown)
	data.Distribution, err = s.fetchPlayerKillDistribution(ctx, playerID)
	setError(err)
	data.FirstBloods, err = s.fetchPlayerFirstBloods(ctx, playerID)
	setError(err)
	data.LongestKillStreak, err = s.fetchPlayerStreak(ctx, playerID, true)
	setError(err)
	data.LongestDeathStreak, err = s.fetchPlayerStreak(ctx, playerID, false)
	setError(err)
	data.FormTrends, err = s.fetchPlayerFormTrends(ctx, playerID)
	setError(err)
	data.SimilarPlayers, err = s.fetchSimilarPlayers(ctx, playerID, 8)
	setError(err)
	data.Aliases, data.IdentityLinked, err = s.fetchPlayerAliases(ctx, playerID, summary.DisplayName)
	setError(err)
	data.PlaySeconds, err = s.fetchPlayerPlaySeconds(ctx, playerID)
	setError(err)
	data.Medals, err = s.fetchPlayerMedals(ctx, playerID)
	setError(err)
	data.Wins, data.Losses, err = s.fetchPlayerWinLoss(ctx, playerID)
	setError(err)
	data.WinLossChart = winLossDonutSVG(data.Wins, data.Losses)
	daily, err := s.fetchPlayerDailyKills(ctx, playerID, 30)
	setError(err)
	data.ActivityChart = columnChartSVG(daily, colorKills, "Kills per day over the last 30 days")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := playerTemplate.Execute(w, data); err != nil {
		log.Printf("render player page failed: %v", err)
	}
}

// pageParam reads a 1-based page number from the query string, defaulting to
// 1 and clamped so hostile values can't request absurd offsets.
func pageParam(r *http.Request, name string) int {
	page, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || page < 1 {
		return 1
	}
	if page > 10000 {
		return 10000
	}
	return page
}

// paginate trims a limit+1 fetch to the page size and builds prev/next links
// that preserve the other table's page and jump back to the table's anchor.
func paginate[T any](rows []T, size int, page int, playerID string, param string, otherPage int, otherParam string, anchor string) ([]T, pager) {
	hasMore := len(rows) > size
	if hasMore {
		rows = rows[:size]
	}
	link := func(p int) string {
		u := "/player?id=" + url.QueryEscape(playerID) + "&" + param + "=" + strconv.Itoa(p)
		if otherPage > 1 {
			u += "&" + otherParam + "=" + strconv.Itoa(otherPage)
		}
		return u + "#" + anchor
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

// fetchPlayerWinLoss counts decided rounds the player was on the winning or
// losing team of; draws and rounds without a recorded winner are skipped.
func (s *apiServer) fetchPlayerWinLoss(ctx context.Context, playerID string) (int, int, error) {
	var wins, losses int
	err := s.db.QueryRowContext(ctx, `
		select
			coalesce(count(*) filter (where s.team_id = r.winning_team_id), 0)::int,
			coalesce(count(*) filter (where s.team_id <> r.winning_team_id), 0)::int
		from round_player_stats s
		join rounds r on r.id = s.round_id
		where s.player_id = nullif($1,'')::uuid
		  and s.team_id is not null
		  and r.winning_team_id in (1, 2)
	`, playerID).Scan(&wins, &losses)
	return wins, losses, err
}

// fetchPlayerDailyKills sums the player's kills per day over the last N days
// for the activity column chart; days without a round are omitted.
func (s *apiServer) fetchPlayerDailyKills(ctx context.Context, playerID string, days int) ([]barItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		select to_char(date_trunc('day', coalesce(r.ended_at, g.started_at)), 'MM-DD'),
			coalesce(sum(s.kills), 0)::int
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		where s.player_id = nullif($1,'')::uuid
		  and coalesce(r.ended_at, g.started_at) >= now() - ($2 || ' days')::interval
		group by 1
		order by 1
	`, playerID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []barItem
	for rows.Next() {
		var item barItem
		if err := rows.Scan(&item.Label, &item.Value); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayerFormTrends(ctx context.Context, playerID string) ([]keyValueRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with ranked as (
			select s.*, row_number() over (order by coalesce(r.ended_at, g.started_at) desc) as rn
			from round_player_stats s
			join rounds r on r.id = s.round_id
			join games g on g.id = r.game_id
			where s.player_id = nullif($1,'')::uuid
		),
		windows as (
			select 'Last 10' as label, * from ranked where rn <= 10
			union all select 'Last 25' as label, * from ranked where rn <= 25
			union all select 'Last 50' as label, * from ranked where rn <= 50
		)
		select label,
			coalesce(sum(kills), 0)::int,
			coalesce(sum(deaths), 0)::int,
			coalesce(sum(score), 0)::int,
			count(*)::int
		from windows
		group by label
		order by case label when 'Last 10' then 1 when 'Last 25' then 2 else 3 end
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []keyValueRow
	for rows.Next() {
		var label string
		var kills, deaths, score, rounds int
		if err := rows.Scan(&label, &kills, &deaths, &score, &rounds); err != nil {
			return nil, err
		}
		result = append(result, keyValueRow{
			Label:  label,
			Value:  fmt.Sprintf("%s K/D", formatKD(kills, deaths)),
			Detail: fmt.Sprintf("%d kills, %d deaths, %d score over %d rounds", kills, deaths, score, rounds),
		})
	}
	return result, rows.Err()
}

func (s *apiServer) fetchSimilarPlayers(ctx context.Context, playerID string, limit int) ([]playerSummaryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with totals as (
			select p.id, p.display_name,
				count(distinct s.round_id)::int rounds,
				coalesce(sum(s.score), 0)::int score,
				coalesce(sum(s.kills), 0)::int kills,
				coalesce(sum(s.deaths), 0)::int deaths,
				coalesce(sum(s.teamkills), 0)::int teamkills,
				coalesce(sum(s.heals), 0)::int heals,
				coalesce(sum(s.repairs), 0)::int repairs,
				case when bool_or(s.is_bot is true) then true when bool_or(s.is_bot is false) then false else null end as is_bot,
				coalesce(to_char(max(coalesce(r.ended_at, g.started_at)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as last_seen
			from players p
			left join round_player_stats s on s.player_id = p.id
			left join rounds r on r.id = s.round_id
			left join games g on g.id = r.game_id
			group by p.id, p.display_name
		),
		self as (select * from totals where id = nullif($1,'')::uuid)
		select t.id::text, t.display_name, t.is_bot, t.rounds, t.score, t.kills, t.deaths, t.teamkills, t.heals, t.repairs, t.last_seen
		from totals t, self
		where t.id <> self.id
		order by
			abs((t.kills::float / greatest(t.rounds, 1)) - (self.kills::float / greatest(self.rounds, 1))) +
			abs((t.deaths::float / greatest(t.rounds, 1)) - (self.deaths::float / greatest(self.rounds, 1))) +
			abs((t.score::float / greatest(t.rounds, 1)) - (self.score::float / greatest(self.rounds, 1))) asc
		limit $2
	`, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []playerSummaryRow
	for rows.Next() {
		var row playerSummaryRow
		var isBot sql.NullBool
		if err := rows.Scan(&row.ID, &row.DisplayName, &isBot, &row.Rounds, &row.Score, &row.Kills, &row.Deaths, &row.Teamkills, &row.Heals, &row.Repairs, &row.LastSeen); err != nil {
			return nil, err
		}
		if isBot.Valid {
			row.IsBot = &isBot.Bool
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayerKillFeed(ctx context.Context, playerID string, limit int, offset int) ([]killFeedRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with feed as (
			select
				coalesce(to_char(coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as event_time_text,
				case
					when coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills') then 'kill'
					when coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid and lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills') then 'teamkill'
					when coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills') then 'death'
					when coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid and lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills') then 'teamkilled'
					else ''
				end as feed_type,
				case when coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid then coalesce(target.id::text, '') else coalesce(actor.id::text, '') end as opponent_id,
				case when coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid then coalesce(target.display_name, 'Unknown') else coalesce(actor.display_name, 'Unknown') end as opponent,
				coalesce(e.weapon_code, '') as weapon_code,
				coalesce(g.map_name, '') as map_name,
				coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) as event_time,
				e.sequence_no
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
			left join players actor on actor.id = coalesce(e.actor_player_id, actor_stats.player_id) and not actor.hidden
			left join players target on target.id = coalesce(e.target_player_id, target_stats.player_id) and not target.hidden
			where e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
			and (coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid or coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid)
		)
		select event_time_text, feed_type, opponent_id, opponent, weapon_code, map_name
		from feed
		where feed_type <> ''
		order by event_time desc, sequence_no desc
		limit $2 offset $3
	`, playerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []killFeedRow
	for rows.Next() {
		var row killFeedRow
		if err := rows.Scan(&row.When, &row.Type, &row.OpponentID, &row.Opponent, &row.Weapon, &row.MapName); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayerWeaponBreakdown(ctx context.Context, playerID string) ([]weaponBreakdownRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with vehicle_codes as (
			select code from vehicle_code_list_mv
		)
		select
			e.weapon_code as weapon,
			count(*) filter (where coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid)::int as kills,
			count(*) filter (where coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid)::int as deaths
		from events e
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
		where e.event_type = 'score'
		and e.vehicle_code is null
		and nullif(e.weapon_code, '') is not null
		and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and (coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid or coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid)
		group by 1
		order by kills desc, deaths desc, weapon asc
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []weaponBreakdownRow
	for rows.Next() {
		var row weaponBreakdownRow
		if err := rows.Scan(&row.Weapon, &row.Kills, &row.Deaths); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayerVehicleBreakdown(ctx context.Context, playerID string) ([]weaponBreakdownRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with vehicle_codes as (
			select code from vehicle_code_list_mv
		)
		select
			regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, ''), 'unknown'), '_PCO[0-9]+$', '') as vehicle,
			count(*) filter (where coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid)::int as kills,
			count(*) filter (where coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid)::int as deaths
		from events e
		left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
		where e.event_type = 'score'
		and (e.vehicle_code is not null or coalesce(e.weapon_code, '') in (select code from vehicle_codes))
		and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
		and (coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid or coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid)
		group by 1
		order by kills desc, deaths desc, vehicle asc
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []weaponBreakdownRow
	for rows.Next() {
		var row weaponBreakdownRow
		if err := rows.Scan(&row.Weapon, &row.Kills, &row.Deaths); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayerKillDistribution(ctx context.Context, playerID string) (killDistributionRow, error) {
	var result killDistributionRow
	var median sql.NullFloat64
	var stddev sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		select coalesce(avg(kills), 0)::float8, percentile_cont(0.5) within group (order by kills), coalesce(max(kills), 0)::int, stddev_samp(kills)
		from round_player_stats
		where player_id = nullif($1,'')::uuid
	`, playerID).Scan(&result.Average, &median, &result.Max, &stddev)
	if median.Valid {
		result.Median = median.Float64
	}
	if stddev.Valid {
		result.Consistency = stddev.Float64
	}
	return result, err
}

func (s *apiServer) fetchPlayerFirstBloods(ctx context.Context, playerID string) (int, error) {
	// player_first_bloods_mv (migration 0011) precomputes this site-wide
	// (every round's first kill, grouped by player) instead of re-scanning
	// every score event on every /player page load.
	var count int
	err := s.db.QueryRowContext(ctx, `select first_bloods from player_first_bloods_mv where player_id = nullif($1,'')::uuid`, playerID).Scan(&count)
	if err == nil {
		return count, nil
	}
	if err != sql.ErrNoRows && !strings.Contains(err.Error(), "does not exist") {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx, `
		with first_kills as (
			select distinct on (e.round_id) e.round_id, coalesce(e.actor_player_id::text, actor_stats.player_id::text) as actor_id
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			where e.round_id is not null and e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
			order by e.round_id, coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval), e.sequence_no
		)
		select count(*)::int from first_kills where actor_id = $1
	`, playerID).Scan(&count)
	return count, err
}

func (s *apiServer) fetchPlayerStreak(ctx context.Context, playerID string, kills bool) (int, error) {
	groupOnDeath := "is_death"
	countColumn := "is_kill"
	if !kills {
		groupOnDeath = "is_kill"
		countColumn = "is_death"
	}
	var streak int
	err := s.db.QueryRowContext(ctx, `
		with stream as (
			select
				(coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')) as is_kill,
				(coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')) as is_death,
				coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) as event_time,
				e.sequence_no
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_stats on actor_stats.round_id = e.round_id and actor_stats.slot_id = e.actor_slot_id
			left join round_player_stats target_stats on target_stats.round_id = e.round_id and target_stats.slot_id = e.target_slot_id
			where e.event_type = 'score'
			and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
			and (coalesce(e.actor_player_id, actor_stats.player_id) = nullif($1,'')::uuid or coalesce(e.target_player_id, target_stats.player_id) = nullif($1,'')::uuid)
		),
		islands as (
			select is_kill, is_death, sum(case when `+groupOnDeath+` then 1 else 0 end) over (order by event_time, sequence_no) as island
			from stream
		),
		runs as (
			select count(*)::int as run_length from islands where `+countColumn+` group by island
		)
		select coalesce(max(run_length), 0)::int from runs
	`, playerID).Scan(&streak)
	return streak, err
}

func (s *apiServer) fetchPlayerRounds(ctx context.Context, playerID string, limit int, offset int) ([]playerRoundRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(to_char(coalesce(r.ended_at, g.started_at) at time zone 'utc', 'YYYY-MM-DD HH24:MI'), ''),
			coalesce(g.map_name, ''),
			s.kills,
			s.deaths,
			s.score,
			s.team_id
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		where s.player_id = nullif($1,'')::uuid
		order by g.started_at desc, r.round_index desc
		limit $2 offset $3
	`, playerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []playerRoundRow{}
	for rows.Next() {
		var row playerRoundRow
		var teamID sql.NullInt64
		if err := rows.Scan(&row.When, &row.MapName, &row.Kills, &row.Deaths, &row.Score, &teamID); err != nil {
			return nil, err
		}
		if teamID.Valid {
			team := int(teamID.Int64)
			row.TeamID = &team
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// fetchKillPairs returns who the player killed most (asVictims=true) or who
// killed the player most (asVictims=false), linked through slot ids.
func (s *apiServer) fetchKillPairs(ctx context.Context, playerID string, asVictims bool, limit int) ([]pairCountRow, error) {
	selfSide, otherSide := "actor", "target"
	if !asVictims {
		selfSide, otherSide = "target", "actor"
	}
	query := fmt.Sprintf(`
		select p.id::text, p.display_name, count(*)::int
		from events e
		join round_player_stats self_stats on self_stats.round_id = e.round_id and self_stats.slot_id = e.%s_slot_id
		join round_player_stats other_stats on other_stats.round_id = e.round_id and other_stats.slot_id = e.%s_slot_id
		join players p on p.id = other_stats.player_id and not p.hidden
		where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'tk')
		and self_stats.player_id = nullif($1,'')::uuid
		group by p.id, p.display_name
		order by count(*) desc, p.display_name asc
		limit $2
	`, selfSide, otherSide)
	rows, err := s.db.QueryContext(ctx, query, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []pairCountRow{}
	for rows.Next() {
		var row pairCountRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// fetchPlayerCodeCounts counts events of one type per code column (kit_code,
// vehicle_code) for a player. Column names are fixed by the callers.
func (s *apiServer) fetchPlayerCodeCounts(ctx context.Context, playerID string, eventType string, codeColumn string, limit int) ([]barItem, error) {
	query := fmt.Sprintf(`
		select e.%s, count(*)::int
		from events e
		join round_player_stats self_stats on self_stats.round_id = e.round_id and self_stats.slot_id = e.actor_slot_id
		where e.event_type = $1
		and e.%s is not null
		and e.%s not like '%%Soldier'
		and self_stats.player_id = nullif($2,'')::uuid
		group by e.%s
		order by count(*) desc, e.%s asc
		limit $3
	`, codeColumn, codeColumn, codeColumn, codeColumn, codeColumn)
	rows, err := s.db.QueryContext(ctx, query, eventType, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []barItem{}
	for rows.Next() {
		var item barItem
		if err := rows.Scan(&item.Label, &item.Value); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type playerCodeBreakdown struct {
	Code   string `json:"code"`
	Kills  int    `json:"kills"`
	Deaths int    `json:"deaths"`
}

type playerCodeCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type playerPairCount struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type playerBreakdown struct {
	Weapons        []playerCodeBreakdown
	Vehicles       []playerCodeBreakdown
	KitPickups     []playerCodeCount
	VehiclePickups []playerCodeCount
	Victims        []playerPairCount
	Nemeses        []playerPairCount
}

// fetchPlayerBreakdownMV is the fast path for /player's weapon/vehicle/kit/
// victim/nemesis panels: player_breakdown_mv (migration 0011) precomputes
// all six in one row per player, verified against the live queries it
// replaces (fetchWeaponStats, fetchPlayerWeaponBreakdown,
// fetchPlayerVehicleBreakdown, fetchPlayerCodeCounts x2, fetchKillPairs x2).
// ok is false (falls back to the live queries) on an older install that
// hasn't applied that migration yet, or a player with no row (shouldn't
// happen — the view is built from a left join off players — but the code
// degrades gracefully either way).
func (s *apiServer) fetchPlayerBreakdownMV(ctx context.Context, playerID string) (playerBreakdown, bool, error) {
	var weaponsJSON, vehiclesJSON, kitJSON, vehiclePickupsJSON, victimsJSON, nemesesJSON []byte
	err := s.db.QueryRowContext(ctx, `
		select weapons, vehicles, kit_pickups, vehicle_pickups, victims, nemeses
		from player_breakdown_mv where player_id = nullif($1,'')::uuid
	`, playerID).Scan(&weaponsJSON, &vehiclesJSON, &kitJSON, &vehiclePickupsJSON, &victimsJSON, &nemesesJSON)
	if err != nil {
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "does not exist") {
			return playerBreakdown{}, false, nil
		}
		return playerBreakdown{}, false, err
	}
	var b playerBreakdown
	for _, pair := range []struct {
		raw []byte
		dst any
	}{
		{weaponsJSON, &b.Weapons},
		{vehiclesJSON, &b.Vehicles},
		{kitJSON, &b.KitPickups},
		{vehiclePickupsJSON, &b.VehiclePickups},
		{victimsJSON, &b.Victims},
		{nemesesJSON, &b.Nemeses},
	} {
		if err := json.Unmarshal(pair.raw, pair.dst); err != nil {
			return playerBreakdown{}, false, err
		}
	}
	return b, true, nil
}

func (s *apiServer) fetchPlayerMapStats(ctx context.Context, playerID string) ([]mapStatRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(nullif(g.map_name, ''), 'unknown'),
			count(*)::int as rounds,
			coalesce(sum(s.kills), 0)::int,
			coalesce(sum(s.deaths), 0)::int,
			coalesce(sum(s.score), 0)::int
		from round_player_stats s
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		where s.player_id = nullif($1,'')::uuid
		group by 1
		order by rounds desc, 1 asc
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []mapStatRow{}
	for rows.Next() {
		var row mapStatRow
		if err := rows.Scan(&row.MapName, &row.Rounds, &row.Kills, &row.Deaths, &row.Score); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// fetchPlayerAliases returns the other nicknames this player has used and
// whether the row is anchored to a BF1942 keyhash identity.
func (s *apiServer) fetchPlayerAliases(ctx context.Context, playerID string, displayName string) ([]string, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select alias from player_aliases
		where player_id = $1
		order by times_seen desc, last_seen_at desc nulls last
		limit 12
	`, playerID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var aliases []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, false, err
		}
		if alias != displayName {
			aliases = append(aliases, alias)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	var linked bool
	err = s.db.QueryRowContext(ctx, `
		select exists (
			select 1 from player_identities
			where player_id = $1 and identity_type = 'bf_keyhash'
		)
	`, playerID).Scan(&linked)
	return aliases, linked, err
}

func (s *apiServer) fetchPlayerPlaySeconds(ctx context.Context, playerID string) (int, error) {
	var seconds int
	err := s.db.QueryRowContext(ctx, `
		select coalesce(sum(play_seconds), 0)::int from round_player_stats where player_id = nullif($1,'')::uuid
	`, playerID).Scan(&seconds)
	return seconds, err
}

func (s *apiServer) fetchPlayerMedals(ctx context.Context, playerID string) ([]medalCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		select category, count(*)::int
		from round_awards
		where player_id = nullif($1,'')::uuid
		group by category
		order by count(*) desc, category asc
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var medals []medalCount
	for rows.Next() {
		var medal medalCount
		if err := rows.Scan(&medal.Category, &medal.Count); err != nil {
			return nil, err
		}
		medals = append(medals, medal)
	}
	return medals, rows.Err()
}

// formatPlayTime renders accumulated seconds as "37h 12m" / "48m" / "—".
func formatPlayTime(seconds int) string {
	if seconds <= 0 {
		return "—"
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", seconds)
}

// scorePerMinute guards the divide; sub-minute totals are not a rate.
func scorePerMinute(score int, seconds int) string {
	if seconds < 60 {
		return "—"
	}
	return fmt.Sprintf("%.2f", float64(score)/(float64(seconds)/60))
}

// medalLabel maps round-award categories to display names.
func medalLabel(category string) string {
	if label := domain.MedalLabel(category); label != category {
		return label
	}
	return displayCode(category)
}

func formatKD(kills int, deaths int) string {
	if deaths == 0 {
		if kills == 0 {
			return "—"
		}
		return fmt.Sprintf("%d.00", kills)
	}
	return fmt.Sprintf("%.2f", float64(kills)/float64(deaths))
}

func kdScore(kills int, deaths int) float64 {
	if kills == 0 && deaths == 0 {
		return -1
	}
	if deaths == 0 {
		return float64(kills) + 1000
	}
	return float64(kills) / float64(deaths)
}

func playerMapHighlights(rows []mapStatRow) ([]keyValueRow, []keyValueRow) {
	candidates := make([]mapStatRow, 0, len(rows))
	for _, row := range rows {
		if row.Rounds <= 0 || row.Kills+row.Deaths == 0 {
			continue
		}
		candidates = append(candidates, row)
	}
	bestRows := append([]mapStatRow(nil), candidates...)
	sort.SliceStable(bestRows, func(i int, j int) bool {
		a, b := kdScore(bestRows[i].Kills, bestRows[i].Deaths), kdScore(bestRows[j].Kills, bestRows[j].Deaths)
		if a == b {
			return bestRows[i].Kills > bestRows[j].Kills
		}
		return a > b
	})
	toughRows := append([]mapStatRow(nil), candidates...)
	sort.SliceStable(toughRows, func(i int, j int) bool {
		a, b := kdScore(toughRows[i].Kills, toughRows[i].Deaths), kdScore(toughRows[j].Kills, toughRows[j].Deaths)
		if a == b {
			return toughRows[i].Deaths > toughRows[j].Deaths
		}
		return a < b
	})
	return mapHighlightRows(bestRows, 4), mapHighlightRows(toughRows, 4)
}

func mapHighlightRows(rows []mapStatRow, limit int) []keyValueRow {
	if len(rows) > limit {
		rows = rows[:limit]
	}
	result := make([]keyValueRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, keyValueRow{
			Label:  displayCode(row.MapName),
			Value:  formatKD(row.Kills, row.Deaths),
			Detail: fmt.Sprintf("%d kills, %d deaths, %d rounds, %d score", row.Kills, row.Deaths, row.Rounds, row.Score),
		})
	}
	return result
}

func playerWeaponHighlights(rows []weaponBreakdownRow) ([]keyValueRow, []keyValueRow) {
	candidates := make([]weaponBreakdownRow, 0, len(rows))
	for _, row := range rows {
		if row.Kills+row.Deaths == 0 {
			continue
		}
		candidates = append(candidates, row)
	}
	bestRows := append([]weaponBreakdownRow(nil), candidates...)
	sort.SliceStable(bestRows, func(i int, j int) bool {
		a, b := kdScore(bestRows[i].Kills, bestRows[i].Deaths), kdScore(bestRows[j].Kills, bestRows[j].Deaths)
		if a == b {
			return bestRows[i].Kills > bestRows[j].Kills
		}
		return a > b
	})
	problemRows := append([]weaponBreakdownRow(nil), candidates...)
	sort.SliceStable(problemRows, func(i int, j int) bool {
		a, b := kdScore(problemRows[i].Kills, problemRows[i].Deaths), kdScore(problemRows[j].Kills, problemRows[j].Deaths)
		if a == b {
			return problemRows[i].Deaths > problemRows[j].Deaths
		}
		return a < b
	})
	return weaponHighlightRows(bestRows, 4), weaponHighlightRows(problemRows, 4)
}

func weaponHighlightRows(rows []weaponBreakdownRow, limit int) []keyValueRow {
	if len(rows) > limit {
		rows = rows[:limit]
	}
	result := make([]keyValueRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, keyValueRow{
			Label:  displayCode(row.Weapon),
			Value:  formatKD(row.Kills, row.Deaths),
			Detail: fmt.Sprintf("%d kills with / %d deaths to", row.Kills, row.Deaths),
		})
	}
	return result
}

// shortWhen trims a "YYYY-MM-DD HH24:MI" stamp down to "MM-DD HH:MI" for
// chart axis labels.
func shortWhen(when string) string {
	if len(when) >= 16 {
		return when[5:16]
	}
	return when
}

var playerTemplate = template.Must(template.New("player").Funcs(template.FuncMap{
	"css": cssFunc, "favicon": faviconFunc,
	"displayCode": displayCode,
	"mapURL":      mapURL,
	"playerKind":  playerKind,
	"playerURL":   playerURL,
	"rivalryURL":  rivalryURL,
	"siteHeader":  siteHeader,
	"siteFooter":  siteFooter,
	"weaponURL":   weaponURL,
	"vehicleURL":  vehicleURL,
	"formatKD":    formatKD,
	"rate":        ratio,
	"tkRate":      tkRate,
	"add":         func(a int, b int) int { return a + b },
	"playTime":    formatPlayTime,
	"scorePerMin": scorePerMinute,
	"medalLabel":  medalLabel,
	"teamName":    teamName,
	"query":       queryEscape,
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Summary.DisplayName }} — Spawnpoint</title>
  <style>{{ css }}</style>{{ favicon }}
</head>
<body>
  {{ siteHeader "players" }}
  <main>
    {{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}

    {{ $kind := playerKind .Summary.IsBot }}
    <div class="page-title">
      <strong>{{ .Summary.DisplayName }}</strong>
      <span class="badge {{ if eq $kind "Bot" }}bot{{ else if eq $kind "Human" }}human{{ end }}">{{ $kind }}</span>
      {{ if .IdentityLinked }}<span class="badge human" title="Stats merged across nickname changes via BF1942 keyhash">Linked ID</span>{{ end }}
      <span class="muted">last seen {{ .Summary.LastSeen }}</span>
    </div>
    {{ if .Aliases }}<div class="stat-note"><span>Also known as: {{ range $i, $a := .Aliases }}{{ if $i }}, {{ end }}{{ $a }}{{ end }}</span></div>{{ end }}
    <div class="stat-note"><span>Download this profile's stats: <a class="link" href="/player/export?id={{ query .Summary.ID }}&format=json">JSON</a> &middot; <a class="link" href="/player/export?id={{ query .Summary.ID }}&format=csv">CSV</a></span></div>

    <section class="player-summary">
      <div class="player-scorecard">
        <div class="hero-stat primary"><span>Score</span><strong>{{ .Summary.Score }}</strong><small>{{ printf "%.1f" (rate .Summary.Score .Summary.Rounds) }} per round</small></div>
        <div class="hero-stat primary"><span>K/D</span><strong>{{ .KD }}</strong><small>{{ .Summary.Kills }} kills / {{ .Summary.Deaths }} deaths</small></div>
        <div class="hero-stat"><span>Rounds</span><strong>{{ .Summary.Rounds }}</strong><small>{{ playTime .PlaySeconds }}</small></div>
        <div class="hero-stat"><span>Best Round</span><strong>{{ .Distribution.Max }}</strong><small>{{ printf "%.2f" .Distribution.Average }} avg kills</small></div>
        <div class="hero-stat"><span>Kill Streak</span><strong>{{ .LongestKillStreak }}</strong><small>{{ .FirstBloods }} first bloods</small></div>
        <div class="hero-stat"><span>Support</span><strong>{{ add .Summary.Heals .Summary.Repairs }}</strong><small>{{ .Summary.Heals }} heals / {{ .Summary.Repairs }} repairs</small></div>
      </div>
      <aside class="panel">
        <h3>Profile Snapshot</h3>
        <div class="profile-facts">
          <div class="fact-row"><span>Teamkills</span><strong>{{ .Summary.Teamkills }}</strong></div>
          <div class="fact-row"><span>TK Rate</span><strong>{{ printf "%.1f" (tkRate .Summary.Teamkills .Summary.Kills) }}%</strong></div>
          <div class="fact-row"><span>Kills/Round</span><strong>{{ printf "%.2f" (rate .Summary.Kills .Summary.Rounds) }}</strong></div>
          <div class="fact-row"><span>Deaths/Round</span><strong>{{ printf "%.2f" (rate .Summary.Deaths .Summary.Rounds) }}</strong></div>
          <div class="fact-row"><span>Median Kills</span><strong>{{ printf "%.1f" .Distribution.Median }}</strong></div>
          <div class="fact-row"><span>Consistency</span><strong>{{ printf "%.2f" .Distribution.Consistency }}</strong></div>
          <div class="fact-row"><span>Score/Min</span><strong>{{ scorePerMin .Summary.Score .PlaySeconds }}</strong></div>
          <div class="fact-row"><span>Death Streak</span><strong>{{ .LongestDeathStreak }}</strong></div>
        </div>
      </aside>
    </section>

    <div class="grid">
      <div class="panel">
        <h3>Performance by round (latest {{ len .Rounds }})</h3>
        <div class="chart">{{ .PerformanceChart }}</div>
        <div class="legend">
          <span><i style="background:#bc861f"></i>Kills</span>
          <span><i style="background:#d05e55"></i>Deaths</span>
        </div>
      </div>
      <div class="panel">
        <h3>Kill / death split</h3>
        <div class="chart chart-donut">{{ .KDChart }}</div>
        <div class="legend center">
          <span><i style="background:#bc861f"></i>Kills {{ .Summary.Kills }}</span>
          <span><i style="background:#d05e55"></i>Deaths {{ .Summary.Deaths }}</span>
        </div>
      </div>
    </div>

    <div class="grid">
      <div class="panel">
        <h3>Kills per day (last 30 days)</h3>
        <div class="chart">{{ .ActivityChart }}</div>
      </div>
      <div class="panel">
        <h3>Round outcomes</h3>
        <div class="chart chart-donut">{{ .WinLossChart }}</div>
        <div class="legend center">
          <span><i style="background:#2f9e68"></i>Wins {{ .Wins }}</span>
          <span><i style="background:#d05e55"></i>Losses {{ .Losses }}</span>
        </div>
      </div>
    </div>

    <div class="grid2">
      <div class="panel">
        <h3>Kills by map</h3>
        {{ .MapBars }}
      </div>
      <div class="panel">
        <h3>Kills by weapon</h3>
        {{ .WeaponBars }}
      </div>
    </div>

    <div class="grid2">
      <div class="panel">
        <h3>Kits picked up</h3>
        {{ .KitBars }}
      </div>
      <div class="panel">
        <h3>Vehicles entered</h3>
        {{ .VehicleBars }}
      </div>
    </div>

    <div class="grid2">
      <section class="panel"><h3>Best Maps</h3><table><thead><tr><th>Map</th><th>K/D</th><th>Detail</th></tr></thead><tbody>{{ range .BestMaps }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No map split data yet.</td></tr>{{ end }}</tbody></table></section>
      <section class="panel"><h3>Tough Maps</h3><table><thead><tr><th>Map</th><th>K/D</th><th>Detail</th></tr></thead><tbody>{{ range .ToughMaps }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No map split data yet.</td></tr>{{ end }}</tbody></table></section>
    </div>

    <div class="grid2">
      <section class="panel"><h3>Best Weapons</h3><table><thead><tr><th>Weapon</th><th>K/D</th><th>Detail</th></tr></thead><tbody>{{ range .BestWeapons }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No weapon split data yet.</td></tr>{{ end }}</tbody></table></section>
      <section class="panel"><h3>Problem Weapons</h3><table><thead><tr><th>Weapon</th><th>K/D</th><th>Detail</th></tr></thead><tbody>{{ range .ProblemWeapons }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No weapon split data yet.</td></tr>{{ end }}</tbody></table></section>
    </div>

    {{ if .Medals }}<section class="panel medal-case"><h3>Medal Case</h3><div class="medal-chips">
      {{ range .Medals }}<span class="medal-chip"><span class="medal g">&#9733;</span>{{ medalLabel .Category }}<b>&times;{{ .Count }}</b></span>{{ end }}
    </div></section>{{ end }}

    <div class="grid2">
      <div class="panel">
        <h3>Recent Form</h3>
        <table><thead><tr><th>Window</th><th>K/D</th><th>Detail</th></tr></thead><tbody>
          {{ range .FormTrends }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td>{{ .Detail }}</td></tr>{{ else }}<tr><td colspan="3" class="muted">No form data yet.</td></tr>{{ end }}
        </tbody></table>
      </div>
      <div class="panel">
        <h3>Similar Players</h3>
        <table><thead><tr><th>Player</th><th>Rounds</th><th>K/D</th><th>Score</th></tr></thead><tbody>
          {{ range .SimilarPlayers }}<tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a></td><td>{{ .Rounds }}</td><td>{{ formatKD .Kills .Deaths }}</td><td>{{ .Score }}</td></tr>{{ else }}<tr><td colspan="4" class="muted">No similar players found.</td></tr>{{ end }}
        </tbody></table>
      </div>
    </div>

    <h2>Weapon Matchups</h2>
    <table>
      <thead><tr><th>Weapon</th><th>Kills With</th><th>Deaths To</th><th>K/D</th></tr></thead>
      <tbody>
        {{ range .WeaponBreakdown }}
        <tr>
          <td><a class="link" href="{{ weaponURL .Weapon }}">{{ displayCode .Weapon }}</a> <span class="muted mono">{{ .Weapon }}</span></td>
          <td>{{ .Kills }}</td>
          <td>{{ .Deaths }}</td>
          <td>{{ formatKD .Kills .Deaths }}</td>
        </tr>
        {{ else }}
        <tr><td colspan="4" class="muted">No weapon matchup data yet.</td></tr>
        {{ end }}
      </tbody>
    </table>

    <h2>Vehicle Matchups</h2>
    <table>
      <thead><tr><th>Vehicle</th><th>Kills With</th><th>Deaths To</th><th>K/D</th></tr></thead>
      <tbody>
        {{ range .VehicleBreakdown }}
        <tr>
          <td><a class="link" href="{{ vehicleURL .Weapon }}">{{ displayCode .Weapon }}</a> <span class="muted mono">{{ .Weapon }}</span></td>
          <td>{{ .Kills }}</td>
          <td>{{ .Deaths }}</td>
          <td>{{ formatKD .Kills .Deaths }}</td>
        </tr>
        {{ else }}
        <tr><td colspan="4" class="muted">No vehicle matchup data yet.</td></tr>
        {{ end }}
      </tbody>
    </table>

    <div class="grid2">
      <div class="panel">
        <h3>Top victims</h3>
        <table>
          <thead><tr><th>Player</th><th>Killed</th></tr></thead>
          <tbody>
            {{ range .Victims }}
            <tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a> <a class="muted" href="{{ rivalryURL $.Summary.ID .ID }}">rivalry</a></td><td>{{ .Count }}</td></tr>
            {{ else }}
            <tr><td colspan="2" class="muted">No kills recorded.</td></tr>
            {{ end }}
          </tbody>
        </table>
      </div>
      <div class="panel">
        <h3>Nemeses (killed by)</h3>
        <table>
          <thead><tr><th>Player</th><th>Deaths</th></tr></thead>
          <tbody>
            {{ range .Nemeses }}
            <tr><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .DisplayName }}</a> <a class="muted" href="{{ rivalryURL $.Summary.ID .ID }}">rivalry</a></td><td>{{ .Count }}</td></tr>
            {{ else }}
            <tr><td colspan="2" class="muted">No deaths recorded.</td></tr>
            {{ end }}
          </tbody>
        </table>
      </div>
    </div>

    <h2>Map Performance</h2>
    <table>
      <thead><tr><th>Map</th><th>Rounds</th><th>Kills</th><th>Deaths</th><th>K/D</th><th>Score</th></tr></thead>
      <tbody>
        {{ range .Maps }}
        <tr>
          <td><a class="link" href="{{ mapURL .MapName }}">{{ displayCode .MapName }}</a></td>
          <td>{{ .Rounds }}</td>
          <td>{{ .Kills }}</td>
          <td>{{ .Deaths }}</td>
          <td>{{ formatKD .Kills .Deaths }}</td>
          <td>{{ .Score }}</td>
        </tr>
        {{ else }}
        <tr><td colspan="6" class="muted">No map data yet.</td></tr>
        {{ end }}
      </tbody>
    </table>

    <h2 id="rounds">Recent Rounds</h2>
    <table>
      <thead><tr><th>When</th><th>Map</th><th>Team</th><th>Kills</th><th>Deaths</th><th>Score</th></tr></thead>
      <tbody>
        {{ range .RoundRows }}
        <tr>
          <td>{{ .When }}</td>
          <td><a class="link" href="{{ mapURL .MapName }}">{{ displayCode .MapName }}</a></td>
          <td>{{ if .TeamID }}{{ teamName .TeamID }}{{ else }}<span class="muted">n/a</span>{{ end }}</td>
          <td>{{ .Kills }}</td>
          <td>{{ .Deaths }}</td>
          <td>{{ .Score }}</td>
        </tr>
        {{ else }}
        <tr><td colspan="6" class="muted">No rounds recorded yet.</td></tr>
        {{ end }}
      </tbody>
    </table>
    <div class="pager">{{ if .RoundsPager.PrevURL }}<a href="{{ .RoundsPager.PrevURL }}">&larr; Newer</a>{{ end }}<span>Page {{ .RoundsPager.Page }}</span>{{ if .RoundsPager.NextURL }}<a href="{{ .RoundsPager.NextURL }}">Older &rarr;</a>{{ end }}</div>

    <h2 id="killfeed">Kill Feed</h2>
    <table>
      <thead><tr><th>When</th><th>Type</th><th>Opponent</th><th>Weapon</th><th>Map</th></tr></thead>
      <tbody>
        {{ range .KillFeed }}
        <tr>
          <td>{{ .When }}</td>
          <td>{{ .Type }}</td>
          <td>{{ if .OpponentID }}<a class="link" href="{{ playerURL .OpponentID "all" }}">{{ .Opponent }}</a>{{ else }}{{ .Opponent }}{{ end }}</td>
          <td>{{ if .Weapon }}<a class="link" href="{{ weaponURL .Weapon }}">{{ displayCode .Weapon }}</a>{{ else }}<span class="muted">Unknown</span>{{ end }}</td>
          <td><a class="link" href="{{ mapURL .MapName }}">{{ displayCode .MapName }}</a></td>
        </tr>
        {{ else }}
        <tr><td colspan="5" class="muted">No kills or deaths recorded yet.</td></tr>
        {{ end }}
      </tbody>
    </table>
    <div class="pager">{{ if .FeedPager.PrevURL }}<a href="{{ .FeedPager.PrevURL }}">&larr; Newer</a>{{ end }}<span>Page {{ .FeedPager.Page }}</span>{{ if .FeedPager.NextURL }}<a href="{{ .FeedPager.NextURL }}">Older &rarr;</a>{{ end }}</div>
  </main>
  {{ siteFooter }}
</body>
</html>`))
