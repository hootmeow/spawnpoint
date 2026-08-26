package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

type ingestJobRow struct {
	StartedAt  string
	Status     string
	Name       string
	SizeKB     int64
	Rounds     int
	Events     int
	DurationMS int64
	Error      string
}

type adminIngestData struct {
	GeneratedAt   time.Time
	DBError       string
	Role          string
	StatusFilter  string
	StatusFilters []filterLink
	Jobs          []ingestJobRow
}

var ingestStatuses = []string{"all", "parsed", "failed", "skipped", "started"}

// adminIngestPage is the ingest monitor: recent jobs with sizes, durations,
// and full failure messages, filterable by status.
func (s *apiServer) adminIngestPage(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	valid := false
	for _, s := range ingestStatuses {
		if s == status {
			valid = true
		}
	}
	if !valid {
		status = "all"
	}

	data := adminIngestData{GeneratedAt: time.Now().UTC(), StatusFilter: status, Role: requestAdminIdentity(r).Role}
	for _, s := range ingestStatuses {
		u := "/admin/ingest"
		if s != "all" {
			u += "?status=" + s
		}
		data.StatusFilters = append(data.StatusFilters, filterLink{Label: strings.Title(s), URL: u, Active: s == status})
	}

	jobs, err := s.fetchIngestJobs(ctx, status, 100)
	if err != nil {
		data.DBError = err.Error()
	}
	data.Jobs = jobs

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminIngestTemplate.Execute(w, data); err != nil {
		log.Printf("render admin ingest failed: %v", err)
	}
}

func (s *apiServer) fetchIngestJobs(ctx context.Context, status string, limit int) ([]ingestJobRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			to_char(started_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS'),
			status,
			original_name,
			coalesce(file_size_bytes, 0) / 1024,
			rounds,
			events,
			coalesce((extract(epoch from finished_at - started_at) * 1000)::bigint, 0),
			coalesce(error_message, '')
		from ingest_jobs
		where $1 = 'all' or status = $1
		order by started_at desc
		limit $2
	`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ingestJobRow
	for rows.Next() {
		var row ingestJobRow
		if err := rows.Scan(&row.StartedAt, &row.Status, &row.Name, &row.SizeKB, &row.Rounds, &row.Events, &row.DurationMS, &row.Error); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

var adminIngestTemplate = template.Must(template.New("adminIngest").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Ingest Monitor - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
{{ adminNav "ingest" .Role }}
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Ingest Monitor</strong><span class="muted">recent log ingestion jobs with sizes, durations, and failures</span></div>
<div class="filters">{{ range .StatusFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>

<table><thead><tr><th>Started (UTC)</th><th>Status</th><th>File</th><th>Size KB</th><th>Rounds</th><th>Events</th><th>ms</th><th>Error</th></tr></thead><tbody>
{{ range .Jobs }}<tr>
<td class="mono">{{ .StartedAt }}</td>
<td>{{ if eq .Status "failed" }}<span class="red">{{ .Status }}</span>{{ else }}{{ .Status }}{{ end }}</td>
<td class="wrap">{{ .Name }}</td>
<td>{{ .SizeKB }}</td>
<td>{{ .Rounds }}</td>
<td>{{ .Events }}</td>
<td>{{ .DurationMS }}</td>
<td class="wrap">{{ .Error }}</td></tr>
{{ else }}<tr><td colspan="8" class="muted">No ingest jobs.</td></tr>{{ end }}
</tbody></table>
<div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }} &middot; latest 100 jobs</span></div>
</main>{{ siteFooter }}</body></html>`))
