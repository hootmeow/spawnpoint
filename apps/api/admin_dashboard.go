package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

// adminNav renders the sub-navigation shared by every admin page. role
// hides the admin-only areas from moderators (the routes themselves are
// enforced by requireRole regardless).
func adminNav(active string, role string) template.HTML {
	items := []struct {
		Key       string
		Label     string
		Href      string
		AdminOnly bool
	}{
		{"overview", "Admin Overview", "/admin", false},
		{"status", "Status", "/admin/status", false},
		{"ingest", "Ingest", "/admin/ingest", false},
		{"servers", "Servers", "/admin/servers", true},
		{"players", "Players", "/admin/players", false},
		{"identity", "Identity", "/admin/identity", false},
		{"users", "Users", "/admin/users", true},
		{"audit", "Audit", "/admin/audit", true},
		{"settings", "Settings", "/admin/settings", true},
	}
	var b strings.Builder
	b.WriteString(`<div class="section-tabs">`)
	for _, item := range items {
		if item.AdminOnly && role != roleAdmin {
			continue
		}
		class := ""
		if item.Key == active {
			class = ` class="active"`
		}
		b.WriteString(`<a href="`)
		b.WriteString(template.HTMLEscapeString(item.Href))
		b.WriteString(`"`)
		b.WriteString(class)
		b.WriteString(`>`)
		b.WriteString(template.HTMLEscapeString(item.Label))
		b.WriteString(`</a>`)
	}
	b.WriteString(`<a href="/admin/logout" style="margin-left:auto">Logout</a></div>`)
	return template.HTML(b.String())
}

type adminHomeData struct {
	GeneratedAt   time.Time
	Version       string
	Role          string
	DBError       string
	Totals        []keyValueRow
	Ingest        adminIngestHealth
	Jobs          []keyValueRow
	AdminDisabled bool
}

type adminIngestHealth struct {
	LastJobAt  string
	LastJobAge string
	Failed24h  int
	Jobs24h    int
	Stale      bool
}

func (s *apiServer) adminHomePage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	data := adminHomeData{GeneratedAt: time.Now().UTC(), Version: version, Role: requestAdminIdentity(r).Role}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		totals, err := s.fetchStatusRows(ctx)
		data.Totals = totals
		if err != nil {
			data.DBError = err.Error()
		}
		ingest, err := s.fetchIngestHealth(ctx)
		data.Ingest = ingest
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
		jobs, err := s.fetchRecentIngestJobs(ctx)
		data.Jobs = jobs
		if err != nil && data.DBError == "" {
			data.DBError = err.Error()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminHomeTemplate.Execute(w, data); err != nil {
		log.Printf("render admin home failed: %v", err)
	}
}

func (s *apiServer) fetchIngestHealth(ctx context.Context) (adminIngestHealth, error) {
	var health adminIngestHealth
	var exists bool
	if err := s.db.QueryRowContext(ctx, `select to_regclass('public.ingest_jobs') is not null`).Scan(&exists); err != nil {
		return health, err
	}
	if !exists {
		health.LastJobAt = "ingest_jobs table missing"
		return health, nil
	}
	var ageSeconds int64
	err := s.db.QueryRowContext(ctx, `
		select
			coalesce(to_char(max(started_at) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), 'never'),
			coalesce(extract(epoch from now() - max(started_at))::bigint, -1),
			count(*) filter (where status = 'failed' and started_at > now() - interval '24 hours')::int,
			count(*) filter (where started_at > now() - interval '24 hours')::int
		from ingest_jobs
	`).Scan(&health.LastJobAt, &ageSeconds, &health.Failed24h, &health.Jobs24h)
	if err != nil {
		return health, err
	}
	health.LastJobAge = humanAge(ageSeconds)
	staleAfter := time.Duration(envInt("STALE_INGEST_MINUTES", 120)) * time.Minute
	health.Stale = ageSeconds >= 0 && time.Duration(ageSeconds)*time.Second > staleAfter
	return health, nil
}

func humanAge(seconds int64) string {
	if seconds < 0 {
		return "never"
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

var adminHomeTemplate = template.Must(template.New("adminHome").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Admin - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "overview" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
{{ if .Ingest.Stale }}<div class="error">Ingest looks stale: no ingest job since {{ .Ingest.LastJobAt }}. Check that spawnpoint-worker is running and the game server is writing logs.</div>{{ end }}
<div class="page-title"><strong>Admin Overview</strong><span class="muted">ingest health and management tools</span></div>

<div class="kpi-grid">
<div class="kpi-card"><span>Last Ingest</span><strong>{{ .Ingest.LastJobAge }}</strong></div>
<div class="kpi-card gold"><span>Jobs (24h)</span><strong>{{ .Ingest.Jobs24h }}</strong></div>
<div class="kpi-card {{ if gt .Ingest.Failed24h 0 }}red{{ else }}green{{ end }}"><span>Failed (24h)</span><strong>{{ .Ingest.Failed24h }}</strong></div>
</div>

<table><thead><tr><th>Area</th><th>Value</th><th>Detail</th></tr></thead><tbody>
{{ range .Totals }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td class="wrap">{{ .Detail }}</td></tr>
{{ else }}<tr><td colspan="3" class="muted">No status rows.</td></tr>{{ end }}
</tbody></table>

<h2>Recent Ingest Jobs</h2>
<table><thead><tr><th>Status</th><th>File</th><th>Detail</th></tr></thead><tbody>
{{ range .Jobs }}<tr><td>{{ .Label }}</td><td>{{ .Value }}</td><td class="wrap">{{ .Detail }}</td></tr>
{{ else }}<tr><td colspan="3" class="muted">No ingest jobs.</td></tr>{{ end }}
</tbody></table>

<div class="stat-note"><span>Spawnpoint {{ .Version }} &middot; generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }} &middot; last job {{ .Ingest.LastJobAt }}</span></div>
</main>{{ siteFooter }}</body></html>`))
