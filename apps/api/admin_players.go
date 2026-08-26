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

type moderationPlayerRow struct {
	ID          string
	Name        string
	Aliases     string
	Rounds      int
	Kills       int
	IsBot       *bool
	Hidden      bool
	BotOverride *bool
}

type adminPlayersData struct {
	GeneratedAt time.Time
	DBError     string
	Role        string
	Message     string
	Query       string
	CSRF        string
	Players     []moderationPlayerRow
	Migrated    bool
}

// adminPlayersPage is the moderation surface: hide players from public
// pages and correct bot/human classification when the logs get it wrong.
func (s *apiServer) adminPlayersPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := adminPlayersData{
		GeneratedAt: time.Now().UTC(),
		Role:        requestAdminIdentity(r).Role,
		CSRF:        requestCSRF(r),
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
		Message:     strings.TrimSpace(r.URL.Query().Get("msg")),
	}
	if err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from information_schema.columns
		               where table_name = 'players' and column_name = 'hidden')
	`).Scan(&data.Migrated); err != nil {
		data.DBError = err.Error()
	}

	if r.Method == http.MethodPost {
		if !data.Migrated {
			http.Error(w, "apply migration 0005_admin_management.sql first", http.StatusConflict)
			return
		}
		s.handlePlayerModeration(ctx, w, r)
		return
	}

	if data.Migrated {
		players, err := s.fetchModerationPlayers(ctx, data.Query, 50)
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
		data.Players = players
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminPlayersTemplate.Execute(w, data); err != nil {
		log.Printf("render admin players failed: %v", err)
	}
}

func (s *apiServer) handlePlayerModeration(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !checkAdminCSRF(r) {
		http.Error(w, "invalid or missing csrf token", http.StatusForbidden)
		return
	}
	redirect := func(message string) {
		v := url.Values{}
		v.Set("msg", message)
		if q := strings.TrimSpace(r.PostFormValue("q")); q != "" {
			v.Set("q", q)
		}
		http.Redirect(w, r, "/admin/players?"+v.Encode(), http.StatusSeeOther)
	}

	id := strings.TrimSpace(r.PostFormValue("id"))
	if id == "" {
		redirect("Error: player id is required")
		return
	}
	var err error
	switch action := r.PostFormValue("action"); action {
	case "hide", "unhide":
		result, execErr := s.db.ExecContext(ctx, `
			update players set hidden = $2 where id = nullif($1,'')::uuid
		`, id, action == "hide")
		err = execErr
		if err == nil {
			if n, _ := result.RowsAffected(); n == 0 {
				err = fmt.Errorf("player %s not found", id)
			}
		}
		if err == nil {
			s.audit(r, "", "player_"+action, id, "")
			redirect(map[string]string{"hide": "Player hidden from public pages.", "unhide": "Player visible again."}[action])
			return
		}
	case "mark_bot", "mark_human", "mark_auto":
		err = s.setBotOverride(ctx, id, action)
		if err == nil {
			s.audit(r, "", "player_"+action, id, "")
			redirect(map[string]string{
				"mark_bot":   "Marked as bot; existing rounds updated.",
				"mark_human": "Marked as human; existing rounds updated.",
				"mark_auto":  "Override cleared; future rounds trust the logs again (existing rounds unchanged).",
			}[r.PostFormValue("action")])
			return
		}
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	redirect("Error: " + err.Error())
}

// setBotOverride records the override on the player and, for bot/human,
// rewrites existing round rows so every page agrees immediately. Ingest
// reapplies the override to future rounds.
func (s *apiServer) setBotOverride(ctx context.Context, id string, action string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var override interface{}
	switch action {
	case "mark_bot":
		override = true
	case "mark_human":
		override = false
	case "mark_auto":
		override = nil
	}
	result, err := tx.ExecContext(ctx, `update players set bot_override = $2 where id = nullif($1,'')::uuid`, id, override)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("player %s not found", id)
	}
	if override != nil {
		if _, err := tx.ExecContext(ctx, `
			update round_player_stats set is_bot = $2 where player_id = nullif($1,'')::uuid and is_bot is distinct from $2
		`, id, override); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *apiServer) fetchModerationPlayers(ctx context.Context, query string, limit int) ([]moderationPlayerRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			p.id::text,
			p.display_name,
			coalesce((select string_agg(a.alias, ', ' order by a.times_seen desc)
				from player_aliases a where a.player_id = p.id), ''),
			coalesce((select count(distinct s.round_id) from round_player_stats s where s.player_id = p.id), 0)::int,
			coalesce((select sum(s.kills) from round_player_stats s where s.player_id = p.id), 0)::int,
			(select case when bool_or(s.is_bot is true) then true when bool_or(s.is_bot is false) then false else null end
				from round_player_stats s where s.player_id = p.id),
			p.hidden,
			p.bot_override
		from players p
		where $1 = ''
		   or p.display_name ilike '%' || $1 || '%'
		   or exists (select 1 from player_aliases a where a.player_id = p.id and a.alias ilike '%' || $1 || '%')
		order by (select count(*) from round_player_stats s where s.player_id = p.id) desc, p.display_name asc
		limit $2
	`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []moderationPlayerRow
	for rows.Next() {
		var row moderationPlayerRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Aliases, &row.Rounds, &row.Kills, &row.IsBot, &row.Hidden, &row.BotOverride); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var adminPlayersTemplate = template.Must(template.New("adminPlayers").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Player Moderation - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "players" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
{{ if not .Migrated }}<div class="error">This page needs migration <code>0005_admin_management.sql</code>.</div>{{ end }}
{{ if .Message }}<div class="callout">{{ .Message }}</div>{{ end }}
<div class="page-title"><strong>Player Moderation</strong><span class="muted">hide players from public pages and fix bot/human classification</span></div>
<div class="award-blurb">Hidden players disappear from player lists, search, leaderboards, and awards; round history pages keep the record. Bot/human overrides rewrite existing rounds and stick for future ingests.</div>

<form class="search" method="get" action="/admin/players">
<input type="search" name="q" value="{{ .Query }}" placeholder="Name or alias"><button type="submit">Search</button></form>

<table><thead><tr><th>Name</th><th>Aliases</th><th>Rounds</th><th>Kills</th><th>Type</th><th>Override</th><th>Visibility</th><th>Actions</th></tr></thead><tbody>
{{ $csrf := .CSRF }}{{ $q := .Query }}
{{ range .Players }}<tr>
<td><a class="link" href="{{ playerURL .ID "all" }}">{{ .Name }}</a></td>
<td class="wrap">{{ .Aliases }}</td>
<td>{{ .Rounds }}</td>
<td>{{ .Kills }}</td>
<td>{{ playerKind .IsBot }}</td>
<td>{{ if .BotOverride }}{{ if deref .BotOverride }}forced bot{{ else }}forced human{{ end }}{{ else }}<span class="muted">auto</span>{{ end }}</td>
<td>{{ if .Hidden }}<span class="muted">hidden</span>{{ else }}<span class="badge">visible</span>{{ end }}</td>
<td>
<form method="post" action="/admin/players" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="q" value="{{ $q }}"><input type="hidden" name="id" value="{{ .ID }}"><input type="hidden" name="action" value="{{ if .Hidden }}unhide{{ else }}hide{{ end }}"><button type="submit">{{ if .Hidden }}Unhide{{ else }}Hide{{ end }}</button></form>
<form method="post" action="/admin/players" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="q" value="{{ $q }}"><input type="hidden" name="id" value="{{ .ID }}"><input type="hidden" name="action" value="mark_bot"><button type="submit">Bot</button></form>
<form method="post" action="/admin/players" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="q" value="{{ $q }}"><input type="hidden" name="id" value="{{ .ID }}"><input type="hidden" name="action" value="mark_human"><button type="submit">Human</button></form>
{{ if .BotOverride }}<form method="post" action="/admin/players" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="q" value="{{ $q }}"><input type="hidden" name="id" value="{{ .ID }}"><input type="hidden" name="action" value="mark_auto"><button type="submit">Auto</button></form>{{ end }}
</td></tr>
{{ else }}<tr><td colspan="8" class="muted">No players match.</td></tr>{{ end }}
</tbody></table>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
