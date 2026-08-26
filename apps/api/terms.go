package main

import (
	"html/template"
	"log"
	"net/http"
)

func (s *apiServer) termsPage(w http.ResponseWriter, r *http.Request) {
	text, href := privacyContact()
	data := privacyPageData{ContactText: text, ContactHref: href}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := termsTemplate.Execute(w, data); err != nil {
		log.Printf("render terms page failed: %v", err)
	}
}

var termsTemplate = template.Must(template.New("terms").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Terms - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
<div class="page-title"><strong>Terms</strong></div>
<section class="panel" style="max-width:720px">
<p>This site is a free, self-hosted stats tracker for a Battlefield 1942 dedicated server. Using it — browsing pages, pulling the JSON API, downloading a profile export — means you accept these terms.</p>
<p><strong>Acceptable use:</strong> don't scrape or hammer the site outside normal browsing (the JSON API exists for programmatic access; use it instead), don't attempt to bypass admin authentication or rate limits, and don't use anything shown here to harass another player.</p>
<p><strong>Accuracy:</strong> stats are parsed automatically from the game server's own logs and shown as-is. They can be wrong — a parser bug, a corrupted log, a naming collision between two players — and nothing here is a guarantee of accuracy or fitness for any particular purpose.</p>
<p><strong>No warranty:</strong> this site is provided "as is", with no uptime guarantee and no warranty of any kind. It can change, be reset, or go offline without notice.</p>
<p><strong>Changes:</strong> these terms can change as the site changes; the current version is always the one on this page.</p>
<p>Questions{{ if .ContactText }}, contact <a class="link" href="{{ .ContactHref }}">{{ .ContactText }}</a>{{ else }}? Contact whoever administers this server{{ end }}. See also <a class="link" href="/privacy">Privacy</a>.</p>
<p>This is an unofficial fan project and not affiliated with or endorsed by EA or DICE.</p>
</section>
</main>{{ siteFooter }}</body></html>`))
