package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
)

type privacyPageData struct {
	ContactText string
	ContactHref string
}

// privacyContact reads PRIVACY_CONTACT from the environment. It can be a
// bare email address (rendered as a mailto: link) or a full URL (a contact
// form, a Discord invite, etc). Left unset, the page falls back to generic
// wording so the site still says *something* true without requiring every
// self-hoster to configure it.
func privacyContact() (text string, href string) {
	contact := os.Getenv("PRIVACY_CONTACT")
	if contact == "" {
		return "", ""
	}
	if strings.Contains(contact, "@") && !strings.Contains(contact, "://") {
		return contact, "mailto:" + contact
	}
	return contact, contact
}

func (s *apiServer) privacyPage(w http.ResponseWriter, r *http.Request) {
	text, href := privacyContact()
	data := privacyPageData{ContactText: text, ContactHref: href}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := privacyTemplate.Execute(w, data); err != nil {
		log.Printf("render privacy page failed: %v", err)
	}
}

var privacyTemplate = template.Must(template.New("privacy").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Privacy - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "" }}<main>
<div class="page-title"><strong>Privacy</strong></div>
<section class="panel" style="max-width:720px">
<p>This site is built from the Battlefield 1942 dedicated server's own event logs, not from any account you create here — there's no signup, and nothing is collected outside of what the game server already logs during a round.</p>
<p><strong>What's shown:</strong> your in-game nickname(s), a stable player identity derived from your BF1942 CD-key hash (so stats follow you across name changes without exposing the key itself), and match stats parsed from the logs &mdash; kills, deaths, rounds, weapons, vehicles, and awards.</p>
<p><strong>Chat:</strong> in-game chat is logged by the game server like any other event, and shown as-is on the chat page. Nothing said in-game is private; treat it the same as any other public voice/text chat.</p>
<p><strong>Retention:</strong> match history is kept indefinitely as part of the server's stats, the same way a scoreboard would be.</p>
<p><strong>Access:</strong> every profile page has a "Download this profile's stats" link (JSON or CSV) so you can get a copy of what's shown about you without asking an admin.</p>
<p><strong>Requesting changes:</strong> if you want your profile hidden from public pages{{ if .ContactText }}, contact <a class="link" href="{{ .ContactHref }}">{{ .ContactText }}</a>{{ else }}, contact whoever administers this server{{ end }}. Round and match history involving other players is kept intact even when a profile is hidden, the same way removing a player from a scoreboard doesn't rewrite the games they played in.</p>
<p>This is an unofficial fan project and not affiliated with or endorsed by EA or DICE.</p>
</section>
</main>{{ siteFooter }}</body></html>`))
