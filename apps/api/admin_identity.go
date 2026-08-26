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

	"github.com/hootmeow/spawnpoint/internal/storage/postgres"
)

type adminPlayerRow struct {
	ID        string
	Name      string
	Aliases   string
	Keyhashes int
	Rounds    int
	Kills     int
	IsBot     *bool
}

// adminIdentityPage lists players with their aliases and identities and
// offers manual merge / detach operations for humans the automatic keyhash
// linking cannot reach (pre-keyhash logs, renamed bots, typo'd merges).
func (s *apiServer) adminIdentityPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if r.Method == http.MethodPost {
		s.handleIdentityAction(ctx, w, r)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := struct {
		GeneratedAt time.Time
		DBError     string
		Role        string
		Query       string
		CSRF        string
		Message     string
		Players     []adminPlayerRow
	}{
		GeneratedAt: time.Now().UTC(),
		Role:        requestAdminIdentity(r).Role,
		Query:       q,
		CSRF:        requestCSRF(r),
		Message:     strings.TrimSpace(r.URL.Query().Get("msg")),
	}

	players, err := s.fetchAdminPlayers(ctx, q, 50)
	if err != nil {
		data.DBError = err.Error()
	}
	data.Players = players

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminIdentityTemplate.Execute(w, data); err != nil {
		log.Printf("render admin identity failed: %v", err)
	}
}

func (s *apiServer) handleIdentityAction(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
		http.Redirect(w, r, "/admin/identity?"+v.Encode(), http.StatusSeeOther)
	}

	action := r.PostFormValue("action")
	var err error
	switch action {
	case "merge":
		fromID := strings.TrimSpace(r.PostFormValue("from_id"))
		intoID := strings.TrimSpace(r.PostFormValue("into_id"))
		err = s.adminMergePlayers(ctx, fromID, intoID)
		if err == nil {
			s.audit(r, "", "identity_merge", fromID, "into "+intoID)
			redirect(fmt.Sprintf("Merged %s into %s.", fromID, intoID))
			return
		}
	case "detach_alias":
		playerID := strings.TrimSpace(r.PostFormValue("player_id"))
		alias := strings.TrimSpace(r.PostFormValue("alias"))
		err = s.adminDetachAlias(ctx, playerID, alias)
		if err == nil {
			s.audit(r, "", "identity_detach_alias", playerID, "alias "+alias)
			redirect(fmt.Sprintf("Detached %q into a new player. Historical rounds stay with the old player; future logs joining as %q match the new one.", alias, alias))
			return
		}
	case "drop_identity":
		playerID := strings.TrimSpace(r.PostFormValue("player_id"))
		keyhash := strings.TrimSpace(r.PostFormValue("keyhash"))
		err = s.adminDropIdentity(ctx, playerID, keyhash)
		if err == nil {
			s.audit(r, "", "identity_drop", playerID, "keyhash "+keyhash)
			redirect("Identity removed. The next log with that keyhash re-links it from scratch.")
			return
		}
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	redirect("Error: " + err.Error())
}

