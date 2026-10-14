package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hootmeow/spawnpoint/domain"
)

// playerExport lets a player download their own stats — the self-serve
// access promised by /privacy — as JSON (full detail) or CSV (a flat
// section/field/value table, since a player's stats don't fit one row).
// Public by design, like every other profile page on this site: no login
// exists to gate it to "your own" data, so anyone who knows a player's id
// (visible on their public profile URL already) can fetch this.

type playerExportData struct {
	ID          string               `json:"id"`
	DisplayName string               `json:"display_name"`
	Aliases     []string             `json:"aliases"`
	Rounds      int                  `json:"rounds"`
	Kills       int                  `json:"kills"`
	Deaths      int                  `json:"deaths"`
	Score       int                  `json:"score"`
	Teamkills   int                  `json:"teamkills"`
	Heals       int                  `json:"heals"`
	Repairs     int                  `json:"repairs"`
	PlaySeconds int                  `json:"play_seconds"`
	LastSeen    string               `json:"last_seen"`
	Medals      []medalCount         `json:"medals"`
	Weapons     []weaponBreakdownRow `json:"weapons"`
	Vehicles    []weaponBreakdownRow `json:"vehicles"`
	Maps        []mapStatRow         `json:"maps"`
	GeneratedAt time.Time            `json:"generated_at"`
}

func (s *apiServer) playerExportPage(w http.ResponseWriter, r *http.Request) {
	playerID := sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("id")))
	if playerID == "" {
		http.Error(w, "missing ?id=", http.StatusBadRequest)
		return
	}
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data, err := s.fetchPlayerExport(ctx, playerID)
	if err != nil {
		http.Error(w, dbErrorMessage(err), http.StatusInternalServerError)
		return
	}
	if data == nil {
		http.NotFound(w, r)
		return
	}

	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		writePlayerExportCSV(w, *data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, exportFilename(data.DisplayName, "json")))
	_ = json.NewEncoder(w).Encode(data)
}

func (s *apiServer) fetchPlayerExport(ctx context.Context, playerID string) (*playerExportData, error) {
	summary, err := s.fetchPlayerByID(ctx, playerID)
	if err != nil || summary == nil {
		return nil, err
	}
	data := &playerExportData{
		ID: summary.ID, DisplayName: summary.DisplayName, Rounds: summary.Rounds,
		Kills: summary.Kills, Deaths: summary.Deaths, Score: summary.Score,
		Teamkills: summary.Teamkills, Heals: summary.Heals, Repairs: summary.Repairs,
		LastSeen: summary.LastSeen, GeneratedAt: time.Now().UTC(),
	}

	var err2 error
	data.Aliases, _, err2 = s.fetchPlayerAliases(ctx, playerID, summary.DisplayName)
	if err2 != nil {
		return nil, err2
	}
	data.PlaySeconds, err2 = s.fetchPlayerPlaySeconds(ctx, playerID)
	if err2 != nil {
		return nil, err2
	}
	data.Medals, err2 = s.fetchPlayerMedals(ctx, playerID)
	if err2 != nil {
		return nil, err2
	}
	data.Maps, err2 = s.fetchPlayerMapStats(ctx, playerID)
	if err2 != nil {
		return nil, err2
	}

	if breakdown, ok, mvErr := s.fetchPlayerBreakdownMV(ctx, playerID); ok {
		for _, weapon := range breakdown.Weapons {
			data.Weapons = append(data.Weapons, weaponBreakdownRow{Weapon: weapon.Code, Kills: weapon.Kills, Deaths: weapon.Deaths})
		}
		for _, vehicle := range breakdown.Vehicles {
			data.Vehicles = append(data.Vehicles, weaponBreakdownRow{Weapon: vehicle.Code, Kills: vehicle.Kills, Deaths: vehicle.Deaths})
		}
	} else {
		if mvErr != nil {
			return nil, mvErr
		}
		data.Weapons, err2 = s.fetchPlayerWeaponBreakdown(ctx, playerID)
		if err2 != nil {
			return nil, err2
		}
		data.Vehicles, err2 = s.fetchPlayerVehicleBreakdown(ctx, playerID)
		if err2 != nil {
			return nil, err2
		}
	}
	return data, nil
}

func writePlayerExportCSV(w http.ResponseWriter, data playerExportData) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, exportFilename(data.DisplayName, "csv")))
	cw := csv.NewWriter(w)
	defer cw.Flush()

	writeCSVRow(cw, "section", "field", "value")
	summaryFields := [][2]string{
		{"display_name", data.DisplayName},
		{"aliases", strings.Join(data.Aliases, "; ")},
		{"rounds", strconv.Itoa(data.Rounds)},
		{"kills", strconv.Itoa(data.Kills)},
		{"deaths", strconv.Itoa(data.Deaths)},
		{"kd", formatKD(data.Kills, data.Deaths)},
		{"score", strconv.Itoa(data.Score)},
		{"teamkills", strconv.Itoa(data.Teamkills)},
		{"heals", strconv.Itoa(data.Heals)},
		{"repairs", strconv.Itoa(data.Repairs)},
		{"play_hours", fmt.Sprintf("%.1f", float64(data.PlaySeconds)/3600)},
		{"last_seen", data.LastSeen},
	}
	for _, f := range summaryFields {
		writeCSVRow(cw, "summary", f[0], f[1])
	}
	for _, medal := range data.Medals {
		writeCSVRow(cw, "medals", domain.MedalLabel(medal.Category), strconv.Itoa(medal.Count))
	}
	for _, weapon := range data.Weapons {
		writeCSVRow(cw, "weapons", weapon.Weapon, fmt.Sprintf("%d kills / %d deaths", weapon.Kills, weapon.Deaths))
	}
	for _, vehicle := range data.Vehicles {
		writeCSVRow(cw, "vehicles", vehicle.Weapon, fmt.Sprintf("%d kills / %d deaths", vehicle.Kills, vehicle.Deaths))
	}
	for _, m := range data.Maps {
		writeCSVRow(cw, "maps", m.MapName, fmt.Sprintf("%d rounds / %d kills / %d deaths", m.Rounds, m.Kills, m.Deaths))
	}
}

// writeCSVRow sanitizes every field before writing (see csvSafe) rather than
// only the obviously player-controlled ones — cheap, and it removes the need
// to keep track of which columns are "safe" as fields get added later.
func writeCSVRow(cw *csv.Writer, fields ...string) {
	safe := make([]string, len(fields))
	for i, f := range fields {
		safe[i] = csvSafe(f)
	}
	_ = cw.Write(safe)
}

// csvSafe neutralizes CSV/formula injection. A cell whose first character is
// =, +, -, @, tab, or CR is interpreted as a formula by Excel, LibreOffice,
// and Google Sheets when the file is opened — a well-documented attack vector
// for any CSV export containing user-controlled text, which here is a
// player's own in-game nickname (e.g. a display_name of
// "=cmd|'/c calc'!A1" isn't a hypothetical, it's the standard PoC for this
// class of bug). Prefixing with a single quote is the standard mitigation:
// spreadsheet apps treat a leading apostrophe as "literal text, don't
// evaluate."
func csvSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}

var exportFilenameRe = regexp.MustCompile(`[^a-z0-9]+`)

func exportFilename(displayName string, ext string) string {
	slug := exportFilenameRe.ReplaceAllString(strings.ToLower(displayName), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "player"
	}
	return fmt.Sprintf("bf1942-%s.%s", slug, ext)
}
