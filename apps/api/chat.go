package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type chatRow struct {
	When     string
	PlayerID string
	Player   string
	Team     *int
	Text     string
	MapName  string
	RoundID  string
}

func (s *apiServer) chatPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const perPage = 100

	data := struct {
		GeneratedAt time.Time
		DBError     string
		Query       string
		Page        int
		PrevURL     string
		NextURL     string
		Rows        []chatRow
		Hidden      bool
	}{GeneratedAt: time.Now().UTC(), Query: q, Page: page}

	pageURL := func(p int) string {
		v := url.Values{}
		if q != "" {
			v.Set("q", q)
		}
		if p > 1 {
			v.Set("page", strconv.Itoa(p))
		}
		if enc := v.Encode(); enc != "" {
			return "/chat?" + enc
		}
		return "/chat"
	}
	if page > 1 {
		data.PrevURL = pageURL(page - 1)
	}

	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		chatLogPublic, err := s.fetchChatLogPublic(ctx)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
		if !chatLogPublic {
			data.Hidden = true
		} else {
			rows, err := s.fetchChat(ctx, q, perPage+1, (page-1)*perPage)
			if err != nil {
				data.DBError = dbErrorMessage(err)
			}
			if len(rows) > perPage {
				rows = rows[:perPage]
				data.NextURL = pageURL(page + 1)
			}
			data.Rows = rows
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := chatTemplate.Execute(w, data); err != nil {
		log.Printf("render chat failed: %v", err)
	}
}

func (s *apiServer) fetchChat(ctx context.Context, query string, limit int, offset int) ([]chatRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(to_char(coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) at time zone 'utc', 'YYYY-MM-DD HH24:MI'), ''),
			coalesce(p.id::text, ''),
			coalesce(p.display_name, 'slot ' || coalesce(e.actor_slot_id::text, '?')),
			s.team_id,
			coalesce(e.chat_text, ''),
			coalesce(g.map_name, ''),
			coalesce(e.round_id::text, '')
		from events e
		join games g on g.id = e.game_id
		left join round_player_stats s on s.round_id = e.round_id and s.slot_id = e.actor_slot_id
		left join players p on p.id = s.player_id
		where e.event_type = 'chat'
		  and coalesce(e.chat_text, '') <> ''
		  and (p.id is null or not p.hidden)
		  and ($1 = '' or e.chat_text ilike '%' || $1 || '%' or coalesce(p.display_name, '') ilike '%' || $1 || '%')
		order by coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) desc, e.sequence_no desc
		limit $2 offset $3
	`, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []chatRow
	for rows.Next() {
		var row chatRow
		if err := rows.Scan(&row.When, &row.PlayerID, &row.Player, &row.Team, &row.Text, &row.MapName, &row.RoundID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var chatTemplate = template.Must(template.New("chat").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Chat - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "chat" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Chat Log</strong><span class="muted">everything said on the battlefield</span></div>
{{ if .Hidden }}
<div class="callout">The chat log is currently hidden by an administrator.</div>
{{ else }}
<form class="search" method="get" action="/chat"><input type="search" name="q" value="{{ .Query }}" placeholder="Search text or player"><button type="submit">Search</button></form>
<table><thead><tr><th>When</th><th>Player</th><th>Team</th><th>Message</th><th>Map</th></tr></thead><tbody>
{{ range .Rows }}<tr><td class="mono">{{ .When }}</td><td>{{ if .PlayerID }}<a class="link" href="{{ playerURL .PlayerID "all" }}">{{ .Player }}</a>{{ else }}{{ .Player }}{{ end }}</td><td>{{ if .Team }}{{ teamName .Team }}{{ else }}<span class="muted">n/a</span>{{ end }}</td><td class="wrap">{{ .Text }}</td><td>{{ if .MapName }}<a class="link" href="{{ mapURL .MapName }}">{{ displayCode .MapName }}</a>{{ end }}</td></tr>
{{ else }}<tr><td colspan="5" class="muted">No chat messages{{ if .Query }} match "{{ .Query }}"{{ end }}.</td></tr>{{ end }}
</tbody></table>
<div class="filters">{{ if .PrevURL }}<a href="{{ .PrevURL }}">&larr; Newer</a>{{ end }}{{ if .NextURL }}<a href="{{ .NextURL }}">Older &rarr;</a>{{ end }}</div>
{{ end }}
<div class="stat-note"><span>Page {{ .Page }} &middot; Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
