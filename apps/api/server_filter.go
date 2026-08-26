package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Server scoping for the stats pages. Every game row carries a server_id, so
// pages that aggregate across games accept ?server=<uuid> and add a pill row
// when more than one server has data. An empty filter means "all servers".

type serverOption struct {
	ID   string
	Name string
}

func (s *apiServer) fetchServerOptions(ctx context.Context) ([]serverOption, error) {
	rows, err := s.db.QueryContext(ctx, `
		select s.id::text, s.name
		from servers s
		where exists (select 1 from games g where g.server_id = s.id)
		order by s.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []serverOption
	for rows.Next() {
		var option serverOption
		if err := rows.Scan(&option.ID, &option.Name); err != nil {
			return nil, err
		}
		result = append(result, option)
	}
	return result, rows.Err()
}

// normalizeServerFilter returns the requested server id when it matches a
// known server, otherwise "" (all servers).
func normalizeServerFilter(requested string, options []serverOption) string {
	requested = strings.TrimSpace(requested)
	for _, option := range options {
		if option.ID == requested {
			return requested
		}
	}
	return ""
}

// serverFilterLinks builds the pill row; nil when only one server has data.
func serverFilterLinks(options []serverOption, active string, link func(key, value string) string) []filterLink {
	if len(options) < 2 {
		return nil
	}
	links := []filterLink{{"All Servers", link("server", ""), active == ""}}
	for _, option := range options {
		links = append(links, filterLink{option.Name, link("server", option.ID), option.ID == active})
	}
	return links
}

func searchServerLink(path string, query string, limit int) func(string, string) string {
	return func(_, value string) string {
		values := url.Values{}
		if query != "" {
			values.Set("q", query)
		}
		if limit != 150 {
			values.Set("limit", strconv.Itoa(limit))
		}
		if value != "" {
			values.Set("server", value)
		}
		if enc := values.Encode(); enc != "" {
			return path + "?" + enc
		}
		return path
	}
}

// appendQueryParam re-encodes a URL with one extra query parameter; used by
// pages whose links are built outside the shared link() closure.
func appendQueryParam(rawURL string, key string, value string) string {
	if value == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	values := parsed.Query()
	values.Set(key, value)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}
