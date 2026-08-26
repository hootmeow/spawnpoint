package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

type adminUserRow struct {
	ID        string
	Username  string
	Role      string
	Disabled  bool
	Sessions  int
	CreatedAt string
	LastLogin string
}

type adminUsersData struct {
	GeneratedAt time.Time
	DBError     string
	Message     string
	CSRF        string
	Role        string
	Actor       string
	Users       []adminUserRow
	Migrated    bool
}

// adminUsersPage manages named admin accounts: create, reset password,
// change role, disable/enable, and revoke sessions. Admin role only.
func (s *apiServer) adminUsersPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	identity := requestAdminIdentity(r)
	data := adminUsersData{
		GeneratedAt: time.Now().UTC(),
		CSRF:        requestCSRF(r),
		Role:        identity.Role,
		Actor:       identity.Actor,
		Message:     strings.TrimSpace(r.URL.Query().Get("msg")),
	}

	if err := s.db.QueryRowContext(ctx, `select to_regclass('public.admin_users') is not null`).Scan(&data.Migrated); err != nil {
		data.DBError = err.Error()
	}

	if r.Method == http.MethodPost {
		if !data.Migrated {
			http.Error(w, "apply migration 0014_admin_accounts.sql first", http.StatusConflict)
			return
		}
		s.handleUserAction(ctx, w, r)
		return
	}

	if data.Migrated {
		users, err := s.fetchAdminUsers(ctx)
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
		data.Users = users
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminUsersTemplate.Execute(w, data); err != nil {
		log.Printf("render admin users failed: %v", err)
	}
}

