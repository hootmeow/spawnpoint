package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hootmeow/spawnpoint/internal/gamespy"
)

const defaultQueryPort = 23000

// liveQueryTTL is how long a GameSpy query result is reused before the game
// server is asked again. /live is deliberately excluded from the page cache
// (it should feel live), but without this every page view — including every
// crawler hit — would fire a UDP query at every registered game server. A few
// seconds of staleness is invisible next to the page's own 15s meta-refresh.
const liveQueryTTL = 10 * time.Second

type liveQueryResult struct {
	values  map[string]string
	err     error
	expires time.Time
}

var liveQueries = struct {
	sync.Mutex
	entries map[string]liveQueryResult
}{entries: map[string]liveQueryResult{}}

// cachedGamespyQuery memoizes gamespy.Query per server for liveQueryTTL.
// Concurrent cache misses for the same server may each query once; that's
// harmless and not worth single-flight machinery.
func cachedGamespyQuery(address string, port int, timeout time.Duration) (map[string]string, error) {
	key := fmt.Sprintf("%s:%d", address, port)
	now := time.Now()
	liveQueries.Lock()
	if entry, ok := liveQueries.entries[key]; ok && now.Before(entry.expires) {
		liveQueries.Unlock()
		return entry.values, entry.err
	}
	liveQueries.Unlock()
	values, err := gamespy.Query(address, port, timeout)
	liveQueries.Lock()
	liveQueries.entries[key] = liveQueryResult{values: values, err: err, expires: time.Now().Add(liveQueryTTL)}
	liveQueries.Unlock()
	return values, err
}

type liveServerStatus struct {
	Name       string
	Address    string
	QueryAddr  string
	Online     bool
	Error      string
	Hostname   string
	MapName    string
	GameType   string
	NumPlayers string
	MaxPlayers string
	RoundTime  string
	TimeLeft   string
	Teams      []liveTeamStatus
	Players    []livePlayerStatus
}

type liveTeamStatus struct {
	Name    string
	Tickets string
}

type livePlayerStatus struct {
	Name   string
	Score  int
	Kills  string
	Deaths string
	Ping   string
	Team   string
}

// liveStatusFor turns a raw GameSpy key/value map into display data.
func liveStatusFor(values map[string]string) liveServerStatus {
	status := liveServerStatus{
		Online:     true,
		Hostname:   values["hostname"],
		MapName:    values["mapname"],
		GameType:   values["gametype"],
		NumPlayers: values["numplayers"],
		MaxPlayers: values["maxplayers"],
		RoundTime:  values["roundtime"],
		TimeLeft:   formatRoundTimeRemaining(values["roundtimeremain"]),
	}
	for teamIndex := 0; ; teamIndex++ {
		name, ok := values[fmt.Sprintf("teamname_%d", teamIndex)]
		if !ok {
			break
		}
		status.Teams = append(status.Teams, liveTeamStatus{
			Name:    name,
			Tickets: values[fmt.Sprintf("tickets%d", teamIndex+1)],
		})
	}
	for playerIndex := 0; ; playerIndex++ {
		name, ok := values[fmt.Sprintf("playername_%d", playerIndex)]
		if !ok {
			name, ok = values[fmt.Sprintf("player_%d", playerIndex)]
		}
		if !ok {
			break
		}
		score, _ := strconv.Atoi(values[fmt.Sprintf("score_%d", playerIndex)])
		team := values[fmt.Sprintf("team_%d", playerIndex)]
		if teamIndex, err := strconv.Atoi(team); err == nil && teamIndex >= 0 && teamIndex < len(status.Teams) {
			team = status.Teams[teamIndex].Name
		}
		status.Players = append(status.Players, livePlayerStatus{
			Name:   name,
			Score:  score,
			Kills:  values[fmt.Sprintf("kills_%d", playerIndex)],
			Deaths: values[fmt.Sprintf("deaths_%d", playerIndex)],
			Ping:   values[fmt.Sprintf("ping_%d", playerIndex)],
			Team:   team,
		})
	}
	sort.SliceStable(status.Players, func(a, b int) bool { return status.Players[a].Score > status.Players[b].Score })
	return status
}