func (s *apiServer) adminMergePlayers(ctx context.Context, fromID string, intoID string) error {
	if fromID == "" || intoID == "" {
		return fmt.Errorf("both player ids are required")
	}
	if fromID == intoID {
		return fmt.Errorf("cannot merge a player into itself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range []string{fromID, intoID} {
		var exists bool
		if err := tx.QueryRowContext(ctx, `select exists (select 1 from players where id = nullif($1,'')::uuid)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("player %s not found", id)
		}
	}
	if err := postgres.MergePlayers(ctx, tx, fromID, intoID); err != nil {
		return err
	}
	return tx.Commit()
}

// adminDetachAlias moves an alias off a player onto a brand-new player row.
// Historical stats keep their current attribution; the new row only collects
// future rounds where someone joins under that nickname without a keyhash.
func (s *apiServer) adminDetachAlias(ctx context.Context, playerID string, alias string) error {
	if playerID == "" || alias == "" {
		return fmt.Errorf("player id and alias are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var aliasCount int
	if err := tx.QueryRowContext(ctx, `select count(*) from player_aliases where player_id = nullif($1,'')::uuid`, playerID).Scan(&aliasCount); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `delete from player_aliases where player_id = nullif($1,'')::uuid and alias = $2`, playerID, alias)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("player %s has no alias %q", playerID, alias)
	}
	if aliasCount <= 1 {
		return fmt.Errorf("refusing to detach the player's only alias")
	}
	var orgID string
	if err := tx.QueryRowContext(ctx, `select organization_id::text from players where id = nullif($1,'')::uuid`, playerID).Scan(&orgID); err != nil {
		return err
	}
	var newID string
	if err := tx.QueryRowContext(ctx, `
		insert into players (id, organization_id, display_name)
		values (gen_random_uuid(), $1::uuid, $2) returning id::text
	`, orgID, alias).Scan(&newID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into player_aliases (id, player_id, alias, first_seen_at, last_seen_at, times_seen)
		values (gen_random_uuid(), $1::uuid, $2, now(), now(), 0)
	`, newID, alias); err != nil {
		return err
	}
	// If the detached alias was the display name, fall back to another alias.
	if _, err := tx.ExecContext(ctx, `
		update players p set display_name = coalesce((
			select a.alias from player_aliases a
			where a.player_id = p.id
			order by a.times_seen desc, a.last_seen_at desc nulls last limit 1
		), p.display_name)
		where p.id = nullif($1,'')::uuid and p.display_name = $2
	`, playerID, alias); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *apiServer) adminDropIdentity(ctx context.Context, playerID string, keyhash string) error {
	if playerID == "" || keyhash == "" {
		return fmt.Errorf("player id and keyhash are required")
	}
	result, err := s.db.ExecContext(ctx, `
		delete from player_identities
		where player_id = nullif($1,'')::uuid and identity_type = 'bf_keyhash' and identity_value = $2
	`, playerID, keyhash)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("no bf_keyhash identity %q on player %s", keyhash, playerID)
	}
	return nil
}

func (s *apiServer) fetchAdminPlayers(ctx context.Context, query string, limit int) ([]adminPlayerRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			p.id::text,
			p.display_name,
			coalesce((select string_agg(a.alias, ', ' order by a.times_seen desc)
				from player_aliases a where a.player_id = p.id), ''),
			coalesce((select count(*) from player_identities i where i.player_id = p.id and i.identity_type = 'bf_keyhash'), 0)::int,
			coalesce((select count(distinct s.round_id) from round_player_stats s where s.player_id = p.id), 0)::int,
			coalesce((select sum(s.kills) from round_player_stats s where s.player_id = p.id), 0)::int,
			(select case when bool_or(s.is_bot is true) then true when bool_or(s.is_bot is false) then false else null end
				from round_player_stats s where s.player_id = p.id)
		from players p
		where $1 = ''
		   or p.display_name ilike '%' || $1 || '%'
		   or exists (select 1 from player_aliases a where a.player_id = p.id and a.alias ilike '%' || $1 || '%')
		   or exists (select 1 from player_identities i where i.player_id = p.id and i.identity_value = $1)
		order by (select count(*) from round_player_stats s where s.player_id = p.id) desc, p.display_name asc
		limit $2
	`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []adminPlayerRow
	for rows.Next() {
		var row adminPlayerRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Aliases, &row.Keyhashes, &row.Rounds, &row.Kills, &row.IsBot); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var adminIdentityTemplate = template.Must(template.New("adminIdentity").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Identity Admin - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "identity" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
{{ if .Message }}<div class="callout">{{ .Message }}</div>{{ end }}
<div class="page-title"><strong>Identity Admin</strong><span class="muted">manual merge and split for players the automatic keyhash linking cannot reach</span></div>

<form class="search" method="get" action="/admin/identity">
<input type="search" name="q" value="{{ .Query }}" placeholder="Name, alias, or keyhash"><button type="submit">Search</button></form>

<table><thead><tr><th>Player ID</th><th>Name</th><th>Type</th><th>Aliases</th><th>Keyhashes</th><th>Rounds</th><th>Kills</th></tr></thead><tbody>
{{ range .Players }}<tr><td class="mono">{{ .ID }}</td><td><a class="link" href="{{ playerURL .ID "all" }}">{{ .Name }}</a></td><td>{{ playerKind .IsBot }}</td><td class="wrap">{{ .Aliases }}</td><td>{{ .Keyhashes }}</td><td>{{ .Rounds }}</td><td>{{ .Kills }}</td></tr>
{{ else }}<tr><td colspan="7" class="muted">No players match.</td></tr>{{ end }}
</tbody></table>

<div class="grid2">
<section class="panel"><h3>Merge Players</h3>
<div class="award-blurb">Folds all rounds, events, aliases, identities, and medals of the first player into the second, then deletes the first. Irreversible.</div>
<form method="post" action="/admin/identity" class="search">
<input type="hidden" name="csrf" value="{{ .CSRF }}">
<input type="hidden" name="action" value="merge"><input type="hidden" name="q" value="{{ .Query }}">
<input type="search" name="from_id" placeholder="Merge this player ID&hellip;">
<input type="search" name="into_id" placeholder="&hellip;into this player ID">
<button type="submit">Merge</button></form></section>

<section class="panel"><h3>Detach Alias</h3>
<div class="award-blurb">Moves a nickname off a player onto a new empty player. History stays put; future rounds under that nickname go to the new player.</div>
<form method="post" action="/admin/identity" class="search">
<input type="hidden" name="csrf" value="{{ .CSRF }}">
<input type="hidden" name="action" value="detach_alias"><input type="hidden" name="q" value="{{ .Query }}">
<input type="search" name="player_id" placeholder="Player ID">
<input type="search" name="alias" placeholder="Alias to detach">
<button type="submit">Detach</button></form></section>

<section class="panel"><h3>Remove Keyhash Link</h3>
<div class="award-blurb">Deletes a bf_keyhash identity from a player (fixes a wrong automatic link). The next log with that keyhash re-links it fresh.</div>
<form method="post" action="/admin/identity" class="search">
<input type="hidden" name="csrf" value="{{ .CSRF }}">
<input type="hidden" name="action" value="drop_identity"><input type="hidden" name="q" value="{{ .Query }}">
<input type="search" name="player_id" placeholder="Player ID">
<input type="search" name="keyhash" placeholder="Keyhash value">
<button type="submit">Remove</button></form></section>
</div>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
