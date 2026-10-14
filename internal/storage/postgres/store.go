package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/hootmeow/spawnpoint/domain"
)

const (
	demoOrganizationID = "00000000-0000-4000-8000-000000000001"
	demoServerID       = "00000000-0000-4000-8000-000000000001"
)

// maxPlayerNameLength bounds ingested player names/aliases well below
// Postgres's ~2704-byte btree index entry limit (player_aliases has a
// unique (player_id, alias) index) — a hostile or malformed log file could
// otherwise carry an arbitrarily long name and abort that ingest job.
const maxPlayerNameLength = 256

func truncatePlayerName(name string) string {
	r := []rune(name)
	if len(r) <= maxPlayerNameLength {
		return name
	}
	return string(r[:maxPlayerNameLength])
}

type Store struct {
	databaseURL string
	db          *sql.DB
}

type IngestOptions struct {
	// ServerID pins the ingest to a known server row (remote uploads resolve
	// it from the API key). When empty, the server is found or created by
	// ServerName.
	ServerID      string
	ServerName    string
	ServerAddress string
	ServerPort    int
	ArchiveDir    string
	// Replace purges any previously ingested data for the same log content
	// and re-ingests it, so logs can be reprocessed after parser changes.
	Replace bool
}

type IngestSummary struct {
	OriginalName string
	SHA256       string
	StorageURI   string
	AlreadySeen  bool
	Rounds       int
	Events       int
	RoundStats   int
	// RoundIDs are the round rows this ingest just wrote (in round_index
	// order), for callers that want to build a per-round notification
	// after a successful watch-mode ingest.
	RoundIDs []string
}

type IngestJobUpdate struct {
	Status        string
	ErrorMessage  string
	SHA256        string
	StorageURI    string
	FileSizeBytes int64
	Rounds        int
	Events        int
	RoundStats    int
}

func New(databaseURL string) *Store {
	return &Store{databaseURL: databaseURL}
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Bounded so a burst of concurrent ingest uploads (or several remote log
	// workers) can't grow this pool without limit and exhaust Postgres'
	// max_connections. Same env vars as apps/api's main request-serving pool,
	// so one setting tunes both.
	db.SetMaxOpenConns(envInt("DB_MAX_OPEN_CONNS", 16))
	db.SetMaxIdleConns(envInt("DB_MAX_IDLE_CONNS", 8))
	db.SetConnMaxLifetime(time.Duration(envInt("DB_CONN_MAX_LIFETIME_SECONDS", 900)) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(envInt("DB_CONN_MAX_IDLE_SECONDS", 300)) * time.Second)
	return &Store{databaseURL: databaseURL, db: db}, nil
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func (s *Store) DatabaseURL() string {
	return s.databaseURL
}

func (s *Store) StartIngestJob(ctx context.Context, serverID string, sourcePath string, fileSizeBytes int64) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	if serverID == "" {
		serverID = demoServerID
	}
	jobID := newUUID()
	_, err := s.db.ExecContext(ctx, `
		insert into ingest_jobs (id, server_id, source_path, original_name, status, file_size_bytes)
		values ($1, $2, $3, $4, 'started', $5)
	`, jobID, serverID, sourcePath, originalLogName(sourcePath), fileSizeBytes)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return "", nil
		}
		return "", err
	}
	return jobID, nil
}