func formatRoundTimeRemaining(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value
	}
	if parsed < 0 {
		parsed = 0
	}
	seconds := int(parsed + 0.5)
	return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
}

func (s *apiServer) livePage(w http.ResponseWriter, r *http.Request) {
	data := struct {
		GeneratedAt time.Time
		DBError     string
		Servers     []liveServerStatus
	}{GeneratedAt: time.Now().UTC()}

	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		rows, err := s.db.QueryContext(ctx, `
			select name, coalesce(address, ''), coalesce(port, 14567), coalesce(query_port, 0)
			from servers
			where enabled
			order by created_at
		`)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		} else {
			defer rows.Close()
			type target struct {
				name      string
				address   string
				queryPort int
			}
			var targets []target
			for rows.Next() {
				var t target
				var gamePort int
				if err := rows.Scan(&t.name, &t.address, &gamePort, &t.queryPort); err != nil {
					data.DBError = dbErrorMessage(err)
					break
				}
				if t.queryPort == 0 {
					t.queryPort = defaultQueryPort
				}
				targets = append(targets, t)
			}
			results := make([]liveServerStatus, len(targets))
			var wg sync.WaitGroup
			for i, t := range targets {
				wg.Add(1)
				go func(i int, t target) {
					defer wg.Done()
					result := liveServerStatus{Name: t.name, Address: t.address, QueryAddr: fmt.Sprintf("%s:%d", t.address, t.queryPort)}
					if t.address == "" {
						result.Error = "no address configured"
						results[i] = result
						return
					}
					values, err := cachedGamespyQuery(t.address, t.queryPort, 2*time.Second)
					if err != nil {
						result.Error = "no response (server down or query port unreachable)"
						results[i] = result
						return
					}
					live := liveStatusFor(values)
					live.Name, live.Address, live.QueryAddr = result.Name, result.Address, result.QueryAddr
					results[i] = live
				}(i, t)
			}
			wg.Wait()
			data.Servers = results
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := liveTemplate.Execute(w, data); err != nil {
		log.Printf("render live page failed: %v", err)
	}
}

var liveTemplate = template.Must(template.New("live").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="refresh" content="15"><title>Live - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "live" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Live Servers</strong><span class="muted">queried over GameSpy just now &middot; refreshes every 15s</span></div>

{{ range .Servers }}
<section class="panel">
<h3>{{ if .Hostname }}{{ .Hostname }}{{ else }}{{ .Name }}{{ end }} {{ if .Online }}<span class="badge">online</span>{{ else }}<span class="muted">offline</span>{{ end }}</h3>
{{ if .Online }}
<div class="kpi-grid">
<div class="kpi-card"><span>Map</span><strong>{{ displayCode .MapName }}</strong></div>
<div class="kpi-card gold"><span>Players</span><strong>{{ .NumPlayers }} / {{ .MaxPlayers }}</strong></div>
<div class="kpi-card"><span>Mode</span><strong>{{ .GameType }}</strong></div>
{{ if .TimeLeft }}<div class="kpi-card green"><span>Round Time Left</span><strong>{{ .TimeLeft }}</strong></div>{{ end }}
{{ range .Teams }}<div class="kpi-card"><span>{{ .Name }} tickets</span><strong>{{ .Tickets }}</strong></div>{{ end }}
</div>
{{ if .Players }}
<table><thead><tr><th>Player</th><th>Team</th><th>Score</th><th>Kills</th><th>Deaths</th><th>Ping</th></tr></thead><tbody>
{{ range .Players }}<tr><td>{{ .Name }}</td><td>{{ .Team }}</td><td>{{ .Score }}</td><td>{{ .Kills }}</td><td>{{ .Deaths }}</td><td>{{ .Ping }}</td></tr>{{ end }}
</tbody></table>
{{ else }}<div class="muted">No players connected.</div>{{ end }}
{{ else }}<div class="muted">{{ .Error }} <span class="mono">({{ .QueryAddr }})</span></div>{{ end }}
</section>
{{ else }}<div class="muted">No enabled servers registered. Add one in <a class="link" href="/admin/servers">/admin/servers</a>.</div>{{ end }}
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
