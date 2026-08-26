package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type adminServerRow struct {
	ID        string
	Name      string
	Game      string
	Address   string
	Port      int
	QueryPort int
	Enabled   bool
	HasKey    bool
	Games     int
	LastGame  string
}

type adminServersData struct {
	GeneratedAt time.Time
	DBError     string
	Role        string
	Message     string
	NewKey      string
	NewKeyFor   string
	CSRF        string
	Servers     []adminServerRow
	Migrated    bool
}

// adminServersPage manages the server registry: metadata edits, enable /
// disable, and per-server ingest API keys for remote log upload.
func (s *apiServer) adminServersPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := adminServersData{
		GeneratedAt: time.Now().UTC(),
		Role:        requestAdminIdentity(r).Role,
		CSRF:        requestCSRF(r),
		Message:     strings.TrimSpace(r.URL.Query().Get("msg")),
	}

	if err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from information_schema.columns
		               where table_name = 'servers' and column_name = 'api_key_sha256')
	`).Scan(&data.Migrated); err != nil {
		data.DBError = err.Error()
	}

	if r.Method == http.MethodPost {
		if !data.Migrated {
			http.Error(w, "apply migration 0013_hash_server_api_keys.sql first", http.StatusConflict)
			return
		}
		s.handleServerAction(ctx, w, r, &data)
		if data.NewKey == "" {
			return // handleServerAction redirected
		}
	}

	if data.Migrated {
		servers, err := s.fetchAdminServers(ctx)
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
		data.Servers = servers
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminServersTemplate.Execute(w, data); err != nil {
		log.Printf("render admin servers failed: %v", err)
	}
}

// handleServerAction performs one POST action. Key rotation renders the page
// directly (via data.NewKey) so the plaintext key is shown exactly once and
// never enters a URL; every other action redirects with a message.
func (s *apiServer) handleServerAction(ctx context.Context, w http.ResponseWriter, r *http.Request, data *adminServersData) {
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
		http.Redirect(w, r, "/admin/servers?"+v.Encode(), http.StatusSeeOther)
	}

	action := r.PostFormValue("action")
	var err error
	switch action {
	case "create":
		err = s.adminCreateServer(ctx, r)
		if err == nil {
			s.audit(r, "", "server_create", strings.TrimSpace(r.PostFormValue("name")), "")
			redirect("Server added.")
			return
		}
	case "update":
		err = s.adminUpdateServer(ctx, r)
		if err == nil {
			s.audit(r, "", "server_update", strings.TrimSpace(r.PostFormValue("name")), "")
			redirect("Server updated.")
			return
		}
	case "toggle":
		id := strings.TrimSpace(r.PostFormValue("id"))
		var enabled bool
		err = s.db.QueryRowContext(ctx, `
			update servers set enabled = not enabled where id = nullif($1,'')::uuid returning enabled
		`, id).Scan(&enabled)
		if err == nil {
			state := map[bool]string{true: "enabled", false: "disabled"}[enabled]
			s.audit(r, "", "server_"+state, id, "")
			redirect(fmt.Sprintf("Server %s.", state))
			return
		}
	case "rotate_key":
		id := strings.TrimSpace(r.PostFormValue("id"))
		buf := make([]byte, 24)
		if _, err = rand.Read(buf); err == nil {
			key := "bfps_" + hex.EncodeToString(buf)
			var name string
			// Only the hash is persisted; the plaintext key is shown to the
			// operator once here and never stored (see migration 0013).
			err = s.db.QueryRowContext(ctx, `
				update servers set api_key = null, api_key_sha256 = $2 where id = nullif($1,'')::uuid returning name
			`, id, hashAPIKey(key)).Scan(&name)
			if err == nil {
				s.audit(r, "", "server_rotate_key", name, "")
				data.NewKey = key
				data.NewKeyFor = name
				return
			}
		}
	case "revoke_key":
		id := strings.TrimSpace(r.PostFormValue("id"))
		_, err = s.db.ExecContext(ctx, `update servers set api_key = null, api_key_sha256 = null where id = nullif($1,'')::uuid`, id)
		if err == nil {
			s.audit(r, "", "server_revoke_key", id, "")
			redirect("API key revoked.")
			return
		}
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	redirect("Error: " + err.Error())
}

func (s *apiServer) adminCreateServer(ctx context.Context, r *http.Request) error {
	name := strings.TrimSpace(r.PostFormValue("name"))
	game := strings.TrimSpace(r.PostFormValue("game"))
	if game == "" {
		game = "bf1942"
	}
	if name == "" {
		return fmt.Errorf("server name is required")
	}
	if game != "bf1942" && game != "bfvietnam" {
		return fmt.Errorf("game must be bf1942 or bfvietnam")
	}
	address := strings.TrimSpace(r.PostFormValue("address"))
	port := formInt(r, "port", 14567)
	queryPort := formInt(r, "query_port", 0)
	_, err := s.db.ExecContext(ctx, `
		insert into servers (id, organization_id, name, game, address, port, query_port)
		values (gen_random_uuid(), (select id from organizations order by created_at limit 1), $1, $2, nullif($3, ''), $4, nullif($5, 0))
	`, name, game, address, port, queryPort)
	return err
}

func (s *apiServer) adminUpdateServer(ctx context.Context, r *http.Request) error {
	id := strings.TrimSpace(r.PostFormValue("id"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	if id == "" || name == "" {
		return fmt.Errorf("server id and name are required")
	}
	address := strings.TrimSpace(r.PostFormValue("address"))
	port := formInt(r, "port", 14567)
	queryPort := formInt(r, "query_port", 0)
	result, err := s.db.ExecContext(ctx, `
		update servers set name = $2, address = nullif($3, ''), port = $4, query_port = nullif($5, 0)
		where id = nullif($1,'')::uuid
	`, id, name, address, port, queryPort)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("server %s not found", id)
	}
	return nil
}

func formInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.PostFormValue(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s *apiServer) fetchAdminServers(ctx context.Context) ([]adminServerRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			s.id::text, s.name, s.game,
			coalesce(s.address, ''), coalesce(s.port, 0), coalesce(s.query_port, 0),
			s.enabled, s.api_key_sha256 is not null,
			coalesce((select count(*) from games g where g.server_id = s.id), 0)::int,
			coalesce((select to_char(max(g.started_at) at time zone 'utc', 'YYYY-MM-DD HH24:MI') from games g where g.server_id = s.id), 'never')
		from servers s
		order by s.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []adminServerRow
	for rows.Next() {
		var row adminServerRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Game, &row.Address, &row.Port, &row.QueryPort, &row.Enabled, &row.HasKey, &row.Games, &row.LastGame); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var adminServersTemplate = template.Must(template.New("adminServers").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Servers - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "servers" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
{{ if not .Migrated }}<div class="error">This page needs migration <code>0013_hash_server_api_keys.sql</code>.</div>{{ end }}
{{ if .Message }}<div class="callout">{{ .Message }}</div>{{ end }}
{{ if .NewKey }}<div class="callout"><strong>New ingest API key for {{ .NewKeyFor }}:</strong> <code class="mono">{{ .NewKey }}</code><br>Copy it now &mdash; it is not shown again. Use it as <code>Authorization: Bearer &lt;key&gt;</code> on <code>POST /api/ingest/upload</code>.</div>{{ end }}
<div class="page-title"><strong>Servers</strong><span class="muted">game server registry, ingest keys, and live-query settings</span></div>

<table><thead><tr><th>Name</th><th>Game</th><th>Address</th><th>Query Port</th><th>Games</th><th>Last Game</th><th>Ingest Key</th><th>Status</th><th>Actions</th></tr></thead><tbody>
{{ $csrf := .CSRF }}
{{ range .Servers }}<tr>
<td><form method="post" action="/admin/servers" id="upd-{{ .ID }}">
<input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="update"><input type="hidden" name="id" value="{{ .ID }}"></form>
<input form="upd-{{ .ID }}" type="text" name="name" value="{{ .Name }}" style="max-width:11rem"></td>
<td>{{ .Game }}</td>
<td><input form="upd-{{ .ID }}" type="text" name="address" value="{{ .Address }}" placeholder="host or IP" style="max-width:9rem">:<input form="upd-{{ .ID }}" type="text" name="port" value="{{ .Port }}" style="max-width:4.5rem"></td>
<td><input form="upd-{{ .ID }}" type="text" name="query_port" value="{{ if .QueryPort }}{{ .QueryPort }}{{ end }}" placeholder="23000" style="max-width:4.5rem"> <button form="upd-{{ .ID }}" type="submit">Save</button></td>
<td>{{ .Games }}</td>
<td class="muted">{{ .LastGame }}</td>
<td>{{ if .HasKey }}<span class="badge">set</span>{{ else }}<span class="muted">none</span>{{ end }}</td>
<td>{{ if .Enabled }}<span class="badge">enabled</span>{{ else }}<span class="muted">disabled</span>{{ end }}</td>
<td>
<form method="post" action="/admin/servers" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="rotate_key"><input type="hidden" name="id" value="{{ .ID }}"><button type="submit">{{ if .HasKey }}Rotate Key{{ else }}Create Key{{ end }}</button></form>
{{ if .HasKey }}<form method="post" action="/admin/servers" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="revoke_key"><input type="hidden" name="id" value="{{ .ID }}"><button type="submit">Revoke</button></form>{{ end }}
<form method="post" action="/admin/servers" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="toggle"><input type="hidden" name="id" value="{{ .ID }}"><button type="submit">{{ if .Enabled }}Disable{{ else }}Enable{{ end }}</button></form>
</td></tr>
{{ else }}<tr><td colspan="9" class="muted">No servers registered.</td></tr>{{ end }}
</tbody></table>

<section class="panel"><h3>Add Server</h3>
<div class="award-blurb">Register a game server. Disabled servers reject remote log uploads. The query port is the GameSpy status port used by the live page (BF1942 default: game port + 8433, commonly 23000 for port 14567).</div>
<form method="post" action="/admin/servers" class="search">
<input type="hidden" name="csrf" value="{{ .CSRF }}"><input type="hidden" name="action" value="create">
<input type="search" name="name" placeholder="Server name">
<input type="search" name="game" placeholder="bf1942" value="bf1942">
<input type="search" name="address" placeholder="Address">
<input type="search" name="port" placeholder="14567">
<input type="search" name="query_port" placeholder="Query port (optional)">
<button type="submit">Add</button></form></section>

<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