func (s *Store) FinishIngestJob(ctx context.Context, jobID string, update IngestJobUpdate) error {
	if s == nil || s.db == nil || jobID == "" {
		return nil
	}
	shaArg := any(nil)
	if update.SHA256 != "" {
		shaArg = update.SHA256
	}
	_, err := s.db.ExecContext(ctx, `
		update ingest_jobs
		set status = $2,
			error_message = nullif($3, ''),
			content_sha256 = case when $4::text is null then content_sha256 else decode($4::text, 'hex') end,
			storage_uri = nullif($5, ''),
			file_size_bytes = case when $6::bigint > 0 then $6 else file_size_bytes end,
			rounds = $7,
			events = $8,
			round_stats = $9,
			finished_at = now()
		where id = $1
	`, jobID, update.Status, update.ErrorMessage, shaArg, update.StorageURI, update.FileSizeBytes, update.Rounds, update.Events, update.RoundStats)
	if err != nil && strings.Contains(err.Error(), "does not exist") {
		return nil
	}
	return err
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) IngestRawLog(ctx context.Context, originalName string, raw []byte, parsed domain.ParseResult, opts IngestOptions) (IngestSummary, error) {
	sum := sha256.Sum256(raw)
	shaHex := hex.EncodeToString(sum[:])
	storageURI, err := archiveRawLog(opts.ArchiveDir, originalName, shaHex, raw)
	if err != nil {
		return IngestSummary{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestSummary{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	serverID, err := resolveServer(ctx, tx, opts)
	if err != nil {
		return IngestSummary{}, err
	}

	if opts.Replace {
		if err := purgeRawLog(ctx, tx, shaHex); err != nil {
			return IngestSummary{}, err
		}
	}

	// Round outcomes, play time, and medals need migration 0004; older
	// databases ingest without them rather than aborting the transaction.
	var hasOutcomes bool
	if err := tx.QueryRowContext(ctx, `
		select to_regclass('round_awards') is not null
		   and exists (select 1 from information_schema.columns
		               where table_name = 'round_player_stats' and column_name = 'play_seconds')
		   and exists (select 1 from information_schema.columns
		               where table_name = 'rounds' and column_name = 'victory_type')
	`).Scan(&hasOutcomes); err != nil {
		return IngestSummary{}, err
	}

	rawLogID := newUUID()
	status := "parsed"
	displayName := originalLogName(originalName)
	_, err = tx.ExecContext(ctx, `
		insert into raw_log_files (id, server_id, original_name, content_sha256, storage_uri, compressed, status, parsed_at)
		values ($1, $2, $3, decode($4, 'hex'), $5, $6, $7, now())
		on conflict (content_sha256) do nothing
	`, rawLogID, serverID, displayName, shaHex, storageURI, isCompressed(originalName), status)
	if err != nil {
		return IngestSummary{}, err
	}

	var inserted bool
	if err := tx.QueryRowContext(ctx, `select id = $1 from raw_log_files where content_sha256 = decode($2, 'hex')`, rawLogID, shaHex).Scan(&inserted); err != nil {
		return IngestSummary{}, err
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return IngestSummary{}, err
		}
		return IngestSummary{OriginalName: originalName, SHA256: shaHex, StorageURI: storageURI, AlreadySeen: true}, nil
	}

	gameID := newUUID()
	settingsJSON, _ := json.Marshal(parsed.Game.Settings)
	startedAt := time.Now().UTC()
	if ts, ok := timestampFromLogName(displayName); ok {
		startedAt = ts
	}
	if len(parsed.Events) > 0 && parsed.Events[0].OccurredAt != nil {
		startedAt = *parsed.Events[0].OccurredAt
	}
	_, err = tx.ExecContext(ctx, `
		insert into games (id, server_id, raw_log_file_id, mod_id, map_id, map_name, game_mode, started_at, server_settings)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
	`, gameID, serverID, rawLogID, parsed.Game.ModID, parsed.Game.MapID, parsed.Game.Map, parsed.Game.GameMode, startedAt, string(settingsJSON))
	if err != nil {
		return IngestSummary{}, err
	}

	roundIDs := map[int]string{}
	for i := 1; i <= max(1, parsed.Rounds); i++ {
		roundID := newUUID()
		roundIDs[i] = roundID
		_, err := tx.ExecContext(ctx, `
			insert into rounds (id, game_id, round_index, end_type)
			values ($1, $2, $3, 'unknown')
		`, roundID, gameID, i)
		if err != nil {
			return IngestSummary{}, err
		}
	}

	// The engine announces a joining human as createPlayer with playerKeyHash
	// logged right after it for the same slot (some builds log the hash first).
	// Pair each createPlayer with its keyhash, then resolve players in log
	// order so the keyhash identifies the human behind every nickname.
	keyhashByCreate := map[int]string{} // parsed.Events index of createPlayer -> keyhash
	{
		keyhashBySlot := map[int]string{} // hash that arrived before its createPlayer
		pendingCreate := map[int]int{}    // slot -> createPlayer index awaiting its hash
		for i, event := range parsed.Events {
			if event.ActorSlotID == nil {
				continue
			}
			slot := *event.ActorSlotID
			switch event.Type {
			case domain.EventPlayerKeyHash:
				keyhash := strings.TrimSpace(event.Params["keyhash"])
				if keyhash == "" {
					continue
				}
				if createIndex, ok := pendingCreate[slot]; ok {
					keyhashByCreate[createIndex] = keyhash
					delete(pendingCreate, slot)
				} else {
					keyhashBySlot[slot] = keyhash
				}
			case domain.EventCreatePlayer:
				if isTruthy(event.Params["is_ai"]) {
					// Bots never own a keyhash; drop anything stale on the slot.
					delete(keyhashBySlot, slot)
					delete(pendingCreate, slot)
					continue
				}
				if keyhash, ok := keyhashBySlot[slot]; ok {
					keyhashByCreate[i] = keyhash
					delete(keyhashBySlot, slot)
				} else {
					pendingCreate[slot] = i
				}
			case domain.EventDisconnectPlayer:
				delete(keyhashBySlot, slot)
				delete(pendingCreate, slot)
			}
		}
	}

	playerBySlot := map[int]string{}
	for i, event := range parsed.Events {
		if event.ActorSlotID == nil {
			continue
		}
		slot := *event.ActorSlotID
		switch event.Type {
		case domain.EventCreatePlayer:
			name := event.Params["name"]
			if name == "" {
				name = fmt.Sprintf("slot-%d", slot)
			}
			playerID, err := ensurePlayerIdentity(ctx, tx, name, keyhashByCreate[i])
			if err != nil {
				return IngestSummary{}, err
			}
			playerBySlot[slot] = playerID
		case domain.EventChangePlayerName:
			name := strings.TrimSpace(firstEventParam(event.Params, "name", "new_name", "newname"))
			if name != "" {
				if playerID := playerBySlot[slot]; playerID != "" {
					if err := recordAlias(ctx, tx, playerID, name); err != nil {
						return IngestSummary{}, err
					}
				}
			}
		}
	}

	for _, event := range parsed.Events {
		var actorPlayerID any
		var targetPlayerID any
		if event.ActorSlotID != nil {
			actorPlayerID = playerBySlot[*event.ActorSlotID]
		}
		if event.TargetSlotID != nil {
			targetPlayerID = playerBySlot[*event.TargetSlotID]
		}
		paramsJSON, _ := json.Marshal(event.Params)
		_, err := tx.ExecContext(ctx, `
			insert into events (
				id, organization_id, server_id, game_id, round_id, raw_log_file_id, sequence_no,
				event_type, raw_event_name, occurred_at, timestamp_seconds, actor_player_id,
				target_player_id, actor_slot_id, target_slot_id, weapon_code, vehicle_code, kit_code,
				chat_text, params
			)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20::jsonb)
		`, newUUID(), demoOrganizationID, serverID, gameID, nullableString(roundIDs[event.RoundIndex]), rawLogID,
			event.SequenceNo, string(event.Type), event.RawName, event.OccurredAt, event.TimestampSec,
			nullableUUID(actorPlayerID), nullableUUID(targetPlayerID), nullableInt(event.ActorSlotID),
			nullableInt(event.TargetSlotID), nullableString(event.Weapon), nullableString(event.Vehicle),
			nullableString(event.Kit), nullableString(event.Text), string(paramsJSON))
		if err != nil {
			return IngestSummary{}, err
		}
	}

	roundsWithRoundStatsPlayers := map[int]bool{}
	for _, roundStats := range parsed.RoundStats {
		if len(roundStats.Players) > 0 {
			roundsWithRoundStatsPlayers[roundStats.RoundIndex] = true
		}
	}

	derivedStats := derivePlayerStats(parsed.Events)
	for roundIndex, statsBySlot := range derivedStats {
		if roundsWithRoundStatsPlayers[roundIndex] {
			continue
		}
		roundID := roundIDs[roundIndex]
		if roundID == "" {
			continue
		}
		for slotID, stats := range statsBySlot {
			playerID := playerBySlot[slotID]
			var err error
			if playerID == "" {
				playerID, err = ensurePlayer(ctx, tx, fmt.Sprintf("slot-%d", slotID))
				if err != nil {
					return IngestSummary{}, err
				}
				playerBySlot[slotID] = playerID
			}
			_, err = tx.ExecContext(ctx, `
				insert into round_player_stats (
					id, round_id, player_id, slot_id, score, kills, deaths, teamkills, captures,
					attacks, defences, objectives, objective_teamkills, heals, self_heals, repairs,
					other_repairs, source
				)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, 'derived_events')
				on conflict (round_id, player_id) do update set
					slot_id = excluded.slot_id,
					score = excluded.score,
					kills = excluded.kills,
					deaths = excluded.deaths,
					teamkills = excluded.teamkills,
					captures = excluded.captures,
					attacks = excluded.attacks,
					defences = excluded.defences,
					objectives = excluded.objectives,
					objective_teamkills = excluded.objective_teamkills,
					heals = excluded.heals,
					self_heals = excluded.self_heals,
					repairs = excluded.repairs,
					other_repairs = excluded.other_repairs,
					source = excluded.source
			`, newUUID(), roundID, playerID, slotID, stats.Score, stats.Kills, stats.Deaths, stats.Teamkills,
				stats.Captures, stats.Attacks, stats.Defences, stats.Objectives, stats.ObjectiveTeamkills,
				stats.Heals, stats.SelfHeals, stats.Repairs, stats.OtherRepairs)
			if err != nil {
				return IngestSummary{}, err
			}
		}
	}

	for _, roundStats := range parsed.RoundStats {
		roundID := roundIDs[roundStats.RoundIndex]
		if roundID == "" {
			continue
		}
		endTicketsJSON, _ := json.Marshal(roundStats.EndTickets)
		roundUpdate := `update rounds
			set ended_at = $2, end_type = 'regular', winning_team_id = $3, end_tickets = $4::jsonb
			where id = $1`
		args := []any{roundID, startedAt.Add(time.Duration(roundStats.TimestampSec) * time.Second), nullableInt(roundStats.WinningTeamID), string(endTicketsJSON)}
		if hasOutcomes {
			roundUpdate = `update rounds
			set ended_at = $2, end_type = 'regular', winning_team_id = $3, end_tickets = $4::jsonb, victory_type = $5
			where id = $1`
			args = append(args, nullableInt(roundStats.VictoryType))
		}
		_, err := tx.ExecContext(ctx, roundUpdate, args...)
		if err != nil {
			return IngestSummary{}, err
		}
		for _, playerStats := range roundStats.Players {
			name := playerStats.Name
			if name == "" && playerStats.SlotID != nil {
				name = fmt.Sprintf("slot-%d", *playerStats.SlotID)
			}
			if name == "" {
				name = "unknown"
			}
			playerID, err := ensurePlayer(ctx, tx, name)
			if err != nil {
				return IngestSummary{}, err
			}
			_, err = tx.ExecContext(ctx, `
				insert into round_player_stats (
					id, round_id, player_id, slot_id, is_bot, team_id, score, kills, deaths, teamkills, captures,
					attacks, defences, objectives, objective_teamkills, heals, self_heals, repairs,
					other_repairs, rank_in_round, source
				)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
				on conflict (round_id, player_id) do update set
					slot_id = excluded.slot_id,
					is_bot = excluded.is_bot,
					team_id = excluded.team_id,
					score = excluded.score,
					kills = excluded.kills,
					deaths = excluded.deaths,
					teamkills = excluded.teamkills,
					captures = excluded.captures,
					attacks = excluded.attacks,
					defences = excluded.defences,
					objectives = excluded.objectives,
					objective_teamkills = excluded.objective_teamkills,
					heals = excluded.heals,
					self_heals = excluded.self_heals,
					repairs = excluded.repairs,
					other_repairs = excluded.other_repairs,
					rank_in_round = excluded.rank_in_round,
					source = excluded.source
			`, newUUID(), roundID, playerID, nullableInt(playerStats.SlotID), nullableBool(playerStats.IsBot), nullableInt(playerStats.TeamID), playerStats.Score,
				playerStats.Kills, playerStats.Deaths, playerStats.Teamkills, playerStats.Captures,
				playerStats.Attacks, playerStats.Defences, playerStats.Objectives,
				playerStats.ObjectiveTeamkills, playerStats.Heals, playerStats.SelfHeals,
				playerStats.Repairs, playerStats.OtherRepairs, nullableInt(playerStats.RankInRound),
				"roundstats")
			if err != nil {
				return IngestSummary{}, err
			}
		}
	}

	// Reapply admin bot/human overrides (migration 0005) to the rounds this
	// log just wrote, so moderation decisions survive new ingests and
	// reingests. Probed like hasOutcomes to tolerate un-migrated databases.
	var hasModeration bool
	if err := tx.QueryRowContext(ctx, `
		select exists (select 1 from information_schema.columns
		               where table_name = 'players' and column_name = 'bot_override')
	`).Scan(&hasModeration); err != nil {
		return IngestSummary{}, err
	}
	if hasModeration {
		for _, roundID := range roundIDs {
			if roundID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				update round_player_stats s set is_bot = p.bot_override
				from players p
				where p.id = s.player_id and p.bot_override is not null
				  and s.round_id = $1 and s.is_bot is distinct from p.bot_override
			`, roundID); err != nil {
				return IngestSummary{}, err
			}
		}
	}

	if hasOutcomes {
		for roundIndex, secondsBySlot := range derivePlaySeconds(parsed.Events) {
			roundID := roundIDs[roundIndex]
			if roundID == "" {
				continue
			}
			for slotID, seconds := range secondsBySlot {
				if _, err := tx.ExecContext(ctx, `
					update round_player_stats set play_seconds = $3 where round_id = $1 and slot_id = $2
				`, roundID, slotID, seconds); err != nil {
					return IngestSummary{}, err
				}
			}
		}
		for _, roundID := range roundIDs {
			if err := recordRoundAwards(ctx, tx, roundID); err != nil {
				return IngestSummary{}, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return IngestSummary{}, err
	}
	if err := s.RefreshSummaries(ctx); err != nil {
		// The ingest itself already committed; a failed refresh just means
		// leaderboards/stats are stale until the next successful ingest or
		// refresh, not a lost or corrupted round. Log it — silently eating
		// this previously made a real failure invisible on the dashboard.
		log.Printf("ingest: refresh summaries after commit: %v", err)
	}
	orderedRoundIDs := make([]string, 0, len(roundIDs))
	for i := 1; i <= max(1, parsed.Rounds); i++ {
		if id := roundIDs[i]; id != "" {
			orderedRoundIDs = append(orderedRoundIDs, id)
		}
	}
	return IngestSummary{
		OriginalName: originalName,
		SHA256:       shaHex,
		StorageURI:   storageURI,
		Rounds:       parsed.Rounds,
		Events:       len(parsed.Events),
		RoundStats:   len(parsed.RoundStats),
		RoundIDs:     orderedRoundIDs,
	}, nil
}

// RoundNotificationSummary gathers just enough about a freshly ingested round
// to build a one-line "round complete" notification: the map/mode, top frag,
// and any round_awards medals recorded for it (recordRoundAwards runs inside
// the same ingest transaction, so these are already present by the time the
// caller sees a committed IngestSummary).
type RoundNotificationSummary struct {
	MapName     string
	GameMode    string
	StartedAt   time.Time
	EndedAt     time.Time
	PlayerCount int
	TopFragName string
	TopFrag     int
	Medals      []RoundMedal
}

type RoundMedal struct {
	Category string
	Player   string
}

func (s *Store) RoundNotificationSummary(ctx context.Context, roundID string) (RoundNotificationSummary, error) {
	var sum RoundNotificationSummary
	err := s.db.QueryRowContext(ctx, `
		select g.map_name, coalesce(g.game_mode, ''), g.started_at, coalesce(r.ended_at, g.started_at)
		from rounds r join games g on g.id = r.game_id
		where r.id = $1
	`, roundID).Scan(&sum.MapName, &sum.GameMode, &sum.StartedAt, &sum.EndedAt)
	if err != nil {
		return sum, err
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from round_player_stats where round_id = $1`, roundID).Scan(&sum.PlayerCount); err != nil {
		return sum, err
	}
	var topFragName sql.NullString
	var topFrag sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		select p.display_name, s.kills
		from round_player_stats s join players p on p.id = s.player_id
		where s.round_id = $1
		order by s.kills desc, p.display_name asc
		limit 1
	`, roundID).Scan(&topFragName, &topFrag); err != nil && err != sql.ErrNoRows {
		return sum, err
	}
	sum.TopFragName = topFragName.String
	sum.TopFrag = int(topFrag.Int64)

	rows, err := s.db.QueryContext(ctx, `
		select ra.category, p.display_name
		from round_awards ra join players p on p.id = ra.player_id
		where ra.round_id = $1
		order by ra.category
	`, roundID)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return sum, nil
		}
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var m RoundMedal
		if err := rows.Scan(&m.Category, &m.Player); err != nil {
			return sum, err
		}
		sum.Medals = append(sum.Medals, m)
	}
	return sum, rows.Err()
}

func (s *Store) RefreshSummaries(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	statements := []string{
		`refresh materialized view player_totals_mv`,
		`refresh materialized view map_totals_mv`,
		// vehicle_code_list_mv first: weapon_totals_mv/vehicle_kill_totals_mv
		// below, plus every weapon/vehicle stat query in apps/api, read it.
		`refresh materialized view vehicle_code_list_mv`,
		`refresh materialized view weapon_totals_mv`,
		`refresh materialized view vehicle_totals_mv`,
		`refresh materialized view vehicle_kill_totals_mv`,
		`refresh materialized view site_totals_mv`,
		// CONCURRENTLY (needs the unique index from migration 0007) so a
		// slow rebuild of this one — it re-scans the whole events table —
		// never blocks concurrent /map reads.
		`refresh materialized view concurrently map_summary_mv`,
		`refresh materialized view vehicle_activity_totals_mv`,
		`refresh materialized view kit_totals_mv`,
		`refresh materialized view kit_top_users_mv`,
		`refresh materialized view kit_top_maps_mv`,
		`refresh materialized view award_totals_mv`,
		`refresh materialized view vehicle_ace_totals_mv`,
		`refresh materialized view player_breakdown_mv`,
		`refresh materialized view player_first_bloods_mv`,
		// watch mode re-verifies its entire backlog against the DB on every
		// worker restart (its "already seen" cache is in-memory only), so
		// each restart writes one 'skipped' ingest_jobs row per archived
		// file. Recent skips stay for admin troubleshooting; anything older
		// than 3 days is pure noise — it grew to 831k rows / 483MB before
		// this was added. 'started' rows older than a day mean the process
		// died mid-file; nothing will ever finish them.
		`delete from ingest_jobs where status = 'skipped' and started_at < now() - interval '3 days'`,
		`delete from ingest_jobs where status = 'started' and started_at < now() - interval '1 day'`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			// Summary views are optional for older installs that have not applied
			// production hardening migrations yet; query code falls back to base tables.
			if strings.Contains(err.Error(), "does not exist") {
				continue
			}
			return err
		}
	}
	return nil
}

type derivedPlayerStats struct {
	Score              int
	Kills              int
	Deaths             int
	Teamkills          int
	Captures           int
	Attacks            int
	Defences           int
	Objectives         int
	ObjectiveTeamkills int
	Heals              int
	SelfHeals          int
	Repairs            int
	OtherRepairs       int
}

func derivePlayerStats(events []domain.NormalizedEvent) map[int]map[int]*derivedPlayerStats {
	rounds := map[int]map[int]*derivedPlayerStats{}
	statFor := func(roundIndex int, slotID *int) *derivedPlayerStats {
		if slotID == nil || roundIndex == 0 {
			return nil
		}
		if rounds[roundIndex] == nil {
			rounds[roundIndex] = map[int]*derivedPlayerStats{}
		}
		if rounds[roundIndex][*slotID] == nil {
			rounds[roundIndex][*slotID] = &derivedPlayerStats{}
		}
		return rounds[roundIndex][*slotID]
	}

	for _, event := range events {
		stats := statFor(event.RoundIndex, event.ActorSlotID)
		if stats == nil {
			continue
		}
		switch event.Type {
		case domain.EventScore:
			switch strings.ToLower(event.Params["score_type"]) {
			case "kill":
				stats.Kills++
				stats.Score++
			case "death", "deathnomsg":
				stats.Deaths++
			case "tk", "teamkill", "teamkills":
				stats.Teamkills++
				stats.Score--
			case "capture":
				stats.Captures++
				stats.Score += 2
			case "attack":
				stats.Attacks++
			case "defence", "defense":
				stats.Defences++
			case "objective":
				stats.Objectives++
			case "objectivetk", "objective_teamkill":
				stats.ObjectiveTeamkills++
			}
		case domain.EventEndHeal:
			if event.TargetSlotID != nil && *event.TargetSlotID == *event.ActorSlotID {
				stats.SelfHeals++
			} else {
				stats.Heals++
			}
		case domain.EventEndRepair:
			stats.Repairs++
		}
	}
	return rounds
}

// derivePlaySeconds estimates seconds of presence per slot per round from the
// event stream: a slot is present from its first event (or createPlayer) until
// it disconnects or the round's last event. Reconnects accumulate segments.
func derivePlaySeconds(events []domain.NormalizedEvent) map[int]map[int]int {
	type roundTrack struct {
		activeSince map[int]float64
		total       map[int]float64
		lastTS      float64
	}
	rounds := map[int]*roundTrack{}
	for _, event := range events {
		if event.RoundIndex == 0 {
			continue
		}
		track := rounds[event.RoundIndex]
		if track == nil {
			track = &roundTrack{activeSince: map[int]float64{}, total: map[int]float64{}}
			rounds[event.RoundIndex] = track
		}
		if event.TimestampSec > track.lastTS {
			track.lastTS = event.TimestampSec
		}
		if event.ActorSlotID == nil {
			continue
		}
		slot := *event.ActorSlotID
		switch event.Type {
		case domain.EventDisconnectPlayer:
			if since, ok := track.activeSince[slot]; ok {
				if event.TimestampSec > since {
					track.total[slot] += event.TimestampSec - since
				}
				delete(track.activeSince, slot)
			}
		default:
			if _, ok := track.activeSince[slot]; !ok {
				track.activeSince[slot] = event.TimestampSec
			}
		}
	}

	result := map[int]map[int]int{}
	for roundIndex, track := range rounds {
		for slot, since := range track.activeSince {
			if track.lastTS > since {
				track.total[slot] += track.lastTS - since
			}
		}
		for slot, seconds := range track.total {
			if seconds <= 0 {
				continue
			}
			if result[roundIndex] == nil {
				result[roundIndex] = map[int]int{}
			}
			result[roundIndex][slot] = int(seconds + 0.5)
		}
	}
	return result
}

// roundAwardCategories maps medal categories to round_player_stats columns.
// Category names are also what the UI displays; columns are trusted literals.
var roundAwardCategories = [][2]string{
	{"kills", "kills"},
	{"score", "score"},
	{"captures", "captures"},
	{"heals", "heals"},
	{"repairs", "repairs"},
	{"deaths", "deaths"},
}

// recordRoundAwards stores the per-category winner of one round. Winners are
// the highest value with round score then slot as deterministic tie-breaks.
func recordRoundAwards(ctx context.Context, tx *sql.Tx, roundID string) error {
	for _, category := range roundAwardCategories {
		name, column := category[0], category[1]
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			insert into round_awards (id, round_id, category, player_id, value)
			select $2, round_id, $3, player_id, %s
			from round_player_stats
			where round_id = $1 and %s > 0
			order by %s desc, score desc, slot_id asc nulls last
			limit 1
			on conflict (round_id, category) do update set player_id = excluded.player_id, value = excluded.value
		`, column, column, column), roundID, newUUID(), name)
		if err != nil {
			return err
		}
	}
	return nil
}

