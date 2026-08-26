package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

type adminSettingsData struct {
	GeneratedAt   time.Time
	DBError       string
	Role          string
	Message       string
	CSRF          string
	Migrated      bool
	ChatLogPublic bool
}

// adminSettingsPage is the site-wide toggle panel: settings here affect
// every visitor immediately, so it's admin-only (see adminNav).
func (s *apiServer) adminSettingsPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := adminSettingsData{
		GeneratedAt: time.Now().UTC(),
		Role:        requestAdminIdentity(r).Role,
		CSRF:        requestCSRF(r),
		Message:     strings.TrimSpace(r.URL.Query().Get("msg")),
	}
	if err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from information_schema.tables where table_name = 'site_settings')
	`).Scan(&data.Migrated); err != nil {
		data.DBError = err.Error()
	}

	if r.Method == http.MethodPost {
		if !data.Migrated {
			http.Error(w, "apply migration 0015_site_settings.sql first", http.StatusConflict)
			return
		}
		s.handleSettingsAction(ctx, w, r)
		return
	}

	if data.Migrated {
		chatLogPublic, err := s.fetchChatLogPublic(ctx)
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
		data.ChatLogPublic = chatLogPublic
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminSettingsTemplate.Execute(w, data); err != nil {
		log.Printf("render admin settings failed: %v", err)
	}
}

func (s *apiServer) handleSettingsAction(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !checkAdminCSRF(r) {
		http.Error(w, "invalid or missing csrf token", http.StatusForbidden)
		return
	}

	chatLogPublic := r.PostFormValue("chat_log_public") == "on"
	if _, err := s.db.ExecContext(ctx, `
		update site_settings set chat_log_public = $1, updated_at = now() where singleton
	`, chatLogPublic); err != nil {
		http.Redirect(w, r, "/admin/settings?msg="+template.URLQueryEscaper("Error: "+err.Error()), http.StatusSeeOther)
		return
	}

	action := "chat_log_hidden"
	message := "Chat log hidden from public pages."
	if chatLogPublic {
		action = "chat_log_shown"
		message = "Chat log visible on public pages."
	}
	s.audit(r, "", action, "", "")
	http.Redirect(w, r, "/admin/settings?msg="+template.URLQueryEscaper(message), http.StatusSeeOther)
}

// fetchChatLogPublic reports whether the public /chat page should show
// message text. It fails open (visible) on any error, including the table
// not existing yet on an install that hasn't applied migration
// 0015_site_settings.sql, since that matches behavior before this setting
// existed.
func (s *apiServer) fetchChatLogPublic(ctx context.Context) (bool, error) {
	var visible bool
	err := s.db.QueryRowContext(ctx, `select chat_log_public from site_settings where singleton`).Scan(&visible)
	if err != nil {
		return true, err
	}
	return visible, nil
}

var adminSettingsTemplate = template.Must(template.New("adminSettings").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Settings - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "settings" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
{{ if not .Migrated }}<div class="error">This page needs migration <code>0015_site_settings.sql</code>.</div>{{ end }}
{{ if .Message }}<div class="callout">{{ .Message }}</div>{{ end }}
<div class="page-title"><strong>Site Settings</strong><span class="muted">site-wide toggles that take effect immediately</span></div>

<form method="post" action="/admin/settings">
<input type="hidden" name="csrf" value="{{ .CSRF }}">
<label><input type="checkbox" name="chat_log_public" {{ if .ChatLogPublic }}checked{{ end }}> Show the public chat log (<code>/chat</code>)</label>
<div class="award-blurb">When off, the <code>/chat</code> page tells visitors the log is hidden and stops listing messages. This does not delete anything.</div>
<button type="submit">Save</button>
</form>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
