package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// sitemapPage lists static routes plus a bounded set of per-entity detail
// pages (maps/weapons/vehicles/kits/clans read from the same materialized
// views the list pages use, so this costs nothing extra; players are capped
// at the top 500 by rounds played to keep the file a reasonable size on a
// long-running install). /admin, /api, /search, and /chat are excluded, same
// as robots.txt.
// siteBaseURL prefers the operator-configured PUBLIC_BASE_URL (needed
// anyway for og:image) and falls back to deriving one from the request, so
// /sitemap.xml and the Sitemap: line in /robots.txt work out of the box even
// on installs that haven't set it.
func siteBaseURL(r *http.Request) string {
	if base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}

func sitemapURL(r *http.Request) string {
	return siteBaseURL(r) + "/sitemap.xml"
}

func (s *apiServer) sitemapPage(w http.ResponseWriter, r *http.Request) {
	base := siteBaseURL(r)

	urls := []string{
		"/", "/players", "/leaderboard", "/awards", "/streaks", "/rounds", "/maps", "/trends",
		"/weapons", "/vehicles", "/kits", "/clans", "/compare", "/rivalry", "/live",
		"/about", "/privacy", "/terms", "/support",
	}

	if s.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if codes, err := s.sitemapCodes(ctx, "select map_name from map_totals_mv order by rounds desc limit 500"); err == nil {
			for _, code := range codes {
				urls = append(urls, "/map?name="+url.QueryEscape(code))
			}
		}
		if codes, err := s.sitemapCodes(ctx, "select weapon_code from weapon_totals_mv order by kills desc limit 500"); err == nil {
			for _, code := range codes {
				urls = append(urls, "/weapon?code="+url.QueryEscape(code))
			}
		}
		if codes, err := s.sitemapCodes(ctx, "select vehicle_code from vehicle_activity_totals_mv order by kills desc limit 500"); err == nil {
			for _, code := range codes {
				urls = append(urls, "/vehicle?code="+url.QueryEscape(code))
			}
		}
		if codes, err := s.sitemapCodes(ctx, "select kit_code from kit_totals_mv order by pickups desc limit 500"); err == nil {
			for _, code := range codes {
				urls = append(urls, "/kit?code="+url.QueryEscape(code))
			}
		}
		if codes, err := s.sitemapCodes(ctx, "select distinct clan_tag from players where clan_tag is not null limit 500"); err == nil {
			for _, code := range codes {
				urls = append(urls, "/clan?tag="+url.QueryEscape(code))
			}
		}
		if codes, err := s.sitemapCodes(ctx, `
			select p.id::text from players p
			join round_player_stats s on s.player_id = p.id
			where not p.hidden
			group by p.id
			order by count(distinct s.round_id) desc
			limit 500
		`); err == nil {
			for _, id := range codes {
				urls = append(urls, "/player?id="+url.QueryEscape(id))
			}
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	_, _ = fmt.Fprint(w, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+"\n")
	for _, u := range urls {
		_, _ = fmt.Fprintf(w, "<url><loc>%s%s</loc></url>\n", base, xmlEscape(u))
	}
	_, _ = fmt.Fprint(w, `</urlset>`)
}

func (s *apiServer) sitemapCodes(ctx context.Context, query string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}
