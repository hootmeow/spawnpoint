package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// bulkExportPage is a server/clan-wide analogue of playerExportPage: a flat
// dump of round-level results (one row per player per round) rather than a
// single player's lifetime summary, for anyone who wants to run their own
// analysis outside this site. Public by design like every other page here,
// bounded by maxBulkExportRows so a wide-open filter set can't turn into an
// unbounded query.
const maxBulkExportRows = 50000

type roundExportRow struct {
	RoundID    string `json:"round_id"`
	MapName    string `json:"map_name"`
	ServerName string `json:"server_name"`
	EndedAt    string `json:"ended_at"`
	PlayerID   string `json:"player_id"`
	PlayerName string `json:"player_name"`
	Kills      int    `json:"kills"`
	Deaths     int    `json:"deaths"`
	Score      int    `json:"score"`
}

type bulkExportData struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Rows        []roundExportRow `json:"rows"`
}

const bulkRoundExportQuery = `
	select r.id::text, coalesce(nullif(g.map_name, ''), 'unknown'), sv.name,
		coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), ''),
		p.id::text, p.display_name, s.kills, s.deaths, s.score
	from round_player_stats s
	join rounds r on r.id = s.round_id
	join games g on g.id = r.game_id
	join servers sv on sv.id = g.server_id
	join players p on p.id = s.player_id and not p.hidden
	where ($1 = '' or p.clan_tag = $1)
	and ($2 = '' or g.server_id = nullif($2,'')::uuid)
	and ($3 = '' or r.ended_at >= nullif($3,'')::timestamptz)
	order by r.ended_at desc nulls last, r.id, p.display_name asc
	limit $4`

func (s *apiServer) bulkExportPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	clan := strings.TrimSpace(r.URL.Query().Get("clan"))

	serverOptions, _ := s.fetchServerOptions(ctx)
	serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)

	since := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		if _, err := time.Parse("2006-01-02", raw); err == nil {
			since = raw
		}
	}
	// rounds.ended_at has no index, so an unfiltered request forces a full
	// sequential scan of round_player_stats to evaluate any date predicate at
	// all (unlike clan/server, which are indexed). Defaulting the window
	// keeps the zero-params request - the one a crawler or curious visitor
	// is most likely to make - bounded; a caller who explicitly wants full
	// history can still pass since=2000-01-01.
	if clan == "" && serverFilter == "" && since == "" {
		since = time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02")
	}

	rows, err := s.fetchBulkRoundExport(ctx, clan, serverFilter, since)
	if err != nil {
		http.Error(w, dbErrorMessage(err), http.StatusInternalServerError)
		return
	}

	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		writeBulkExportCSV(w, rows)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, bulkExportFilename(clan, "json")))
	_ = json.NewEncoder(w).Encode(bulkExportData{GeneratedAt: time.Now().UTC(), Rows: rows})
}

func (s *apiServer) fetchBulkRoundExport(ctx context.Context, clan string, serverID string, since string) ([]roundExportRow, error) {
	rows, err := s.db.QueryContext(ctx, bulkRoundExportQuery, clan, serverID, since, maxBulkExportRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []roundExportRow
	for rows.Next() {
		var row roundExportRow
		if err := rows.Scan(&row.RoundID, &row.MapName, &row.ServerName, &row.EndedAt, &row.PlayerID, &row.PlayerName, &row.Kills, &row.Deaths, &row.Score); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func writeBulkExportCSV(w http.ResponseWriter, rows []roundExportRow) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, bulkExportFilename("", "csv")))
	cw := csv.NewWriter(w)
	defer cw.Flush()

	writeCSVRow(cw, "round_id", "map_name", "server_name", "ended_at", "player_id", "player_name", "kills", "deaths", "score")
	for _, row := range rows {
		writeCSVRow(cw, row.RoundID, row.MapName, row.ServerName, row.EndedAt, row.PlayerID, row.PlayerName,
			strconv.Itoa(row.Kills), strconv.Itoa(row.Deaths), strconv.Itoa(row.Score))
	}
}

func bulkExportFilename(clan string, ext string) string {
	slug := exportFilenameRe.ReplaceAllString(strings.ToLower(clan), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "rounds"
	}
	return fmt.Sprintf("bf1942-%s.%s", slug, ext)
}