// purgeRawLog removes every row derived from a previously ingested log with
// the same content hash so the log can be re-ingested from scratch. Players
// and aliases are kept; they are shared across logs and reused by ensurePlayer.
func purgeRawLog(ctx context.Context, tx *sql.Tx, shaHex string) error {
	var rawLogID string
	err := tx.QueryRowContext(ctx, `
		select id from raw_log_files where content_sha256 = decode($1, 'hex')
	`, shaHex).Scan(&rawLogID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	statements := []string{
		`delete from events where raw_log_file_id = $1`,
		`delete from round_player_stats where round_id in (
			select r.id from rounds r
			join games g on g.id = r.game_id
			where g.raw_log_file_id = $1
		)`,
		`delete from rounds where game_id in (select id from games where raw_log_file_id = $1)`,
		`delete from games where raw_log_file_id = $1`,
		`delete from raw_log_files where id = $1`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, rawLogID); err != nil {
			return err
		}
	}
	return nil
}

// resolveServer returns the server row this ingest belongs to. An explicit
// ServerID wins; otherwise the server is found by name (or created) inside
// the single default organization. The very first server keeps the fixed
// demoServerID so existing single-server installs stay stable.
func resolveServer(ctx context.Context, tx *sql.Tx, opts IngestOptions) (string, error) {
	_, err := tx.ExecContext(ctx, `
		insert into organizations (id, name) values ($1, 'Demo Organization')
		on conflict (id) do nothing
	`, demoOrganizationID)
	if err != nil {
		return "", err
	}
	if opts.ServerID != "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, `select exists (select 1 from servers where id::text = $1)`, opts.ServerID).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("server %s not found", opts.ServerID)
		}
		return opts.ServerID, nil
	}
	name := opts.ServerName
	if name == "" {
		name = "BF1942 Server"
	}
	var serverID string
	err = tx.QueryRowContext(ctx, `
		select id from servers where organization_id = $1 and name = $2 order by created_at limit 1
	`, demoOrganizationID, name).Scan(&serverID)
	if err == sql.ErrNoRows {
		serverID = newUUID()
		var hasAny bool
		if err := tx.QueryRowContext(ctx, `select exists (select 1 from servers where organization_id = $1)`, demoOrganizationID).Scan(&hasAny); err != nil {
			return "", err
		}
		if !hasAny {
			serverID = demoServerID
		}
		if _, err := tx.ExecContext(ctx, `
			insert into servers (id, organization_id, name, game, address, port)
			values ($1, $2, $3, 'bf1942', $4, $5)
		`, serverID, demoOrganizationID, name, nullableString(opts.ServerAddress), opts.ServerPort); err != nil {
			return "", err
		}
		return serverID, nil
	}
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		update servers set address = coalesce(nullif($2, ''), address), port = coalesce(nullif($3, 0), port)
		where id = $1
	`, serverID, opts.ServerAddress, opts.ServerPort)
	return serverID, err
}

// EnsureServer resolves (creating if needed) a server by name outside an
// ingest transaction, so callers can pin subsequent ingests to its id.
func (s *Store) EnsureServer(ctx context.Context, name string, address string, port int) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	serverID, err := resolveServer(ctx, tx, IngestOptions{ServerName: name, ServerAddress: address, ServerPort: port})
	if err != nil {
		return "", err
	}
	return serverID, tx.Commit()
}

// WatchedServer is one registered server as seen by the worker's server
// up/down watcher: just enough to query GameSpy and label a notification.
type WatchedServer struct {
	ID        string
	Name      string
	Address   string
	QueryPort int
}

// ListEnabledServers returns every enabled server with a configured address,
// for the worker's up/down watch loop (apps/worker only; the API's /live
// page queries this itself since it also needs the game port).
func (s *Store) ListEnabledServers(ctx context.Context) ([]WatchedServer, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id::text, name, address, coalesce(query_port, 23000)
		from servers
		where enabled and coalesce(address, '') <> ''
		order by created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WatchedServer
	for rows.Next() {
		var w WatchedServer
		if err := rows.Scan(&w.ID, &w.Name, &w.Address, &w.QueryPort); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

func ensurePlayer(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	name = truncatePlayerName(name)
	var playerID string
	err := tx.QueryRowContext(ctx, `
		select p.id
		from players p
		join player_aliases a on a.player_id = p.id
		where p.organization_id = $1 and a.alias = $2
		limit 1
	`, demoOrganizationID, name).Scan(&playerID)
	if err == nil {
		if _, err := tx.ExecContext(ctx, `
			update player_aliases set last_seen_at = now(), times_seen = times_seen + 1
			where player_id = $1 and alias = $2
		`, playerID, name); err != nil {
			// Discarding this would leave the transaction aborted (Postgres
			// fails every statement after a failed one) while every caller
			// downstream sees "current transaction is aborted" instead of
			// this, the actual cause.
			return "", err
		}
		return playerID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	return createPlayerRow(ctx, tx, name)
}

// ensurePlayerIdentity resolves a joining player to a canonical players row.
// The keyhash (the engine's per-CD-key GUID) is the identity: the same human
// keeps one players row across nickname changes, and two humans sharing a
// nickname stay separate. Players without a keyhash (bots, roundstats-only
// names) fall back to nickname matching via ensurePlayer.
func ensurePlayerIdentity(ctx context.Context, tx *sql.Tx, name string, keyhash string) (string, error) {
	name = truncatePlayerName(name)
	if keyhash == "" {
		return ensurePlayer(ctx, tx, name)
	}

	var playerID string
	err := tx.QueryRowContext(ctx, `
		select player_id from player_identities
		where identity_type = 'bf_keyhash' and identity_value = $1
	`, keyhash).Scan(&playerID)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if err == nil {
		// Known human. Absorb any nickname-keyed duplicate row created before
		// this keyhash was first seen, then record the current nickname.
		duplicateID, err := findPlayerByAliasWithoutKeyhash(ctx, tx, name, playerID)
		if err != nil {
			return "", err
		}
		if duplicateID != "" {
			if err := MergePlayers(ctx, tx, duplicateID, playerID); err != nil {
				return "", err
			}
		}
		if err := recordAlias(ctx, tx, playerID, name); err != nil {
			return "", err
		}
		// The profile shows the nickname the human joined with most recently.
		if _, err := tx.ExecContext(ctx, `update players set display_name = $2, clan_tag = nullif($3, '') where id = $1`, playerID, name, domain.ExtractClanTag(name)); err != nil {
			return "", err
		}
		return playerID, nil
	}

	// First time this keyhash appears: adopt the nickname-keyed row if one
	// exists and no other keyhash owns it, otherwise start a fresh player.
	playerID, err = findPlayerByAliasWithoutKeyhash(ctx, tx, name, "")
	if err != nil {
		return "", err
	}
	if playerID == "" {
		playerID, err = createPlayerRow(ctx, tx, name)
		if err != nil {
			return "", err
		}
	} else if err := recordAlias(ctx, tx, playerID, name); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		insert into player_identities (id, player_id, identity_type, identity_value, verified)
		values ($1, $2, 'bf_keyhash', $3, true)
	`, newUUID(), playerID, keyhash)
	return playerID, err
}

