package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

// audit appends one row to admin_audit_log. actor may be "" to derive it
// from the request's authenticated identity. Failures are logged, never
// surfaced — an audit hiccup must not block the action it describes — but
// the action itself has already been journaled to the process log either way.
func (s *apiServer) audit(r *http.Request, actor, action, target, detail string) {
	if s.db == nil {
		return
	}
	if actor == "" {
		actor = requestAdminIdentity(r).Actor
	}
	if actor == "" {
		if bearerAuthed(r) {
			actor = "admin-token"
		} else {
			actor = "-"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		insert into admin_audit_log (actor, remote_ip, action, target, detail)
		values ($1, $2, $3, nullif($4,''), nullif($5,''))
	`, actor, remoteIP(r), action, target, detail)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		log.Printf("audit log write failed (action=%s): %v", action, err)
	}
}

type auditRow struct {
	At       string
	Actor    string
	RemoteIP string
	Action   string
	Target   string
	Detail   string
}

func (s *apiServer) adminAuditPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := struct {
		GeneratedAt time.Time
		DBError     string
		Role        string
		Query       string
		Rows        []auditRow
	}{
		GeneratedAt: time.Now().UTC(),
		Role:        requestAdminIdentity(r).Role,
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
	}

	rows, err := s.db.QueryContext(ctx, `
		select
			to_char(at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS'),
			actor, coalesce(remote_ip, ''), action, coalesce(target, ''), coalesce(detail, '')
		from admin_audit_log
		where ($1 = '' or actor ilike '%' || $1 || '%' or action ilike '%' || $1 || '%' or target ilike '%' || $1 || '%')
		order by at desc
		limit 200
	`, data.Query)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			data.DBError = "This page needs migration 0014_admin_accounts.sql."
		} else {
			data.DBError = err.Error()
		}
	} else {
		defer rows.Close()
		for rows.Next() {
			var row auditRow
			if err := rows.Scan(&row.At, &row.Actor, &row.RemoteIP, &row.Action, &row.Target, &row.Detail); err != nil {
				data.DBError = err.Error()
				break
			}
			data.Rows = append(data.Rows, row)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminAuditTemplate.Execute(w, data); err != nil {
		log.Printf("render admin audit failed: %v", err)
	}
}

var adminAuditTemplate = template.Must(template.New("adminAudit").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Audit Log - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "audit" .Role }}
{{ if .DBError }}<div class="error">{{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Audit Log</strong><span class="muted">every admin action, newest first (last 200)</span></div>
<form method="get" action="/admin/audit" class="search">
<input type="search" name="q" placeholder="Filter by actor, action, or target" value="{{ .Query }}">
<button type="submit">Filter</button>
</form>
<table><thead><tr><th>When (UTC)</th><th>Actor</th><th>IP</th><th>Action</th><th>Target</th><th>Detail</th></tr></thead><tbody>
{{ range .Rows }}<tr><td class="mono">{{ .At }}</td><td>{{ .Actor }}</td><td class="mono">{{ .RemoteIP }}</td><td>{{ .Action }}</td><td class="mono">{{ .Target }}</td><td class="wrap">{{ .Detail }}</td></tr>
{{ else }}<tr><td colspan="6" class="muted">No audit entries yet.</td></tr>{{ end }}
</tbody></table>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
</main>{{ siteFooter }}</body></html>`))