// runCreateAdmin is the non-interactive counterpart to the "Create Account"
// form on /admin/users: setup.sh calls it (spawnpoint-api -create-admin)
// to provision the operator's first real account, so first login doesn't
// require bootstrapping through the ADMIN_TOKEN by hand. Always grants the
// admin role and re-enables/updates the account if the username already
// exists, so re-running setup.sh with the same username resets the password.
func runCreateAdmin() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("create-admin: DATABASE_URL is required")
	}
	username := strings.TrimSpace(os.Getenv("ADMIN_CREATE_USERNAME"))
	password := os.Getenv("ADMIN_CREATE_PASSWORD")
	if username == "" {
		log.Fatal("create-admin: ADMIN_CREATE_USERNAME is required")
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		log.Fatalf("create-admin: open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var migrated bool
	if err := db.QueryRowContext(ctx, `select to_regclass('public.admin_users') is not null`).Scan(&migrated); err != nil {
		log.Fatalf("create-admin: %v", err)
	}
	if !migrated {
		log.Fatal("create-admin: apply migration 0014_admin_accounts.sql first")
	}

	hash, err := hashPassword(password)
	if err != nil {
		log.Fatalf("create-admin: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		insert into admin_users (username, password_hash, role, disabled)
		values ($1, $2, 'admin', false)
		on conflict (username) do update set password_hash = excluded.password_hash, role = 'admin', disabled = false
	`, username, hash)
	if err != nil {
		log.Fatalf("create-admin: %v", err)
	}
	fmt.Printf("admin account ready: %s\n", username)
}

const minPasswordLength = 10

func hashPassword(password string) (string, error) {
	if len(password) < minPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	// bcrypt silently truncates past 72 bytes; refuse instead of pretending.
	if len(password) > 72 {
		return "", fmt.Errorf("password must be at most 72 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func (s *apiServer) handleUserAction(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
		http.Redirect(w, r, "/admin/users?"+v.Encode(), http.StatusSeeOther)
	}

	id := strings.TrimSpace(r.PostFormValue("id"))
	username := strings.TrimSpace(r.PostFormValue("username"))
	var err error
	switch action := r.PostFormValue("action"); action {
	case "create":
		role := r.PostFormValue("role")
		if role != roleAdmin && role != roleModerator {
			role = roleModerator
		}
		if username == "" {
			err = fmt.Errorf("username is required")
			break
		}
		var hash string
		if hash, err = hashPassword(r.PostFormValue("password")); err != nil {
			break
		}
		_, err = s.db.ExecContext(ctx, `
			insert into admin_users (username, password_hash, role) values ($1, $2, $3)
		`, username, hash, role)
		if err == nil {
			s.audit(r, "", "user_create", username, "role "+role)
			redirect(fmt.Sprintf("Created %s (%s).", username, role))
			return
		}
	case "reset_password":
		var hash string
		if hash, err = hashPassword(r.PostFormValue("password")); err != nil {
			break
		}
		var name string
		err = s.db.QueryRowContext(ctx, `
			update admin_users set password_hash = $2 where id = nullif($1,'')::uuid returning username
		`, id, hash).Scan(&name)
		if err == nil {
			// A password reset means the old credential may be compromised
			// (or simply shared); drop the user's open sessions with it.
			_, _ = s.db.ExecContext(ctx, `delete from admin_sessions where user_id = nullif($1,'')::uuid`, id)
			s.audit(r, "", "user_reset_password", name, "sessions revoked")
			redirect(fmt.Sprintf("Password reset for %s; their sessions were signed out.", name))
			return
		}
	case "set_role":
		role := r.PostFormValue("role")
		if role != roleAdmin && role != roleModerator {
			err = fmt.Errorf("role must be admin or moderator")
			break
		}
		// Only demotions can lock the admin area; promotions are always safe.
		if role == roleModerator {
			if err = s.guardLastAdmin(ctx, id, "demote"); err != nil {
				break
			}
		}
		var name string
		err = s.db.QueryRowContext(ctx, `
			update admin_users set role = $2 where id = nullif($1,'')::uuid returning username
		`, id, role).Scan(&name)
		if err == nil {
			// Open sessions carry their role; refresh them so the change
			// applies immediately in both directions.
			_, _ = s.db.ExecContext(ctx, `update admin_sessions set role = $2 where user_id = nullif($1,'')::uuid`, id, role)
			s.audit(r, "", "user_set_role", name, "now "+role)
			redirect(fmt.Sprintf("%s is now %s.", name, role))
			return
		}
	case "toggle":
		if err = s.guardLastAdmin(ctx, id, "disable"); err != nil {
			break
		}
		var name string
		var disabled bool
		err = s.db.QueryRowContext(ctx, `
			update admin_users set disabled = not disabled where id = nullif($1,'')::uuid returning username, disabled
		`, id).Scan(&name, &disabled)
		if err == nil {
			if disabled {
				_, _ = s.db.ExecContext(ctx, `delete from admin_sessions where user_id = nullif($1,'')::uuid`, id)
			}
			state := map[bool]string{true: "disabled", false: "enabled"}[disabled]
			s.audit(r, "", "user_"+state, name, "")
			redirect(fmt.Sprintf("%s %s.", name, state))
			return
		}
	case "revoke_sessions":
		var name string
		err = s.db.QueryRowContext(ctx, `select username from admin_users where id = nullif($1,'')::uuid`, id).Scan(&name)
		if err == nil {
			_, err = s.db.ExecContext(ctx, `delete from admin_sessions where user_id = nullif($1,'')::uuid`, id)
		}
		if err == nil {
			s.audit(r, "", "user_revoke_sessions", name, "")
			redirect(fmt.Sprintf("All sessions for %s signed out.", name))
			return
		}
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	redirect("Error: " + err.Error())
}

// guardLastAdmin refuses to demote or disable the only enabled admin
// account when no break-glass ADMIN_TOKEN exists — that would lock everyone
// out of the admin area permanently.
func (s *apiServer) guardLastAdmin(ctx context.Context, id string, verb string) error {
	if adminToken() != "" {
		return nil
	}
	var lastAdmin bool
	err := s.db.QueryRowContext(ctx, `
		select exists (
			select 1 from admin_users where id = nullif($1,'')::uuid and role = 'admin' and not disabled
		) and not exists (
			select 1 from admin_users where id <> nullif($1,'')::uuid and role = 'admin' and not disabled
		)
	`, id).Scan(&lastAdmin)
	if err != nil {
		return err
	}
	if lastAdmin {
		return fmt.Errorf("cannot %s the only admin account with no ADMIN_TOKEN configured — that would lock the admin area", verb)
	}
	return nil
}

func (s *apiServer) fetchAdminUsers(ctx context.Context) ([]adminUserRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			u.id::text, u.username, u.role, u.disabled,
			(select count(*) from admin_sessions ses where ses.user_id = u.id and ses.expires_at > now())::int,
			to_char(u.created_at at time zone 'utc', 'YYYY-MM-DD'),
			coalesce(to_char(u.last_login_at at time zone 'utc', 'YYYY-MM-DD HH24:MI'), 'never')
		from admin_users u
		order by u.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []adminUserRow
	for rows.Next() {
		var row adminUserRow
		if err := rows.Scan(&row.ID, &row.Username, &row.Role, &row.Disabled, &row.Sessions, &row.CreatedAt, &row.LastLogin); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var adminUsersTemplate = template.Must(template.New("adminUsers").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Users - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "users" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
{{ if not .Migrated }}<div class="error">This page needs migration <code>0014_admin_accounts.sql</code>.</div>{{ end }}
{{ if .Message }}<div class="callout">{{ .Message }}</div>{{ end }}
<div class="page-title"><strong>Admin Users</strong><span class="muted">signed in as {{ .Actor }} &middot; accounts, roles, and sessions</span></div>

<table><thead><tr><th>Username</th><th>Role</th><th>Status</th><th>Active Sessions</th><th>Created</th><th>Last Login</th><th>Actions</th></tr></thead><tbody>
{{ $csrf := .CSRF }}
{{ range .Users }}<tr>
<td>{{ .Username }}</td>
<td>{{ .Role }}</td>
<td>{{ if .Disabled }}<span class="muted">disabled</span>{{ else }}<span class="badge">active</span>{{ end }}</td>
<td>{{ .Sessions }}</td>
<td class="muted">{{ .CreatedAt }}</td>
<td class="muted">{{ .LastLogin }}</td>
<td>
<form method="post" action="/admin/users" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="reset_password"><input type="hidden" name="id" value="{{ .ID }}"><input type="password" name="password" placeholder="New password" autocomplete="new-password" style="max-width:10rem"><button type="submit">Reset</button></form>
<form method="post" action="/admin/users" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="set_role"><input type="hidden" name="id" value="{{ .ID }}"><input type="hidden" name="role" value="{{ if eq .Role "admin" }}moderator{{ else }}admin{{ end }}"><button type="submit">Make {{ if eq .Role "admin" }}Moderator{{ else }}Admin{{ end }}</button></form>
{{ if gt .Sessions 0 }}<form method="post" action="/admin/users" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="revoke_sessions"><input type="hidden" name="id" value="{{ .ID }}"><button type="submit">Sign Out All</button></form>{{ end }}
<form method="post" action="/admin/users" style="display:inline"><input type="hidden" name="csrf" value="{{ $csrf }}"><input type="hidden" name="action" value="toggle"><input type="hidden" name="id" value="{{ .ID }}"><button type="submit">{{ if .Disabled }}Enable{{ else }}Disable{{ end }}</button></form>
</td></tr>
{{ else }}<tr><td colspan="7" class="muted">No accounts yet — create the first one below (make it an admin).</td></tr>{{ end }}
</tbody></table>

<section class="panel"><h3>Create Account</h3>
<div class="award-blurb">Admins can do everything. Moderators get the dashboards, player moderation, and identity tools — but not the server registry, ingest keys, user management, or this audit trail. Passwords need at least 10 characters.</div>
<form method="post" action="/admin/users" class="search">
<input type="hidden" name="csrf" value="{{ .CSRF }}"><input type="hidden" name="action" value="create">
<input type="text" name="username" placeholder="Username" autocomplete="off">
<input type="password" name="password" placeholder="Password (10+ chars)" autocomplete="new-password">
<select name="role"><option value="admin">admin</option><option value="moderator">moderator</option></select>
<button type="submit">Create</button></form></section>

<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