// findPlayerByAliasWithoutKeyhash finds a player known by the alias that has
// no keyhash identity of its own (excluding excludeID). Rows that already own
// a keyhash belong to a different human who happens to share the nickname.
func findPlayerByAliasWithoutKeyhash(ctx context.Context, tx *sql.Tx, alias string, excludeID string) (string, error) {
	var playerID string
	err := tx.QueryRowContext(ctx, `
		select p.id
		from players p
		join player_aliases a on a.player_id = p.id
		where p.organization_id = $1 and a.alias = $2 and p.id <> coalesce(nullif($3, ''), '00000000-0000-0000-0000-000000000000')::uuid
		  and not exists (
			select 1 from player_identities i
			where i.player_id = p.id and i.identity_type = 'bf_keyhash'
		  )
		limit 1
	`, demoOrganizationID, alias, excludeID).Scan(&playerID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return playerID, nil
}

func createPlayerRow(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	playerID := newUUID()
	_, err := tx.ExecContext(ctx, `
		insert into players (id, organization_id, display_name, clan_tag) values ($1, $2, $3, nullif($4, ''))
	`, playerID, demoOrganizationID, name, domain.ExtractClanTag(name))
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		insert into player_aliases (id, player_id, alias, first_seen_at, last_seen_at, times_seen)
		values ($1, $2, $3, now(), now(), 1)
	`, newUUID(), playerID, name)
	return playerID, err
}

func recordAlias(ctx context.Context, tx *sql.Tx, playerID string, alias string) error {
	result, err := tx.ExecContext(ctx, `
		update player_aliases set last_seen_at = now(), times_seen = times_seen + 1
		where player_id = $1 and alias = $2
	`, playerID, alias)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		insert into player_aliases (id, player_id, alias, first_seen_at, last_seen_at, times_seen)
		values ($1, $2, $3, now(), now(), 1)
		on conflict (player_id, alias) do nothing
	`, newUUID(), playerID, alias)
	return err
}

