package main

import (
	"html/template"
	"log"
	"net/http"
)

func (s *apiServer) aboutPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := aboutTemplate.Execute(w, nil); err != nil {
		log.Printf("render about page failed: %v", err)
	}
}

var aboutTemplate = template.Must(template.New("about").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>About - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
<div class="page-title"><strong>About</strong></div>
<section class="panel" style="max-width:720px">
<p>This site tracks every round played on our Battlefield 1942 server. A worker reads the event logs the game server writes, loads them into a database, and the pages here are built from that: rounds, player profiles, leaderboards, awards, and live server status.</p>
<p>Players are matched by their BF1942 keyhash, so stats stick with you if you change your nickname. Team 1 is shown as Axis and team 2 as Allies.</p>
<p>The code is open source if you want to run it for your own server: <a class="link" href="https://github.com/hootmeow/spawnpoint">github.com/hootmeow/spawnpoint</a></p>
<p>This is an unofficial fan project, not affiliated with or endorsed by EA or DICE. See <a class="link" href="/privacy">Privacy</a> for what's collected and how to request changes.</p>
</section>
</main>{{ siteFooter }}</body></html>`))