// MergePlayers folds every row belonging to fromID into intoID and removes
// fromID. Ingest calls it when a keyhash reveals that a nickname-keyed player
// row from older logs is the same human as an identity-linked player; the
// admin identity page calls it for manual merges.
func MergePlayers(ctx context.Context, tx *sql.Tx, fromID string, intoID string) error {
	if fromID == "" || intoID == "" || fromID == intoID {
		return nil
	}
	var hasAwards bool
	if err := tx.QueryRowContext(ctx, `select to_regclass('round_awards') is not null`).Scan(&hasAwards); err != nil {
		return err
	}
	statements := []string{
		// Rounds where both rows exist (a reconnect under a new name mid-round):
		// combine the counters, then drop the duplicate before repointing.
		`update round_player_stats a set
			score = a.score + b.score, kills = a.kills + b.kills, deaths = a.deaths + b.deaths,
			teamkills = a.teamkills + b.teamkills, captures = a.captures + b.captures,
			attacks = a.attacks + b.attacks, defences = a.defences + b.defences,
			objectives = a.objectives + b.objectives, objective_teamkills = a.objective_teamkills + b.objective_teamkills,
			heals = a.heals + b.heals, self_heals = a.self_heals + b.self_heals,
			repairs = a.repairs + b.repairs, other_repairs = a.other_repairs + b.other_repairs
		from round_player_stats b
		where a.round_id = b.round_id and a.player_id = $2 and b.player_id = $1`,
		`delete from round_player_stats b where b.player_id = $1
			and exists (select 1 from round_player_stats a where a.round_id = b.round_id and a.player_id = $2)`,
		`update round_player_stats set player_id = $2 where player_id = $1`,
		`update events set actor_player_id = $2 where actor_player_id = $1`,
		`update events set target_player_id = $2 where target_player_id = $1`,
		`update player_aliases a set
			times_seen = a.times_seen + b.times_seen,
			first_seen_at = least(a.first_seen_at, b.first_seen_at),
			last_seen_at = greatest(a.last_seen_at, b.last_seen_at)
		from player_aliases b
		where a.player_id = $2 and b.player_id = $1 and a.alias = b.alias`,
		`delete from player_aliases b where b.player_id = $1
			and exists (select 1 from player_aliases a where a.player_id = $2 and a.alias = b.alias)`,
		`update player_aliases set player_id = $2 where player_id = $1`,
		`update player_identities set player_id = $2 where player_id = $1`,
	}
	if hasAwards {
		statements = append(statements, `update round_awards set player_id = $2 where player_id = $1`)
	}
	statements = append(statements, `delete from players where id = $1 and id <> $2`)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, fromID, intoID); err != nil {
			return err
		}
	}
	return nil
}

// archiveRawLog stores the raw log as <sha256>__<original-name> so the archive
// stays content-addressed for dedupe while keeping the server's file name
// (which carries the round start timestamp) recoverable on re-ingest.
func archiveRawLog(dir string, originalName string, shaHex string, raw []byte) (string, error) {
	if dir == "" {
		dir = "data/raw-logs"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	base := originalLogName(originalName)
	if filepath.Ext(base) == "" {
		base += ".xml"
	}
	path := filepath.Join(dir, shaHex+"__"+base)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// originalLogName strips the content-hash prefix from archived file names so
// re-ingesting from the archive records the server's original log name.
func originalLogName(name string) string {
	base := filepath.Base(name)
	if i := strings.Index(base, "__"); i == 64 && isHexString(base[:i]) {
		return base[i+2:]
	}
	return base
}

func isHexString(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

var logNameTimestamp = regexp.MustCompile(`(\d{8})_(\d{4})`)

// timestampFromLogName recovers the round start time embedded in BF1942 event
// log names such as ev_14567-20260703_0143.xml. The DICE XML log itself only
// carries relative timestamps, so the file name is the only absolute clock.
func timestampFromLogName(name string) (time.Time, bool) {
	match := logNameTimestamp.FindStringSubmatch(filepath.Base(name))
	if match == nil {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("20060102_1504", match[1]+"_"+match[2], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func newUUID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func firstEventParam(params map[string]string, names ...string) string {
	for _, name := range names {
		if value := params[name]; value != "" {
			return value
		}
	}
	return ""
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableUUID(value any) any {
	if value == nil || value == "" {
		return nil
	}
	return value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func isCompressed(name string) bool {
	return filepath.Ext(name) == ".zxml"
}
