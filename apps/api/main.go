package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/hootmeow/spawnpoint/internal/notify"
	"github.com/hootmeow/spawnpoint/internal/parser/bflog"
	"github.com/hootmeow/spawnpoint/internal/storage/postgres"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"; update.sh
// and setup.sh both pass it as `git describe --tags`. Defaults to "dev" for
// `go run`/unversioned builds.
var version = "dev"

type apiServer struct {
	db                *sql.DB
	store             *postgres.Store
	webhook           *notify.Webhook
	roundNotifyWindow time.Duration
}

type siteTotals struct {
	Rounds  int
	Players int
	Kills   int
	Events  int
	Maps    int
}

type dashboardData struct {
	GeneratedAt    time.Time
	Totals         siteTotals
	ServerFilter   string
	ServerFilters  []filterLink
	LeaderboardURL string
	RoundsURL      string
	MapsURL        string
	PlayersURL     string
	WeaponsURL     string
	VehiclesURL    string
	Rounds         []roundRow
	TopPlayers     []leaderRow
	CombatTrend    template.HTML
	PlayerMetrics  template.HTML
	WeaponBars     template.HTML
	VehicleBars    template.HTML
	DBError        string
}

type roundRow struct {
	ID            string `json:"id"`
	RoundHash     string `json:"round_hash"`
	MapName       string `json:"map_name"`
	GameMode      string `json:"game_mode"`
	RoundIndex    int    `json:"round_index"`
	EventCount    int    `json:"event_count"`
	PlayerCount   int    `json:"player_count"`
	Kills         int    `json:"kills"`
	WinningTeamID *int   `json:"winning_team_id,omitempty"`
	VictoryType   *int   `json:"victory_type,omitempty"`
	Duration      int    `json:"duration_seconds"`
	EndTickets    string `json:"end_tickets"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at"`
}

type playerRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	MapName     string `json:"map_name"`
	Score       int    `json:"score"`
	Kills       int    `json:"kills"`
	Deaths      int    `json:"deaths"`
	Teamkills   int    `json:"teamkills"`
	Heals       int    `json:"heals"`
	Repairs     int    `json:"repairs"`
	Source      string `json:"source"`
	IsBot       *bool  `json:"is_bot,omitempty"`
}

type eventCountRow struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type playerSummaryRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	IsBot       *bool  `json:"is_bot,omitempty"`
	Rounds      int    `json:"rounds"`
	Score       int    `json:"score"`
	Kills       int    `json:"kills"`
	Deaths      int    `json:"deaths"`
	Teamkills   int    `json:"teamkills"`
	Heals       int    `json:"heals"`
	Repairs     int    `json:"repairs"`
	LastSeen    string `json:"last_seen"`
}

type weaponStatRow struct {
	WeaponCode string `json:"weapon_code"`
	Uses       int    `json:"uses"`
	Kills      int    `json:"kills"`
	Teamkills  int    `json:"teamkills"`
	Deaths     int    `json:"deaths"`
	Users      int    `json:"users"`
}

type vehicleStatRow struct {
	VehicleCode string `json:"vehicle_code"`
	Entries     int    `json:"entries"`
	Kills       int    `json:"kills"`
	Teamkills   int    `json:"teamkills"`
	Users       int    `json:"users"`
	Rounds      int    `json:"rounds"`
	Destroyed   int    `json:"destroyed"`
	Destroyers  int    `json:"destroyers"`
}

type weaponUserRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	IsBot       *bool  `json:"is_bot,omitempty"`
	Uses        int    `json:"uses"`
	Kills       int    `json:"kills"`
	Teamkills   int    `json:"teamkills"`
	LastUsed    string `json:"last_used"`
}

type playerEventRow struct {
	When         string `json:"when"`
	MapName      string `json:"map_name"`
	RoundIndex   int    `json:"round_index"`
	EventType    string `json:"event_type"`
	RawEventName string `json:"raw_event_name"`
	ActorID      string `json:"actor_id"`
	ActorName    string `json:"actor_name"`
	ActorKind    string `json:"actor_kind"`
	TargetID     string `json:"target_id"`
	TargetName   string `json:"target_name"`
	TargetKind   string `json:"target_kind"`
	WeaponCode   string `json:"weapon_code"`
	VehicleCode  string `json:"vehicle_code"`
	KitCode      string `json:"kit_code"`
	ChatText     string `json:"chat_text"`
	Params       string `json:"params"`
}

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	createAdmin := flag.Bool("create-admin", false, "create or update an admin account from ADMIN_CREATE_USERNAME/ADMIN_CREATE_PASSWORD, then exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *createAdmin {
		runCreateAdmin()
		return
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	bflog.MaxEvents = envInt("MAX_LOG_EVENTS", bflog.MaxEvents)

	host := os.Getenv("HOST")
	if t := adminToken(); t != "" {
		if len(t) < 16 {
			log.Printf("SECURITY WARNING: ADMIN_TOKEN is only %d characters; use at least 24 random characters. It is a full-access break-glass credential — a short token is guessable.", len(t))
		}
		if !envBool("SECURE_COOKIES") {
			if host == "" {
				// Unset HOST binds every interface, which — per this
				// project's own deployment paths (bare setup.sh,
				// docker-compose prod's default port publish) — usually
				// means this process is directly reachable with no reverse
				// proxy in front to terminate TLS. Can't safely refuse to
				// start over this alone: setup.sh's own "going live" step
				// intentionally ships HOST/SECURE_COOKIES commented out
				// until the operator completes it, so a fresh install would
				// never boot. Warn loudly instead.
				log.Printf("SECURITY WARNING: ADMIN_TOKEN is set, SECURE_COOKIES is not enabled, and HOST is unset (binding all interfaces) — this looks like a direct-to-internet deployment with no reverse proxy, meaning the admin token/session cookie are transiting in cleartext RIGHT NOW. Before going live: set HOST=127.0.0.1 behind a TLS-terminating reverse proxy (see docs/nginx-cloudflare-reverse-proxy.md) and set SECURE_COOKIES=1.")
			} else {
				log.Printf("SECURITY WARNING: ADMIN_TOKEN is set but SECURE_COOKIES is not enabled. If this instance is reachable over plain HTTP, the admin token and session cookie transit in cleartext. Terminate TLS (see docs/nginx-cloudflare-reverse-proxy.md) and set SECURE_COOKIES=1.")
			}
		}
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required (see docs/production.md); refusing to start and silently serve a database-less site")
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		log.Fatalf("database open failed: %v", err)
	}
	db.SetMaxOpenConns(envInt("DB_MAX_OPEN_CONNS", 16))
	db.SetMaxIdleConns(envInt("DB_MAX_IDLE_CONNS", 8))
	db.SetConnMaxLifetime(time.Duration(envInt("DB_CONN_MAX_LIFETIME_SECONDS", 900)) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(envInt("DB_CONN_MAX_IDLE_SECONDS", 300)) * time.Second)

	server := &apiServer{db: db, webhook: notify.NewWebhook(os.Getenv("DISCORD_WEBHOOK_URL")), roundNotifyWindow: envDuration("ROUND_NOTIFY_WINDOW", 20*time.Minute)}
	store, err := postgres.Open(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("ingest store open failed: %v", err)
	}
	server.store = store
	mux := http.NewServeMux()
	mux.HandleFunc("/", server.dashboard)
	mux.HandleFunc("/assets/site.css", siteCSS)
	mux.HandleFunc("/assets/og-image.png", ogImageHandler)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(faviconSVG))
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /admin\nDisallow: /api/\nDisallow: /search\nDisallow: /chat\nSitemap: " + sitemapURL(r) + "\n"))
	})
	mux.HandleFunc("/sitemap.xml", server.sitemapPage)
	mux.HandleFunc("/live", server.livePage)
	mux.HandleFunc("/players", server.playersPage)
	mux.HandleFunc("/search", server.searchPage)
	mux.HandleFunc("/leaderboard", server.leaderboardPage)
	mux.HandleFunc("/awards", server.awardsPage)
	mux.HandleFunc("/streaks", server.streaksPage)
	mux.HandleFunc("/chat", server.chatPage)
	mux.HandleFunc("/trends", server.trendsPage)
	mux.HandleFunc("/rounds", server.roundsPage)
	mux.HandleFunc("/weapons", server.weaponsPage)
	mux.HandleFunc("/vehicles", server.vehiclesPage)
	mux.HandleFunc("/player", server.playerPage)
	mux.HandleFunc("/player/export", server.playerExportPage)
	mux.HandleFunc("/export/rounds", server.bulkExportPage)
	mux.HandleFunc("/weapon", server.weaponPage)
	mux.HandleFunc("/vehicle", server.vehiclePage)
	mux.HandleFunc("/round", server.roundPage)
	mux.HandleFunc("/maps", server.mapsPage)
	mux.HandleFunc("/map", server.mapPage)
	mux.HandleFunc("/rivalry", server.rivalryPage)
	mux.HandleFunc("/kits", server.kitsPage)
	mux.HandleFunc("/kit", server.kitPage)
	mux.HandleFunc("/clans", server.clansPage)
	mux.HandleFunc("/clan", server.clanPage)
	mux.HandleFunc("/support", server.supportPage)
	mux.HandleFunc("/insights", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/trends", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/compare", server.comparePage)
	mux.HandleFunc("/about", server.aboutPage)
	mux.HandleFunc("/privacy", server.privacyPage)
	mux.HandleFunc("/terms", server.termsPage)
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/status", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/admin", server.requireRole(roleModerator, server.adminHomePage))
	mux.HandleFunc("/admin/status", server.requireRole(roleModerator, server.statusPage))
	mux.HandleFunc("/admin/login", server.adminLoginPage)
	mux.HandleFunc("/admin/logout", server.adminLogout)
	mux.HandleFunc("/admin/identity", server.requireRole(roleModerator, server.adminIdentityPage))
	mux.HandleFunc("/admin/servers", server.requireRole(roleAdmin, server.adminServersPage))
	mux.HandleFunc("/admin/players", server.requireRole(roleModerator, server.adminPlayersPage))
	mux.HandleFunc("/admin/ingest", server.requireRole(roleModerator, server.adminIngestPage))
	mux.HandleFunc("/admin/users", server.requireRole(roleAdmin, server.adminUsersPage))
	mux.HandleFunc("/admin/audit", server.requireRole(roleAdmin, server.adminAuditPage))
	mux.HandleFunc("/admin/settings", server.requireRole(roleAdmin, server.adminSettingsPage))
	mux.HandleFunc("/api/rounds", server.roundsJSON)
	mux.HandleFunc("/api/players", server.playersJSON)
	mux.HandleFunc("/api/player-search", server.playerSearchJSON)
	mux.HandleFunc("/api/player-activity", server.playerActivityJSON)
	mux.HandleFunc("/api/player-events", server.playerEventsJSON)
	mux.HandleFunc("/api/weapons", server.weaponsJSON)
	mux.HandleFunc("/api/vehicles-destroyed", server.vehiclesDestroyedJSON)
	mux.HandleFunc("/api/weapon-users", server.weaponUsersJSON)
	mux.HandleFunc("/api/weapon-events", server.weaponEventsJSON)
	mux.HandleFunc("/api/events", server.eventsJSON)
	mux.HandleFunc("/api/ingest/upload", server.ingestUpload)
	mux.HandleFunc("/healthz", server.healthz)

	// HOST is unset by default (bind all interfaces) so the common
	// setup.sh/go-run path keeps working unchanged. Set it to 127.0.0.1 when
	// running behind a reverse proxy on the same host, so the app is only
	// reachable through the proxy — see docs/nginx-cloudflare-reverse-proxy.md.
	addr := host + ":" + port
	log.Printf("spawnpoint api listening on %s", addr)
	limiter := newRateLimiter(envInt("RATE_LIMIT_RPS", 20))
	cache := newPageCache(time.Duration(envInt("PAGE_CACHE_SECONDS", 60)) * time.Second)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           recoverMiddleware(productionMiddleware(rateLimitMiddleware(limiter, gzipMiddleware(pageCacheMiddleware(cache, mux))))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	case <-ctx.Done():
		stop()
		log.Printf("shutting down: draining in-flight requests (up to 15s)")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown timed out, forcing close: %v", err)
			_ = httpServer.Close()
		}
	}
}

// recoverMiddleware turns a handler panic into a clean 500 with a structured
// log line instead of an abruptly closed connection and an unstructured
// "http: panic serving ..." line from net/http's own per-connection recovery.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving path=%s remote=%s: %v\n%s", r.URL.Path, remoteIP(r), rec, debug.Stack())
				http.Error(w, "something went wrong; please try again shortly", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func productionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "private, max-age=15")
		// The site is 100% server-rendered with zero client-side JS and no
		// external resources (see README), so this can be strict: no script
		// of any kind, no framing, everything same-origin. style-src needs
		// 'unsafe-inline' because every page ships CSS in an inline <style>
		// block rather than a linked stylesheet.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), payment=(), usb=()")
		if requestIsHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Log the path only: query strings can carry search terms and must
		// never carry credentials, so keep them out of the journal entirely.
		log.Printf("request method=%s path=%s status=%d duration_ms=%d remote=%s", r.Method, r.URL.Path, rec.status, time.Since(start).Milliseconds(), remoteIP(r))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// remoteIP resolves the client's address for rate limiting, login
// throttling, and request logs. CF-Connecting-IP and X-Forwarded-For are
// ordinary request headers — anyone can set either to anything, including a
// fresh value on every single request, which would otherwise make the
// per-IP rate limiter and the admin login throttle completely ineffective.
//
// The deciding factor is the real TCP peer (r.RemoteAddr), which nothing a
// client sends can fake: these headers are ONLY trusted when that peer is
// loopback or a private address, i.e. your own reverse proxy running on
// this host or on the same private network — never a direct request from
// the open internet. This matters concretely for this project's own
// deployment paths: docker-compose.prod.yml publishes the api container's
// port straight to the host with no proxy in front by default, and a plain
// setup.sh/go-run install with no HOST set binds every interface — in both
// of those (probably the two most common setups), a request's peer address
// is the real client, and these headers must be ignored, not trusted.
//
// Once there IS a trusted proxy in front (see
// docs/nginx-cloudflare-reverse-proxy.md — HOST=127.0.0.1, or an nginx
// sidecar container on the same Docker network), CF-Connecting-IP is
// trusted whole (Cloudflare overwrites it for every request through their
// edge), and X-Forwarded-For's LAST entry is trusted — it's a
// comma-separated hop chain proxies *append* to (RFC 7239 style), so the
// last entry is the one your own proxy added from the real peer it saw, not
// the first, which is whatever the original client sent and is therefore
// just as attacker-controlled as the direct-connection case above.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !trustedProxyPeer(host) {
		return host
	}
	if cfIP := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cfIP != "" {
		return cfIP
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	return host
}

func trustedProxyPeer(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// dbErrorMessage logs the real error server-side and returns a generic
// message for public pages. Raw Postgres errors can echo table/column names
// and query fragments — fine on /admin (an authenticated operator reading
// their own install's diagnostics) but not something to hand an anonymous
// visitor on a site that bots will scrape by default.
func dbErrorMessage(err error) string {
	log.Printf("db error: %v", err)
	return "something went wrong loading this page; please try again shortly"
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func listLimit(r *http.Request, fallback int) int {
	limit := fallback
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value != "" {
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
			limit = parsed
		}
	}
	if limit < 25 {
		return 25
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func cleanDateParam(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return ""
	}
	return value
}

// healthz is a readiness probe: 200 while the database answers and ingest is
// fresh, 503 with detail otherwise. Staleness only applies once the database
// has ingest history, so an empty install still reports healthy.
func (s *apiServer) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{"status": "ok", "version": version}
	code := http.StatusOK

	if s.db == nil {
		response["status"] = "degraded"
		response["database"] = "not configured"
		code = http.StatusServiceUnavailable
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		var ageSeconds int64
		err := s.db.QueryRowContext(ctx, `
			select coalesce(extract(epoch from now() - max(started_at))::bigint, -1)
			from ingest_jobs
		`).Scan(&ageSeconds)
		if err != nil {
			response["status"] = "degraded"
			response["database"] = dbErrorMessage(err)
			code = http.StatusServiceUnavailable
		} else {
			response["database"] = "ok"
			staleAfter := time.Duration(envInt("STALE_INGEST_MINUTES", 120)) * time.Minute
			if ageSeconds >= 0 {
				response["last_ingest_age_seconds"] = ageSeconds
				if time.Duration(ageSeconds)*time.Second > staleAfter {
					response["status"] = "degraded"
					response["ingest"] = "stale"
					code = http.StatusServiceUnavailable
				}
			}
		}
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response)
}

func siteCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(sharedCSS))
}

func (s *apiServer) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := dashboardData{
		GeneratedAt:    time.Now().UTC(),
		LeaderboardURL: "/leaderboard",
		RoundsURL:      "/rounds",
		MapsURL:        "/maps",
		PlayersURL:     "/players",
		WeaponsURL:     "/weapons",
		VehiclesURL:    "/vehicles",
	}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		setErr := func(err error) {
			if err != nil && data.DBError == "" {
				data.DBError = dbErrorMessage(err)
			}
		}

		var err error
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := func(_, value string) string {
			if value == "" {
				return "/"
			}
			return "/?server=" + queryEscape(value)
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		if serverFilter != "" {
			data.LeaderboardURL = appendQueryParam(data.LeaderboardURL, "server", serverFilter)
			data.RoundsURL = appendQueryParam(data.RoundsURL, "server", serverFilter)
			data.MapsURL = appendQueryParam(data.MapsURL, "server", serverFilter)
			data.PlayersURL = appendQueryParam(data.PlayersURL, "server", serverFilter)
			data.WeaponsURL = appendQueryParam(data.WeaponsURL, "server", serverFilter)
			data.VehiclesURL = appendQueryParam(data.VehiclesURL, "server", serverFilter)
		}

		data.Totals, err = s.fetchTotals(ctx, serverFilter)
		setErr(err)
		data.CombatTrend, err = s.fetchDashboardCombatTrend(ctx, 10, serverFilter)
		setErr(err)
		data.PlayerMetrics, err = s.fetchDashboardPlayerMetrics(ctx, serverFilter)
		setErr(err)
		data.WeaponBars, err = s.fetchKillBarsFromMV(ctx, dashboardWeaponKillsFromMVQuery, dashboardWeaponKillsQuery, 8, colorKills, serverFilter)
		setErr(err)
		data.VehicleBars, err = s.fetchKillBarsFromMV(ctx, dashboardVehicleKillsFromMVQuery, dashboardVehicleKillsQuery, 8, colorScore, serverFilter)
		setErr(err)
		data.TopPlayers, err = s.fetchLeaderboard(ctx, "kills", "", "all", 1, "all", serverFilter, 10)
		setErr(err)
		rounds, err := s.fetchRoundsFiltered(ctx, 40, "", "", "", serverFilter)
		setErr(err)
		// The server cycles empty maps every couple of minutes; only rounds
		// somebody actually played belong on the overview.
		for _, round := range rounds {
			if round.PlayerCount > 0 {
				data.Rounds = append(data.Rounds, round)
			}
			if len(data.Rounds) == 8 {
				break
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		log.Printf("render dashboard failed: %v", err)
	}
}

func (s *apiServer) playersPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	botFilter := normalizeBotFilter(r.URL.Query().Get("bots"))
	limit := listLimit(r, 150)
	data := struct {
		GeneratedAt   time.Time
		DBError       string
		Query         string
		BotFilter     string
		BotFilters    []filterLink
		ServerFilter  string
		ServerFilters []filterLink
		Limit         int
		Players       []playerSummaryRow
	}{GeneratedAt: time.Now().UTC(), Query: query, BotFilter: botFilter, Limit: limit}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := func(key, value string) string {
			v := url.Values{}
			set := func(k, val, def string) {
				if k == key {
					val = value
				}
				if val != def && val != "" {
					v.Set(k, val)
				}
			}
			set("q", query, "")
			set("bots", botFilter, "all")
			set("limit", strconv.Itoa(limit), "150")
			set("server", serverFilter, "")
			if enc := v.Encode(); enc != "" {
				return "/players?" + enc
			}
			return "/players"
		}
		for _, b := range []struct{ Key, Label string }{{"all", "All"}, {"humans", "Humans"}, {"bots", "Bots"}} {
			data.BotFilters = append(data.BotFilters, filterLink{b.Label, link("bots", b.Key), b.Key == botFilter})
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		rows, err := s.fetchPlayerSearch(ctx, query, botFilter, limit, serverFilter)
		data.Players = rows
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := playersTemplate.Execute(w, data); err != nil {
		log.Printf("render players page failed: %v", err)
	}
}

func (s *apiServer) roundsPage(w http.ResponseWriter, r *http.Request) {
	mapName := strings.TrimSpace(r.URL.Query().Get("map"))
	from := cleanDateParam(r.URL.Query().Get("from"))
	to := cleanDateParam(r.URL.Query().Get("to"))
	data := struct {
		GeneratedAt   time.Time
		DBError       string
		MapName       string
		From          string
		To            string
		ServerFilters []filterLink
		Rounds        []roundRow
		PageLinks     []filterLink
	}{GeneratedAt: time.Now().UTC(), MapName: mapName, From: from, To: to}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		link := func(_, value string) string {
			base := "/rounds"
			v := url.Values{}
			if mapName != "" {
				v.Set("map", mapName)
			}
			if from != "" {
				v.Set("from", from)
			}
			if to != "" {
				v.Set("to", to)
			}
			if value != "" {
				v.Set("server", value)
			}
			if enc := v.Encode(); enc != "" {
				return base + "?" + enc
			}
			return base
		}
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		// The archive view shows the last 100 rounds, 25 per page.
		rows, err := s.fetchRoundsFiltered(ctx, 100, mapName, from, to, serverFilter)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
		const perPage = 25
		pages := (len(rows) + perPage - 1) / perPage
		if pages < 1 {
			pages = 1
		}
		page := pageParam(r, "page")
		if page > pages {
			page = pages
		}
		start := (page - 1) * perPage
		end := start + perPage
		if end > len(rows) {
			end = len(rows)
		}
		data.Rounds = rows[start:end]
		if pages > 1 {
			pageLink := func(p int) string {
				v := url.Values{}
				if mapName != "" {
					v.Set("map", mapName)
				}
				if from != "" {
					v.Set("from", from)
				}
				if to != "" {
					v.Set("to", to)
				}
				if serverFilter != "" {
					v.Set("server", serverFilter)
				}
				if p > 1 {
					v.Set("page", strconv.Itoa(p))
				}
				if enc := v.Encode(); enc != "" {
					return "/rounds?" + enc
				}
				return "/rounds"
			}
			for p := 1; p <= pages; p++ {
				data.PageLinks = append(data.PageLinks, filterLink{strconv.Itoa(p), pageLink(p), p == page})
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := roundsTemplate.Execute(w, data); err != nil {
		log.Printf("render rounds page failed: %v", err)
	}
}

func (s *apiServer) weaponsPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := listLimit(r, 150)
	data := struct {
		GeneratedAt   time.Time
		DBError       string
		Query         string
		ServerFilter  string
		ServerFilters []filterLink
		Limit         int
		Weapons       []weaponStatRow
		KillBars      template.HTML
		EffBars       template.HTML
	}{GeneratedAt: time.Now().UTC(), Query: query, Limit: limit}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		// Site-wide, no map filter — a busy install can take a few seconds
		// even after indexing (see docs/performance.md), so this gets the
		// same wider margin as /map, /kits, /awards.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := searchServerLink("/weapons", query, limit)
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		rows, err := s.fetchWeaponStats(ctx, limit, "", query, serverFilter)
		data.Weapons = rows
		data.KillBars = weaponStatsBars(rows)
		data.EffBars = weaponEfficiencyBars(rows)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := weaponsTemplate.Execute(w, data); err != nil {
		log.Printf("render weapons page failed: %v", err)
	}
}

func (s *apiServer) vehiclesPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := listLimit(r, 150)
	data := struct {
		GeneratedAt   time.Time
		DBError       string
		Query         string
		ServerFilter  string
		ServerFilters []filterLink
		Limit         int
		Vehicles      []vehicleStatRow
		Activity      template.HTML
		EffBars       template.HTML
	}{GeneratedAt: time.Now().UTC(), Query: query, Limit: limit}
	if s.db == nil {
		data.DBError = "DATABASE_URL is not configured"
	} else {
		// vehicle_events aggregates every vehicle-related event site-wide
		// (no map filter); still a multi-second query on a busy install even
		// after the vehicle_code_list_mv fix, so this gets the same wider
		// margin as /map, /kits, /awards, /weapons.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		serverOptions, _ := s.fetchServerOptions(ctx)
		serverFilter := normalizeServerFilter(r.URL.Query().Get("server"), serverOptions)
		data.ServerFilter = serverFilter
		link := searchServerLink("/vehicles", query, limit)
		data.ServerFilters = serverFilterLinks(serverOptions, serverFilter, link)
		rows, err := s.fetchVehicleStats(ctx, limit, "", query, serverFilter)
		data.Vehicles = rows
		data.Activity = vehicleStatsBars(rows)
		data.EffBars = vehicleEfficiencyBars(rows)
		if err != nil {
			data.DBError = dbErrorMessage(err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := vehiclesTemplate.Execute(w, data); err != nil {
		log.Printf("render vehicles page failed: %v", err)
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// sanitizeUUID returns s when it is a well-formed UUID, otherwise "" so that
// nullif($n,”)::uuid casts in queries never see an invalid value.
func sanitizeUUID(s string) string {
	if uuidPattern.MatchString(s) {
		return s
	}
	return ""
}

func (s *apiServer) roundsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchRounds(ctx, 50)
	writeJSON(w, rows, err)
}

func (s *apiServer) playersJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchPlayers(ctx, 100, normalizeBotFilter(r.URL.Query().Get("bots")))
	writeJSON(w, rows, err)
}

func (s *apiServer) eventsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchEventCounts(ctx)
	writeJSON(w, rows, err)
}

func (s *apiServer) playerSearchJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchPlayerSearch(ctx, strings.TrimSpace(r.URL.Query().Get("q")), normalizeBotFilter(r.URL.Query().Get("bots")), 100, "")
	writeJSON(w, rows, err)
}

func (s *apiServer) playerActivityJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchPlayerActivity(ctx, sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("player_id"))), strings.TrimSpace(r.URL.Query().Get("q")), 100)
	writeJSON(w, rows, err)
}

func (s *apiServer) playerEventsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchPlayerEvents(ctx, 200, sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("player_id"))), strings.TrimSpace(r.URL.Query().Get("q")), strings.TrimSpace(r.URL.Query().Get("event_type")), strings.TrimSpace(r.URL.Query().Get("weapon")))
	writeJSON(w, rows, err)
}

func (s *apiServer) weaponsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchWeaponStats(ctx, 100, sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("player_id"))), strings.TrimSpace(r.URL.Query().Get("q")), "")
	writeJSON(w, rows, err)
}

func (s *apiServer) vehiclesDestroyedJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchVehicleStats(ctx, 100, sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("player_id"))), strings.TrimSpace(r.URL.Query().Get("q")), "")
	writeJSON(w, rows, err)
}

func (s *apiServer) weaponUsersJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchWeaponUsers(ctx, strings.TrimSpace(r.URL.Query().Get("weapon")), normalizeBotFilter(r.URL.Query().Get("bots")), sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("player_id"))), strings.TrimSpace(r.URL.Query().Get("q")), 100)
	writeJSON(w, rows, err)
}

func (s *apiServer) weaponEventsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.fetchWeaponEvents(ctx, strings.TrimSpace(r.URL.Query().Get("weapon")), sanitizeUUID(strings.TrimSpace(r.URL.Query().Get("player_id"))), strings.TrimSpace(r.URL.Query().Get("q")), 200)
	writeJSON(w, rows, err)
}

func writeJSON(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": dbErrorMessage(err)})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func (s *apiServer) fetchTotals(ctx context.Context, serverFilter string) (siteTotals, error) {
	var totals siteTotals
	if s.db == nil {
		return totals, sql.ErrConnDone
	}
	if serverFilter == "" {
		// Precomputed by migration 0007 (refreshed after every ingest); the
		// live version below counts across rounds/round_player_stats/events
		// with no filter at all, which on a multi-million-row events table
		// took the better part of a second on every homepage load.
		err := s.db.QueryRowContext(ctx, `select rounds, players, kills, events, maps from site_totals_mv`).
			Scan(&totals.Rounds, &totals.Players, &totals.Kills, &totals.Events, &totals.Maps)
		if err == nil {
			return totals, nil
		}
		if !strings.Contains(err.Error(), "does not exist") {
			return totals, err
		}
		// Older install without migration 0007 applied yet — fall through to the live query.
	}
	err := s.db.QueryRowContext(ctx, `
		select
			(select count(*) from rounds r join games g on g.id = r.game_id where ($1 = '' or g.server_id = nullif($1,'')::uuid))::int,
			(case when $1 = ''
				then (select count(*) from players)::int
				else (select count(distinct s.player_id) from round_player_stats s join rounds r on r.id = s.round_id join games g on g.id = r.game_id where g.server_id = nullif($1,'')::uuid)::int
			end),
			(select coalesce(sum(s.kills), 0) from round_player_stats s join rounds r on r.id = s.round_id join games g on g.id = r.game_id where ($1 = '' or g.server_id = nullif($1,'')::uuid))::int,
			(select count(*) from events e where ($1 = '' or e.server_id = nullif($1,'')::uuid))::int,
			(select count(distinct map_name) from games where map_name is not null and map_name <> '' and ($1 = '' or server_id = nullif($1,'')::uuid))::int
	`, serverFilter).Scan(&totals.Rounds, &totals.Players, &totals.Kills, &totals.Events, &totals.Maps)
	return totals, err
}

func (s *apiServer) fetchRounds(ctx context.Context, limit int) ([]roundRow, error) {
	return s.fetchRoundsFiltered(ctx, limit, "", "", "", "")
}

func (s *apiServer) fetchRoundsFiltered(ctx context.Context, limit int, mapName string, fromDate string, toDate string, serverFilter string) ([]roundRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	// event_count/player_count/kills used to come from two CTEs that
	// group-by the entire events/round_player_stats tables (millions of
	// rows) before this query's LIMIT ever applies. Correlated subqueries
	// let Postgres run them only for the handful of rounds actually
	// returned, using the existing round_id-keyed indexes.
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(substr(encode(raw.content_sha256, 'hex'), 1, 8), ''),
			r.id::text,
			coalesce(g.map_name, ''),
			coalesce(g.game_mode, ''),
			r.round_index,
			(select count(*)::int from events e where e.round_id = r.id),
			(select count(distinct player_id)::int from round_player_stats s where s.round_id = r.id),
			(select coalesce(sum(kills), 0)::int from round_player_stats s where s.round_id = r.id),
			r.winning_team_id,
			r.victory_type,
			coalesce(extract(epoch from (r.ended_at - g.started_at)), 0)::int as duration_seconds,
			coalesce(r.end_tickets::text, '{}'),
			to_char(g.started_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'),
			coalesce(to_char(r.ended_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '')
		from raw_log_files raw
		join games g on g.raw_log_file_id = raw.id
		join rounds r on r.game_id = g.id
		where ($2 = '' or g.map_name = $2)
		and ($3 = '' or g.started_at::date >= $3::date)
		and ($4 = '' or g.started_at::date <= $4::date)
		and ($5 = '' or g.server_id = nullif($5,'')::uuid)
		order by raw.original_name desc
		limit $1
	`, limit, mapName, fromDate, toDate, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []roundRow
	for rows.Next() {
		var row roundRow
		if err := rows.Scan(&row.RoundHash, &row.ID, &row.MapName, &row.GameMode, &row.RoundIndex, &row.EventCount, &row.PlayerCount, &row.Kills, &row.WinningTeamID, &row.VictoryType, &row.Duration, &row.EndTickets, &row.StartedAt, &row.EndedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayers(ctx context.Context, limit int, botFilter string) ([]playerRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := s.db.QueryContext(ctx, `
		select
			p.id::text,
			p.display_name,
			coalesce(g.map_name, ''),
			s.is_bot,
			s.score,
			s.kills,
			s.deaths,
			s.teamkills,
			s.heals,
			s.repairs,
			s.source
		from round_player_stats s
		join players p on p.id = s.player_id and not p.hidden
		join rounds r on r.id = s.round_id
		join games g on g.id = r.game_id
		where (
			$2 = 'all'
			or ($2 = 'bots' and s.is_bot is true)
			or ($2 = 'humans' and coalesce(s.is_bot, false) is false)
		)
		order by s.kills desc, s.score desc, s.deaths asc, p.display_name asc
		limit $1
	`, limit, botFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []playerRow{}
	for rows.Next() {
		var row playerRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.MapName, &row.IsBot, &row.Score, &row.Kills, &row.Deaths, &row.Teamkills, &row.Heals, &row.Repairs, &row.Source); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchEventCounts(ctx context.Context) ([]eventCountRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := s.db.QueryContext(ctx, `
		select raw_event_name, count(*)::int
		from events
		group by raw_event_name
		order by count(*) desc, raw_event_name asc
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []eventCountRow{}
	for rows.Next() {
		var row eventCountRow
		if err := rows.Scan(&row.Name, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchDashboardCombatTrend(ctx context.Context, limit int, serverFilter string) (template.HTML, error) {
	if s.db == nil {
		return "", sql.ErrConnDone
	}
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(g.map_name, ''),
			coalesce(sum(s.kills), 0)::int,
			coalesce(sum(s.deaths), 0)::int,
			coalesce(sum(s.score), 0)::int
		from rounds r
		join games g on g.id = r.game_id
		left join round_player_stats s on s.round_id = r.id
		where ($2 = '' or g.server_id = nullif($2,'')::uuid)
		group by r.id, g.map_name, r.round_index, g.started_at
		having coalesce(sum(s.kills), 0) + coalesce(sum(s.deaths), 0) > 0
		order by g.started_at desc
		limit $1
	`, limit, serverFilter)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	points := []seriesPoint{}
	for rows.Next() {
		var p seriesPoint
		if err := rows.Scan(&p.Label, &p.Kills, &p.Deaths, &p.Score); err != nil {
			return "", err
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	return combatTrendHTML(points), nil
}

func (s *apiServer) fetchDashboardPlayerMetrics(ctx context.Context, serverFilter string) (template.HTML, error) {
	if s.db == nil {
		return "", sql.ErrConnDone
	}
	rows, err := s.db.QueryContext(ctx, `
		with totals as (
			select
				p.id::text as id,
				p.display_name,
				count(distinct s.round_id)::int as rounds,
				coalesce(sum(s.score), 0)::int as score,
				coalesce(sum(s.kills), 0)::int as kills,
				coalesce(sum(s.deaths), 0)::int as deaths,
				coalesce(sum(s.teamkills), 0)::int as teamkills,
				coalesce(sum(s.heals + s.repairs + s.captures), 0)::int as support
			from players p
			join round_player_stats s on s.player_id = p.id
			join rounds r on r.id = s.round_id
			join games g on g.id = r.game_id
			where not p.hidden
			  and ($1 = '' or g.server_id = nullif($1,'')::uuid)
			group by p.id, p.display_name
		),
		ranked as (
			select id, display_name, 'K/D' as metric, case when deaths = 0 then kills::text || '.00' else to_char(kills::numeric / deaths::numeric, 'FM999990.00') end as value, kills || ' kills / ' || deaths || ' deaths' as detail, kills::numeric / greatest(deaths, 1)::numeric as sort_value from totals where rounds >= 3 and kills > 0
			union all
			select id, display_name, 'Kills/Round' as metric, to_char(kills::numeric / greatest(rounds, 1), 'FM999990.0') as value, kills || ' kills across ' || rounds || ' rounds' as detail, kills::numeric / greatest(rounds, 1)::numeric as sort_value from totals where rounds >= 3
			union all
			select id, display_name, 'Score/Round' as metric, to_char(score::numeric / greatest(rounds, 1), 'FM999990.0') as value, score || ' score across ' || rounds || ' rounds' as detail, score::numeric / greatest(rounds, 1)::numeric as sort_value from totals where rounds >= 3
		)
		select id, display_name, metric, value, detail
		from (
			select *, row_number() over (partition by metric order by sort_value desc, display_name asc) as pos
			from ranked
		) top
		where pos <= 4
		order by case metric when 'K/D' then 1 when 'Kills/Round' then 2 else 3 end, pos
	`, serverFilter)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	result := []dashboardMetricRow{}
	for rows.Next() {
		var row dashboardMetricRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Metric, &row.Value, &row.Detail); err != nil {
			return "", err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return metricRowsHTML(result), nil
}

func (s *apiServer) fetchPlayerByID(ctx context.Context, playerID string) (*playerSummaryRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	row := s.db.QueryRowContext(ctx, `
		select
			p.id::text,
			p.display_name,
			case
				when bool_or(s.is_bot is true) then true
				when bool_or(s.is_bot is false) then false
				else null
			end as is_bot,
			count(distinct s.round_id)::int as rounds,
			coalesce(sum(s.score), 0)::int as score,
			coalesce(sum(s.kills), 0)::int as kills,
			coalesce(sum(s.deaths), 0)::int as deaths,
			coalesce(sum(s.teamkills), 0)::int as teamkills,
			coalesce(sum(s.heals), 0)::int as heals,
			coalesce(sum(s.repairs), 0)::int as repairs,
			coalesce(to_char(max(coalesce(r.ended_at, g.started_at)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as last_seen
		from players p
		left join round_player_stats s on s.player_id = p.id
		left join rounds r on r.id = s.round_id
		left join games g on g.id = r.game_id
		where p.id = nullif($1,'')::uuid and not p.hidden
		group by p.id, p.display_name
	`, playerID)

	var result playerSummaryRow
	var isBot sql.NullBool
	if err := row.Scan(&result.ID, &result.DisplayName, &isBot, &result.Rounds, &result.Score, &result.Kills, &result.Deaths, &result.Teamkills, &result.Heals, &result.Repairs, &result.LastSeen); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if isBot.Valid {
		result.IsBot = &isBot.Bool
	}
	return &result, nil
}

func (s *apiServer) fetchPlayerSearch(ctx context.Context, query string, botFilter string, limit int, serverFilter string) ([]playerSummaryRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := s.db.QueryContext(ctx, `
		with player_totals as (
			select
				p.id,
				p.display_name,
				case
					when bool_or(s.is_bot is true) then true
					when bool_or(s.is_bot is false) then false
					else null
				end as is_bot,
				count(distinct s.round_id)::int as rounds,
				coalesce(sum(s.score), 0)::int as score,
				coalesce(sum(s.kills), 0)::int as kills,
				coalesce(sum(s.deaths), 0)::int as deaths,
				coalesce(sum(s.teamkills), 0)::int as teamkills,
				coalesce(sum(s.heals), 0)::int as heals,
				coalesce(sum(s.repairs), 0)::int as repairs,
				coalesce(to_char(max(coalesce(r.ended_at, g.started_at)) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as last_seen
			from players p
			left join round_player_stats s on s.player_id = p.id
			left join rounds r on r.id = s.round_id
			left join games g on g.id = r.game_id
			where (
				$1 = ''
				or p.display_name ilike '%' || $1 || '%'
				or exists (
					select 1 from player_aliases a
					where a.player_id = p.id and a.alias ilike '%' || $1 || '%'
				)
			) and not p.hidden
			and ($4 = '' or g.server_id = nullif($4,'')::uuid)
			group by p.id, p.display_name
		)
		select id::text, display_name, is_bot, rounds, score, kills, deaths, teamkills, heals, repairs, last_seen
		from player_totals
		where (
			$2 = 'all'
			or ($2 = 'bots' and is_bot is true)
			or ($2 = 'humans' and coalesce(is_bot, false) is false)
		)
		order by kills desc, score desc, deaths asc, display_name asc
		limit $3
	`, query, botFilter, limit, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []playerSummaryRow{}
	for rows.Next() {
		var row playerSummaryRow
		var isBot sql.NullBool
		if err := rows.Scan(&row.ID, &row.DisplayName, &isBot, &row.Rounds, &row.Score, &row.Kills, &row.Deaths, &row.Teamkills, &row.Heals, &row.Repairs, &row.LastSeen); err != nil {
			return nil, err
		}
		if isBot.Valid {
			row.IsBot = &isBot.Bool
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchWeaponStats(ctx context.Context, limit int, playerID string, query string, serverFilter string) ([]weaponStatRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	if playerID == "" && query == "" && serverFilter == "" {
		if rows, ok, err := s.fetchWeaponStatsFromMV(ctx, limit); ok {
			return rows, err
		}
	}
	// playerID is resolved (and hidden players substituted with nilUUID) up
	// front so identity-based filtering below can never match a hidden
	// player's events, regardless of whether the id came in directly or via
	// a display_name search.
	playerIDs, err := s.resolvePlayerIDs(ctx, playerID, query, 100)
	if err != nil {
		return nil, err
	}
	idFilter := strings.Join(playerIDs, ",")
	rows, err := s.db.QueryContext(ctx, `
		with vehicle_codes as (
			select code from vehicle_code_list_mv
		)
		select
			e.weapon_code as weapon_code,
			count(*) filter (
				where ($2 = '' and $3 = '')
				or ($2 <> '' and coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = any(string_to_array($2, ',')))
				or ($3 <> '' and e.weapon_code ilike '%' || $3 || '%')
			)::int as uses,
			count(*) filter (
				where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
				and (
					($2 = '' and $3 = '')
					or ($2 <> '' and coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = any(string_to_array($2, ',')))
					or ($3 <> '' and e.weapon_code ilike '%' || $3 || '%')
				)
			)::int as kills,
			count(*) filter (
				where lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills')
				and (
					($2 = '' and $3 = '')
					or ($2 <> '' and coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = any(string_to_array($2, ',')))
					or ($3 <> '' and e.weapon_code ilike '%' || $3 || '%')
				)
			)::int as teamkills,
			count(*) filter (
				where lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills', 'tk', 'teamkill', 'teamkills')
				and (
					($2 = '' and $3 = '')
					or ($2 <> '' and coalesce(e.target_player_id::text, target_slot_stats.player_id::text) = any(string_to_array($2, ',')))
					or ($3 <> '' and e.weapon_code ilike '%' || $3 || '%')
				)
			)::int as deaths,
			count(distinct coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text, e.actor_slot_id::text))::int as users
		from events e
		left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_slot_stats on target_slot_stats.round_id = e.round_id and target_slot_stats.slot_id = e.target_slot_id
		where nullif(e.weapon_code, '') is not null
		and e.vehicle_code is null
		and coalesce(e.weapon_code, '') not in (select code from vehicle_codes)
		and ($4 = '' or e.server_id = nullif($4,'')::uuid)
		and (
			($2 = '' and $3 = '')
			or ($2 <> '' and (
				coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = any(string_to_array($2, ','))
				or coalesce(e.target_player_id::text, target_slot_stats.player_id::text) = any(string_to_array($2, ','))
			))
			or ($3 <> '' and e.weapon_code ilike '%' || $3 || '%')
		)
		group by e.weapon_code
		order by kills desc, uses desc, deaths desc, weapon_code asc
		limit $1
	`, limit, idFilter, query, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []weaponStatRow{}
	for rows.Next() {
		var row weaponStatRow
		if err := rows.Scan(&row.WeaponCode, &row.Uses, &row.Kills, &row.Teamkills, &row.Deaths, &row.Users); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// fetchWeaponStatsFromMV is the fast path for the unfiltered /weapons list:
// weapon_totals_mv (migration 0002) already has uses/kills/teamkills/users
// with identical semantics to the live query's unfiltered case (verified by
// direct comparison against the live query). "deaths" isn't a column there,
// but with no player/search filter the live query's deaths filter condition
// collapses to just the kill-or-teamkill score_type check — i.e. it's
// exactly kills+teamkills, not a separately-tracked value — so no new
// migration is needed. ok is false (falls back to the live query) on an
// older install that hasn't applied migration 0002's summary views.
func (s *apiServer) fetchWeaponStatsFromMV(ctx context.Context, limit int) ([]weaponStatRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select weapon_code, uses, kills, teamkills, users
		from weapon_totals_mv
		order by kills desc, uses desc, teamkills desc, weapon_code asc
		limit $1
	`, limit)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	result := []weaponStatRow{}
	for rows.Next() {
		var row weaponStatRow
		if err := rows.Scan(&row.WeaponCode, &row.Uses, &row.Kills, &row.Teamkills, &row.Users); err != nil {
			return nil, true, err
		}
		row.Deaths = row.Kills + row.Teamkills
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func (s *apiServer) fetchVehicleStats(ctx context.Context, limit int, playerID string, query string, serverFilter string) ([]vehicleStatRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	if playerID == "" && query == "" && serverFilter == "" {
		if rows, ok, err := s.fetchVehicleStatsFromMV(ctx, limit); ok {
			return rows, err
		}
	}
	// See fetchWeaponStats: resolve playerID/query through resolvePlayerIDs
	// first so a hidden player's id (or display name) can never filter in
	// their own vehicle activity.
	playerIDs, err := s.resolvePlayerIDs(ctx, playerID, query, 100)
	if err != nil {
		return nil, err
	}
	idFilter := strings.Join(playerIDs, ",")
	rows, err := s.db.QueryContext(ctx, `
		with vehicle_codes as (
			select code from vehicle_code_list_mv
		),
		vehicle_events as (
			select
				regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') as vehicle_code,
				e.raw_event_name,
				e.event_type,
				e.params,
				e.round_id,
				coalesce(e.actor_player_id, actor_slot_stats.player_id) as actor_id
			from events e
			left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
			where (
				e.event_type in ('enter_vehicle', 'exit_vehicle', 'destroy_vehicle')
				or (e.event_type = 'score' and e.vehicle_code is not null)
				or (e.event_type = 'score' and coalesce(e.weapon_code, '') in (select code from vehicle_codes))
			)
			and ($4 = '' or e.server_id = nullif($4,'')::uuid)
			and coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')) is not null
			and regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') not like '%Soldier'
			and lower(regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '')) not like '%dummy%'
			and lower(regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '')) not like '%camera%'
			and lower(regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '')) not like '%wheel%'
			and (
				($2 = '' and $3 = '')
				or ($2 <> '' and coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) = any(string_to_array($2, ',')))
				or ($3 <> '' and regexp_replace(coalesce(nullif(e.vehicle_code, ''), nullif(e.weapon_code, '')), '_PCO[0-9]+$', '') ilike '%' || $3 || '%')
			)
		)
		select
			vehicle_code,
			count(*) filter (where event_type = 'enter_vehicle')::int as entries,
			count(*) filter (where event_type = 'score' and lower(coalesce(params->>'score_type', '')) in ('kill', 'kills'))::int as kills,
			count(*) filter (where event_type = 'score' and lower(coalesce(params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int as teamkills,
			count(distinct actor_id)::int as users,
			count(distinct round_id)::int as rounds,
			count(*) filter (where raw_event_name = 'destroyVehicle' or event_type = 'destroy_vehicle')::int as destroyed,
			count(distinct actor_id) filter (where raw_event_name = 'destroyVehicle' or event_type = 'destroy_vehicle')::int as destroyers
		from vehicle_events
		group by vehicle_code
		order by kills desc, entries desc, destroyed desc, vehicle_code asc
		limit $1
	`, limit, idFilter, query, serverFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []vehicleStatRow{}
	for rows.Next() {
		var row vehicleStatRow
		if err := rows.Scan(&row.VehicleCode, &row.Entries, &row.Kills, &row.Teamkills, &row.Users, &row.Rounds, &row.Destroyed, &row.Destroyers); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// fetchVehicleStatsFromMV is the fast path for the unfiltered /vehicles
// list: vehicle_activity_totals_mv (migration 0008) precomputes exactly
// this aggregation. ok is false (falls back to the live query) on an older
// install that hasn't applied that migration yet.
func (s *apiServer) fetchVehicleStatsFromMV(ctx context.Context, limit int) ([]vehicleStatRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		select vehicle_code, entries, kills, teamkills, users, rounds, destroyed, destroyers
		from vehicle_activity_totals_mv
		order by kills desc, entries desc, destroyed desc, vehicle_code asc
		limit $1
	`, limit)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, true, err
	}
	defer rows.Close()
	result := []vehicleStatRow{}
	for rows.Next() {
		var row vehicleStatRow
		if err := rows.Scan(&row.VehicleCode, &row.Entries, &row.Kills, &row.Teamkills, &row.Users, &row.Rounds, &row.Destroyed, &row.Destroyers); err != nil {
			return nil, true, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func (s *apiServer) fetchWeaponUsers(ctx context.Context, weaponCode string, botFilter string, playerID string, query string, limit int) ([]weaponUserRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	if weaponCode == "" {
		return []weaponUserRow{}, nil
	}
	playerIDs, err := s.resolvePlayerIDs(ctx, playerID, query, 100)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		with weapon_events as (
			select
				coalesce(e.actor_player_id, actor_slot_stats.player_id) as player_id,
				actor_slot_stats.is_bot,
				e.params,
				coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) as event_time
			from events e
			left join games g on g.id = e.game_id
			left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
			where coalesce(e.weapon_code, '') = $1
			and (
				$4 = ''
				or coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) in (
					select unnest(string_to_array($4, ','))
				)
			)
		),
		weapon_totals as (
			select
				player_id,
				case
					when bool_or(is_bot is true) then true
					when bool_or(is_bot is false) then false
					else null
				end as is_bot,
				count(*)::int as uses,
				count(*) filter (where lower(coalesce(params->>'score_type', '')) in ('kill', 'kills'))::int as kills,
				count(*) filter (where lower(coalesce(params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills'))::int as teamkills,
				coalesce(to_char(max(event_time) at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), '') as last_used
			from weapon_events
			where player_id is not null
			group by player_id
		)
		select wt.player_id::text, p.display_name, wt.is_bot, wt.uses, wt.kills, wt.teamkills, wt.last_used
		from weapon_totals wt
		join players p on p.id = wt.player_id and not p.hidden
		where (
			$2 = 'all'
			or ($2 = 'bots' and wt.is_bot is true)
			or ($2 = 'humans' and coalesce(wt.is_bot, false) is false)
		)
		order by wt.kills desc, wt.uses desc, p.display_name asc
		limit $3
	`, weaponCode, botFilter, limit, strings.Join(playerIDs, ","))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []weaponUserRow{}
	for rows.Next() {
		var row weaponUserRow
		var isBot sql.NullBool
		if err := rows.Scan(&row.ID, &row.DisplayName, &isBot, &row.Uses, &row.Kills, &row.Teamkills, &row.LastUsed); err != nil {
			return nil, err
		}
		if isBot.Valid {
			row.IsBot = &isBot.Bool
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchWeaponEvents(ctx context.Context, weaponCode string, playerID string, query string, limit int) ([]playerEventRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	if weaponCode == "" {
		return []playerEventRow{}, nil
	}
	playerIDs, err := s.resolvePlayerIDs(ctx, playerID, query, 100)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		select
			coalesce(to_char(e.occurred_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), to_char(g.started_at + (e.timestamp_seconds || ' seconds')::interval, 'YYYY-MM-DD HH24:MI:SS "UTC"')),
			coalesce(g.map_name, ''),
			coalesce(r.round_index, 0),
			e.event_type,
			e.raw_event_name,
			coalesce(actor.id::text, ''),
			coalesce(actor.display_name, ''),
			case
				when actor_slot_stats.is_bot is true then 'Bot'
				when actor_slot_stats.is_bot is false then 'Human'
				else ''
			end as actor_kind,
			coalesce(target.id::text, ''),
			coalesce(target.display_name, ''),
			case
				when target_slot_stats.is_bot is true then 'Bot'
				when target_slot_stats.is_bot is false then 'Human'
				else ''
			end as target_kind,
			coalesce(e.weapon_code, ''),
			coalesce(e.vehicle_code, ''),
			coalesce(e.kit_code, ''),
			coalesce(e.chat_text, ''),
			coalesce((e.params - 'keyhash')::text, '{}')
		from events e
		left join rounds r on r.id = e.round_id
		left join games g on g.id = e.game_id
		left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_slot_stats on target_slot_stats.round_id = e.round_id and target_slot_stats.slot_id = e.target_slot_id
		left join players actor on actor.id = coalesce(e.actor_player_id, actor_slot_stats.player_id) and not actor.hidden
		left join players target on target.id = coalesce(e.target_player_id, target_slot_stats.player_id) and not target.hidden
		where coalesce(e.weapon_code, '') = $2
		and (
			$3 = ''
			or coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) in (
				select unnest(string_to_array($3, ','))
			)
			or coalesce(e.target_player_id::text, target_slot_stats.player_id::text) in (
				select unnest(string_to_array($3, ','))
			)
		)
		order by coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) desc, e.sequence_no desc
		limit $1
	`, limit, weaponCode, strings.Join(playerIDs, ","))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []playerEventRow{}
	for rows.Next() {
		var row playerEventRow
		if err := rows.Scan(&row.When, &row.MapName, &row.RoundIndex, &row.EventType, &row.RawEventName, &row.ActorID, &row.ActorName, &row.ActorKind, &row.TargetID, &row.TargetName, &row.TargetKind, &row.WeaponCode, &row.VehicleCode, &row.KitCode, &row.ChatText, &row.Params); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *apiServer) fetchPlayerEvents(ctx context.Context, limit int, playerID string, query string, eventFilter string, weaponFilter string) ([]playerEventRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	playerIDs, err := s.resolvePlayerIDs(ctx, playerID, query, 100)
	if err != nil {
		return nil, err
	}
	return s.fetchPlayerEventsForIDs(ctx, limit, playerIDs, eventFilter, weaponFilter)
}

func (s *apiServer) fetchPlayerActivity(ctx context.Context, playerID string, query string, limit int) ([]eventCountRow, error) {
	if s.db == nil {
		return nil, sql.ErrConnDone
	}
	playerIDs, err := s.resolvePlayerIDs(ctx, playerID, query, 100)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return []eventCountRow{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		with selected_players as (
			select unnest(string_to_array($1, ',')) as id
		)
		select
			case
				when e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills') and coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) in (select id from selected_players) then 'kills'
				when e.event_type = 'score' and lower(coalesce(e.params->>'score_type', '')) in ('tk', 'teamkill', 'teamkills') and coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) in (select id from selected_players) then 'teamkills'
				when e.event_type = 'score' and coalesce(e.target_player_id::text, target_slot_stats.player_id::text) in (select id from selected_players) then 'deaths'
				when e.event_type = 'enter_vehicle' then 'vehicles entered'
				when e.event_type = 'pickup_kit' then 'kits picked up'
				when e.event_type in ('begin_heal', 'end_heal') then 'healing events'
				when e.event_type in ('begin_repair', 'end_repair') then 'repair events'
				when e.raw_event_name = 'spawnEvent' then 'spawns'
				when e.event_type = 'destroy_vehicle' then 'vehicles destroyed'
				else e.event_type
			end as name,
			count(*)::int as count
		from events e
		left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_slot_stats on target_slot_stats.round_id = e.round_id and target_slot_stats.slot_id = e.target_slot_id
		where (
			coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) in (select id from selected_players)
			or coalesce(e.target_player_id::text, target_slot_stats.player_id::text) in (select id from selected_players)
		)
		group by name
		order by count desc, name asc
		limit $2
	`, strings.Join(playerIDs, ","), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []eventCountRow{}
	for rows.Next() {
		var row eventCountRow
		if err := rows.Scan(&row.Name, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// nilUUID is substituted for a hidden or nonexistent player id in
// resolvePlayerIDs: it keeps the player filter ACTIVE while matching no
// rows. Returning an empty list instead would be interpreted by several
// event queries as "no filter requested" and dump unfiltered events —
// exactly what a hidden player's id must not do.
const nilUUID = "00000000-0000-0000-0000-000000000000"

func (s *apiServer) resolvePlayerIDs(ctx context.Context, playerID string, query string, limit int) ([]string, error) {
	playerIDs := []string{}
	if playerID != "" {
		var hidden bool
		err := s.db.QueryRowContext(ctx, `
			select hidden from players where id = nullif($1,'')::uuid
		`, playerID).Scan(&hidden)
		if err != nil || hidden {
			playerID = nilUUID
		}
		playerIDs = append(playerIDs, playerID)
	} else if query != "" {
		ids, err := s.fetchMatchingPlayerIDs(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		playerIDs = ids
		if len(playerIDs) == 0 {
			return []string{}, nil
		}
	}
	return playerIDs, nil
}

func (s *apiServer) fetchMatchingPlayerIDs(ctx context.Context, query string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		select p.id::text
		from players p
		where (p.display_name ilike '%' || $1 || '%'
		or exists (
			select 1 from player_aliases a
			where a.player_id = p.id and a.alias ilike '%' || $1 || '%'
		)) and not p.hidden
		order by p.display_name asc
		limit $2
	`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *apiServer) fetchPlayerEventsForIDs(ctx context.Context, limit int, playerIDs []string, eventFilter string, weaponFilter string) ([]playerEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		with selected_players as (
			select unnest(string_to_array($2, ',')) as id
		)
		select
			coalesce(to_char(e.occurred_at at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS "UTC"'), to_char(g.started_at + (e.timestamp_seconds || ' seconds')::interval, 'YYYY-MM-DD HH24:MI:SS "UTC"')),
			coalesce(g.map_name, ''),
			coalesce(r.round_index, 0),
			e.event_type,
			e.raw_event_name,
			coalesce(actor.id::text, ''),
			coalesce(actor.display_name, ''),
			case
				when actor_slot_stats.is_bot is true then 'Bot'
				when actor_slot_stats.is_bot is false then 'Human'
				else ''
			end as actor_kind,
			coalesce(target.id::text, ''),
			coalesce(target.display_name, ''),
			case
				when target_slot_stats.is_bot is true then 'Bot'
				when target_slot_stats.is_bot is false then 'Human'
				else ''
			end as target_kind,
			coalesce(e.weapon_code, ''),
			coalesce(e.vehicle_code, ''),
			coalesce(e.kit_code, ''),
			coalesce(e.chat_text, ''),
			coalesce((e.params - 'keyhash')::text, '{}')
		from events e
		left join rounds r on r.id = e.round_id
		left join games g on g.id = e.game_id
		left join round_player_stats actor_slot_stats on actor_slot_stats.round_id = e.round_id and actor_slot_stats.slot_id = e.actor_slot_id
		left join round_player_stats target_slot_stats on target_slot_stats.round_id = e.round_id and target_slot_stats.slot_id = e.target_slot_id
		left join players actor on actor.id = coalesce(e.actor_player_id, actor_slot_stats.player_id) and not actor.hidden
		left join players target on target.id = coalesce(e.target_player_id, target_slot_stats.player_id) and not target.hidden
		where (
			$2 = ''
			or coalesce(e.actor_player_id::text, actor_slot_stats.player_id::text) in (select id from selected_players)
			or coalesce(e.target_player_id::text, target_slot_stats.player_id::text) in (select id from selected_players)
		)
		and ($3 = '' or e.event_type = $3 or e.raw_event_name = $3)
		and ($4 = '' or coalesce(e.weapon_code, '') = $4)
		order by coalesce(e.occurred_at, g.started_at + (e.timestamp_seconds || ' seconds')::interval) desc, e.sequence_no desc
		limit $1
	`, limit, strings.Join(playerIDs, ","), eventFilter, weaponFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []playerEventRow{}
	for rows.Next() {
		var row playerEventRow
		if err := rows.Scan(&row.When, &row.MapName, &row.RoundIndex, &row.EventType, &row.RawEventName, &row.ActorID, &row.ActorName, &row.ActorKind, &row.TargetID, &row.TargetName, &row.TargetKind, &row.WeaponCode, &row.VehicleCode, &row.KitCode, &row.ChatText, &row.Params); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func normalizeBotFilter(value string) string {
	switch value {
	case "bots", "humans":
		return value
	default:
		return "all"
	}
}

func playerKind(isBot *bool) string {
	if isBot == nil {
		return "Unknown"
	}
	if *isBot {
		return "Bot"
	}
	return "Human"
}

func eventSummary(row playerEventRow) string {
	params := map[string]string{}
	_ = json.Unmarshal([]byte(row.Params), &params)
	scoreType := strings.ToLower(params["score_type"])
	actor := row.ActorName
	if actor == "" {
		actor = "Unknown"
	}
	target := row.TargetName
	weapon := row.WeaponCode
	if weapon == "" {
		weapon = params["weapon"]
	}

	switch row.EventType {
	case "score":
		switch scoreType {
		case "kill", "kills":
			if target != "" {
				if weapon != "" {
					return fmt.Sprintf("%s killed %s with %s", actor, target, weapon)
				}
				return fmt.Sprintf("%s killed %s", actor, target)
			}
			return fmt.Sprintf("%s scored a kill", actor)
		case "tk", "teamkill", "teamkills":
			if target != "" {
				return fmt.Sprintf("%s teamkilled %s", actor, target)
			}
			return fmt.Sprintf("%s made a teamkill", actor)
		case "death", "deathnomsg":
			return fmt.Sprintf("%s died", actor)
		default:
			if scoreType != "" {
				return fmt.Sprintf("%s score event: %s", actor, scoreType)
			}
		}
	case "pickup_kit":
		if row.KitCode != "" {
			return fmt.Sprintf("%s picked up %s", actor, displayCode(row.KitCode))
		}
	case "enter_vehicle":
		if row.VehicleCode != "" {
			return fmt.Sprintf("%s entered %s", actor, row.VehicleCode)
		}
	case "exit_vehicle":
		if row.VehicleCode != "" {
			return fmt.Sprintf("%s exited %s", actor, row.VehicleCode)
		}
	case "begin_heal", "end_heal":
		if target != "" {
			return fmt.Sprintf("%s healed %s", actor, target)
		}
		return fmt.Sprintf("%s used a medpack", actor)
	case "begin_repair", "end_repair":
		if row.VehicleCode != "" {
			return fmt.Sprintf("%s repaired %s", actor, row.VehicleCode)
		}
		return fmt.Sprintf("%s repaired", actor)
	case "spawn":
		return fmt.Sprintf("%s spawned", actor)
	case "destroy_vehicle":
		if row.VehicleCode != "" {
			return fmt.Sprintf("%s destroyed %s", actor, row.VehicleCode)
		}
	case "chat", "radio_message":
		if row.ChatText != "" {
			return fmt.Sprintf("%s: %s", actor, row.ChatText)
		}
	}

	if target != "" {
		return fmt.Sprintf("%s -> %s", actor, target)
	}
	return row.RawEventName
}

func selectedFilter(current string, value string) string {
	if current == value {
		return "active"
	}
	return ""
}

func queryEscape(value string) string {
	return url.QueryEscape(value)
}

func displayCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unknown"
	}
	value = stripPCOSuffix(value)
	value = strings.TrimSuffix(value, "PCO")
	value = strings.TrimSuffix(value, "PCO5")
	value = strings.TrimSuffix(value, "_AI")
	value = strings.ReplaceAll(value, "_", " ")
	value = splitCodeWords(value)
	value = expandFactionAbbrev(value)
	replacer := strings.NewReplacer(
		"Passanger", "Passenger",
		"Canons", "Cannons",
		"Multi Player", "Multiplayer",
		"mg42", "MG42",
	)
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	return value
}

// expandFactionAbbrev spells out BF1942 kit faction codes so abbreviations
// (including "Jap", a racial slur) never render in the UI.
func expandFactionAbbrev(value string) string {
	words := strings.Fields(value)
	for i, w := range words {
		switch w {
		case "Jap":
			words[i] = "Japan"
		case "Rus":
			words[i] = "Russian"
		case "Us":
			words[i] = "US"
		case "USMarine":
			words[i] = "US Marine"
		}
	}
	return strings.Join(words, " ")
}

func splitCodeWords(value string) string {
	var b strings.Builder
	var prev rune
	for i, r := range value {
		if i > 0 && r >= 'A' && r <= 'Z' && ((prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

func stripPCOSuffix(value string) string {
	i := strings.LastIndex(value, "_PCO")
	if i <= 0 {
		return value
	}
	for _, r := range value[i+len("_PCO"):] {
		if r < '0' || r > '9' {
			return value
		}
	}
	return value[:i]
}

func playerURL(playerID string, botFilter string) string {
	values := url.Values{}
	values.Set("id", playerID)
	return "/player?" + values.Encode()
}

func roundURL(roundID string) string {
	values := url.Values{}
	values.Set("id", roundID)
	return "/round?" + values.Encode()
}

func mapURL(name string) string {
	values := url.Values{}
	values.Set("name", name)
	return "/map?" + values.Encode()
}

func weaponURL(code string) string {
	values := url.Values{}
	values.Set("code", code)
	return "/weapon?" + values.Encode()
}

func vehicleURL(code string) string {
	values := url.Values{}
	values.Set("code", stripPCOSuffix(code))
	return "/vehicle?" + values.Encode()
}

func rivalryURL(a string, b string) string {
	values := url.Values{}
	values.Set("a", a)
	values.Set("b", b)
	return "/rivalry?" + values.Encode()
}

func participantCell(playerID string, name string, kind string, botFilter string) template.HTML {
	if name == "" && kind == "" {
		return ""
	}
	label := template.HTMLEscapeString(name)
	if label == "" {
		label = "Unknown"
	}
	kindText := ""
	if kind != "" {
		kindText = ` <span class="muted">` + template.HTMLEscapeString(kind) + `</span>`
	}
	if playerID == "" {
		return template.HTML(label + kindText)
	}
	return template.HTML(`<a class="link" href="` + template.HTMLEscapeString(playerURL(playerID, botFilter)) + `">` + label + `</a>` + kindText)
}

// dashboardWeaponKillsQuery and dashboardVehicleKillsQuery are the overview
// fast paths: kills only, no per-event stats joins, no filter machinery.
const dashboardWeaponKillsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select e.weapon_code as code, count(*)::int as kills
	from events e
	where e.event_type = 'score'
	and e.vehicle_code is null
	and nullif(e.weapon_code, '') is not null
	and ($2 = '' or e.server_id = nullif($2,'')::uuid)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	and e.weapon_code not in (select code from vehicle_codes)
	group by 1 order by kills desc, code asc limit $1`

const dashboardVehicleKillsQuery = `
	with vehicle_codes as (
		select code from vehicle_code_list_mv
	)
	select regexp_replace(coalesce(nullif(e.vehicle_code, ''), e.weapon_code), '_PCO[0-9]+$', '') as code, count(*)::int as kills
	from events e
	where e.event_type = 'score'
	and ($2 = '' or e.server_id = nullif($2,'')::uuid)
	and lower(coalesce(e.params->>'score_type', '')) in ('kill', 'kills')
	and (e.vehicle_code is not null or e.weapon_code in (select code from vehicle_codes))
	and coalesce(nullif(e.vehicle_code, ''), e.weapon_code, '') not like '%Soldier'
	group by 1 order by kills desc, code asc limit $1`

func (s *apiServer) fetchKillBars(ctx context.Context, query string, limit int, color string, serverFilter string) (template.HTML, error) {
	if s.db == nil {
		return "", sql.ErrConnDone
	}
	rows, err := s.db.QueryContext(ctx, query, limit, serverFilter)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	items := []barItem{}
	for rows.Next() {
		var code string
		var kills int
		if err := rows.Scan(&code, &kills); err != nil {
			return "", err
		}
		items = append(items, barItem{Label: displayCode(code), Value: kills})
	}
	return barListHTML(items, color), rows.Err()
}

const dashboardWeaponKillsFromMVQuery = `select weapon_code, kills from weapon_totals_mv where kills > 0 order by kills desc, weapon_code asc limit $1`
const dashboardVehicleKillsFromMVQuery = `select vehicle_code, kills from vehicle_kill_totals_mv order by kills desc, vehicle_code asc limit $1`

// fetchKillBarsFromMV is the fast path for the all-servers dashboard: reads
// migration 0007's precomputed totals instead of re-scanning `events`
// (millions of rows) on every homepage load. Falls back to the live query
// on older installs that haven't applied that migration yet.
func (s *apiServer) fetchKillBarsFromMV(ctx context.Context, mvQuery string, liveQuery string, limit int, color string, serverFilter string) (template.HTML, error) {
	if s.db == nil {
		return "", sql.ErrConnDone
	}
	if serverFilter == "" {
		rows, err := s.db.QueryContext(ctx, mvQuery, limit)
		if err == nil {
			defer rows.Close()
			items := []barItem{}
			for rows.Next() {
				var code string
				var kills int
				if err := rows.Scan(&code, &kills); err != nil {
					return "", err
				}
				items = append(items, barItem{Label: displayCode(code), Value: kills})
			}
			if err := rows.Err(); err != nil {
				return "", err
			}
			return barListHTML(items, color), nil
		}
		if !strings.Contains(err.Error(), "does not exist") {
			return "", err
		}
	}
	return s.fetchKillBars(ctx, liveQuery, limit, color, serverFilter)
}

func weaponStatsBars(rows []weaponStatRow) template.HTML {
	items := []barItem{}
	for i, row := range rows {
		if i >= 10 {
			break
		}
		items = append(items, barItem{Label: displayCode(row.WeaponCode), Value: row.Kills})
	}
	return barListHTML(items, colorKills)
}

func weaponEfficiencyBars(rows []weaponStatRow) template.HTML {
	items := []barItem{}
	for i, row := range rows {
		if i >= 12 {
			break
		}
		items = append(items, barItem{Label: displayCode(row.WeaponCode), Value: int(ratio(row.Kills, row.Users) * 100)})
	}
	return barListHTML(items, colorInfo)
}

func vehicleStatsBars(rows []vehicleStatRow) template.HTML {
	items := []barItem{}
	for i, row := range rows {
		if i >= 10 {
			break
		}
		items = append(items, barItem{Label: displayCode(row.VehicleCode), Value: row.Kills})
	}
	return barListHTML(items, colorScore)
}

func vehicleEfficiencyBars(rows []vehicleStatRow) template.HTML {
	items := []barItem{}
	for i, row := range rows {
		if i >= 12 {
			break
		}
		items = append(items, barItem{Label: displayCode(row.VehicleCode), Value: int(ratio(row.Kills, row.Entries) * 100)})
	}
	return barListHTML(items, colorScore)
}

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"css": cssFunc, "favicon": faviconFunc,
	"displayCode": displayCode,
	"playerKind":  playerKind,
	"playerURL":   playerURL,
	"mapURL":      mapURL,
	"roundURL":    roundURL,
	"siteHeader":  siteHeader,
	"siteFooter":  siteFooter,
	"rank":        func(i int) int { return i + 1 },
	"durationMin": func(seconds int) string { return fmt.Sprintf("%dm", (seconds+30)/60) },
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Spawnpoint</title>
  <style>{{ css }}</style>{{ favicon }}
</head>
<body>
  {{ siteHeader "overview" }}
  <main>
    {{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
    <div class="overview-shell">
      <section class="overview-hero">
        <span class="eyebrow">Battlefield 1942 server stats</span>
        <h2>After-action intelligence for every round played.</h2>
        <p>Round reports, player profiles, weapon and vehicle breakdowns, awards, and live status &mdash; rebuilt from the engine&rsquo;s own event logs.</p>
        <div class="overview-actions">
          <a class="action-link primary" href="/live">Live status</a>
          <a class="action-link" href="{{ .LeaderboardURL }}">Leaderboard</a>
          <a class="action-link" href="{{ .RoundsURL }}">Browse rounds</a>
        </div>
        {{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
        <div class="stat-note"><span>Generated {{ .GeneratedAt.Format "2006-01-02 15:04:05 UTC" }}</span></div>
      </section>
      <aside class="overview-rail">
        <div class="kpi-grid">
          <a class="kpi-card card-link" href="{{ .RoundsURL }}"><span>Rounds</span><strong>{{ .Totals.Rounds }}</strong><small>recorded</small></a>
          <a class="kpi-card card-link" href="{{ .PlayersURL }}"><span>Players</span><strong>{{ .Totals.Players }}</strong><small>identified</small></a>
          <a class="kpi-card card-link gold" href="{{ .WeaponsURL }}"><span>Kills</span><strong>{{ .Totals.Kills }}</strong><small>scored</small></a>
          <a class="kpi-card card-link green" href="{{ .MapsURL }}"><span>Maps</span><strong>{{ .Totals.Maps }}</strong><small>in rotation</small></a>
        </div>
      </aside>
    </div>

    <div class="dashboard-hero">
      <section class="panel chart-panel">
        <h3>Recent Combat Trend</h3>
        <div class="chart">{{ .CombatTrend }}</div>
        <div class="legend">
          <span><i style="background:#bc861f"></i>Kills</span>
          <span><i style="background:#d05e55"></i>Deaths</span>
        </div>
      </section>
      <section class="panel">
        <h3>Player Standouts</h3>
        <div class="metric-scroll">{{ .PlayerMetrics }}</div>
      </section>
    </div>

    <div class="grid2">
      <section class="panel">
        <div class="section-heading"><h3>Top Weapons</h3><a href="{{ .WeaponsURL }}">Weapon index</a></div>
        {{ .WeaponBars }}
      </section>
      <section class="panel">
        <div class="section-heading"><h3>Top Vehicles</h3><a href="{{ .VehiclesURL }}">Vehicle index</a></div>
        {{ .VehicleBars }}
      </section>
    </div>

    <section class="data-section">
      <div class="section-heading"><h2>Top Players</h2><a href="{{ .LeaderboardURL }}">Full leaderboard</a></div>
      <table>
        <thead>
          <tr><th>#</th><th>Player</th><th>Type</th><th>Rounds</th><th>Kills</th><th>Deaths</th><th>K/D</th><th>Score</th></tr>
        </thead>
        <tbody>
          {{ range $i, $row := .TopPlayers }}
          <tr>
            <td>{{ rank $i }}</td>
            <td><a class="link" href="{{ playerURL $row.ID "all" }}">{{ $row.DisplayName }}</a></td>
            <td>{{ playerKind $row.IsBot }}</td>
            <td>{{ $row.Rounds }}</td>
            <td>{{ $row.Kills }}</td>
            <td>{{ $row.Deaths }}</td>
            <td>{{ printf "%.2f" $row.KD }}</td>
            <td>{{ $row.Score }}</td>
          </tr>
          {{ else }}
          <tr><td colspan="8" class="muted">No player stats ingested yet.</td></tr>
          {{ end }}
        </tbody>
      </table>
    </section>

    <section class="data-section">
      <div class="section-heading"><h2>Recent Rounds</h2><a href="{{ .RoundsURL }}">Round archive</a></div>
      <table>
        <thead>
          <tr><th>Map</th><th>Round</th><th>Players</th><th>Kills</th><th>Duration</th><th>Ended</th></tr>
        </thead>
        <tbody>
          {{ range .Rounds }}
          <tr>
            <td><a class="link" href="{{ roundURL .ID }}">{{ displayCode .MapName }}</a></td>
            <td><a class="link" href="{{ roundURL .ID }}"><span class="mono">{{ .RoundHash }}</span></a></td>
            <td>{{ .PlayerCount }}</td>
            <td>{{ .Kills }}</td>
            <td>{{ durationMin .Duration }}</td>
            <td>{{ .EndedAt }}</td>
          </tr>
          {{ else }}
          <tr><td colspan="6" class="muted">No completed rounds ingested yet.</td></tr>
          {{ end }}
        </tbody>
      </table>
    </section>
  </main>
  {{ siteFooter }}
</body>
</html>`))

var playersTemplate = template.Must(template.New("players").Funcs(template.FuncMap{
	"css": cssFunc, "favicon": faviconFunc,
	"playerKind":     playerKind,
	"playerURL":      playerURL,
	"selectedFilter": selectedFilter,
	"siteHeader":     siteHeader,
	"siteFooter":     siteFooter,
	"formatKD":       formatKD,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Players - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "players" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Players</strong><span class="muted">rankings, profile drilldowns, bot/human splits</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<form class="search" method="get" action="/players"><input type="hidden" name="bots" value="{{ .BotFilter }}">{{ if .ServerFilter }}<input type="hidden" name="server" value="{{ .ServerFilter }}">{{ end }}<input type="search" name="q" value="{{ .Query }}" placeholder="Search player or alias"><input type="search" name="limit" value="{{ .Limit }}" placeholder="Limit"><button type="submit">Search</button></form>
<div class="filters">{{ range .BotFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>
<table><thead><tr><th>Player</th><th>Type</th><th>Rounds</th><th>Score</th><th>Kills</th><th>Deaths</th><th>K/D</th><th>TK</th><th>Support</th><th>Last Seen</th></tr></thead><tbody>
{{ range .Players }}<tr><td><a class="link" href="{{ playerURL .ID $.BotFilter }}">{{ .DisplayName }}</a></td><td>{{ playerKind .IsBot }}</td><td>{{ .Rounds }}</td><td>{{ .Score }}</td><td>{{ .Kills }}</td><td>{{ .Deaths }}</td><td>{{ formatKD .Kills .Deaths }}</td><td>{{ .Teamkills }}</td><td>{{ .Heals }} heals / {{ .Repairs }} repairs</td><td>{{ .LastSeen }}</td></tr>{{ else }}<tr><td colspan="10" class="muted">No players found.</td></tr>{{ end }}
</tbody></table>
</main>{{ siteFooter }}</body></html>`))

var roundsTemplate = template.Must(template.New("rounds").Funcs(template.FuncMap{
	"css": cssFunc, "favicon": faviconFunc,
	"displayCode":  displayCode,
	"mapURL":       mapURL,
	"roundOutcome": roundOutcome,
	"roundURL":     roundURL,
	"siteHeader":   siteHeader,
	"siteFooter":   siteFooter,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Rounds - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "rounds" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Rounds</strong><span class="muted">click a round for summary, swings, carry, contribution, and map comparison</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<form class="search" method="get" action="/rounds"><input type="search" name="map" value="{{ .MapName }}" placeholder="Map code"><input type="search" name="from" value="{{ .From }}" placeholder="From YYYY-MM-DD"><input type="search" name="to" value="{{ .To }}" placeholder="To YYYY-MM-DD"><button type="submit">Filter</button></form>
<table><thead><tr><th>Round</th><th>Map</th><th>Mode</th><th>Kills</th><th>Players</th><th>Events</th><th>Outcome</th><th>Duration</th><th>Ended</th></tr></thead><tbody>
{{ range .Rounds }}<tr><td><a class="link" href="{{ roundURL .ID }}"><span class="mono">{{ .RoundHash }}</span></a></td><td><a class="link" href="{{ roundURL .ID }}">{{ displayCode .MapName }}</a></td><td>{{ .GameMode }}</td><td>{{ .Kills }}</td><td>{{ .PlayerCount }}</td><td>{{ .EventCount }}</td><td>{{ roundOutcome .WinningTeamID .VictoryType }}</td><td>{{ .Duration }}s</td><td>{{ .EndedAt }}</td></tr>{{ else }}<tr><td colspan="9" class="muted">No rounds found.</td></tr>{{ end }}
</tbody></table>
{{ if .PageLinks }}<div class="pager"><span>Page</span>{{ range .PageLinks }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
</main>{{ siteFooter }}</body></html>`))

var weaponsTemplate = template.Must(template.New("weapons").Funcs(template.FuncMap{
	"css": cssFunc, "favicon": faviconFunc,
	"displayCode": displayCode,
	"rate":        ratio,
	"siteHeader":  siteHeader,
	"siteFooter":  siteFooter,
	"tkRate":      tkRate,
	"weaponURL":   weaponURL,
	"formatKD":    formatKD,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Weapons - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "weapons" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Weapons</strong><span class="muted">efficiency, TK risk, deaths-to, and detailed weapon pages</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<form class="search" method="get" action="/weapons">{{ if .ServerFilter }}<input type="hidden" name="server" value="{{ .ServerFilter }}">{{ end }}<input type="search" name="q" value="{{ .Query }}" placeholder="Filter by weapon or player"><input type="search" name="limit" value="{{ .Limit }}" placeholder="Limit"><button type="submit">Search</button></form>
<div class="grid2"><section class="panel"><h3>Kill Pressure</h3>{{ .KillBars }}</section><section class="panel"><h3>Kills Per User</h3>{{ .EffBars }}</section></div>
<table><thead><tr><th>Weapon</th><th>Kills</th><th>Users</th><th>Uses</th><th>Kills/User</th><th>Kills/Use</th><th>Deaths To</th><th>TK</th><th>TK Rate</th></tr></thead><tbody>
{{ range .Weapons }}<tr><td><a class="link" href="{{ weaponURL .WeaponCode }}">{{ displayCode .WeaponCode }}</a> <span class="muted mono">{{ .WeaponCode }}</span></td><td>{{ .Kills }}</td><td>{{ .Users }}</td><td>{{ .Uses }}</td><td>{{ printf "%.2f" (rate .Kills .Users) }}</td><td>{{ printf "%.2f" (rate .Kills .Uses) }}</td><td>{{ .Deaths }}</td><td>{{ .Teamkills }}</td><td>{{ printf "%.1f" (tkRate .Teamkills .Kills) }}%</td></tr>{{ else }}<tr><td colspan="9" class="muted">No weapon rows.</td></tr>{{ end }}
</tbody></table>
</main>{{ siteFooter }}</body></html>`))

var vehiclesTemplate = template.Must(template.New("vehicles").Funcs(template.FuncMap{
	"css": cssFunc, "favicon": faviconFunc,
	"displayCode": displayCode,
	"rate":        ratio,
	"siteHeader":  siteHeader,
	"siteFooter":  siteFooter,
	"vehicleURL":  vehicleURL,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Vehicles - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body>{{ siteHeader "vehicles" }}<main>
{{ if .DBError }}<div class="error">Database error: {{ .DBError }}</div>{{ end }}
<div class="page-title"><strong>Vehicles</strong><span class="muted">entries, effectiveness, destruction risk, operator drilldowns</span></div>
{{ if .ServerFilters }}<div class="filters">{{ range .ServerFilters }}<a href="{{ .URL }}"{{ if .Active }} class="active"{{ end }}>{{ .Label }}</a>{{ end }}</div>{{ end }}
<form class="search" method="get" action="/vehicles">{{ if .ServerFilter }}<input type="hidden" name="server" value="{{ .ServerFilter }}">{{ end }}<input type="search" name="q" value="{{ .Query }}" placeholder="Filter by vehicle or operator"><input type="search" name="limit" value="{{ .Limit }}" placeholder="Limit"><button type="submit">Search</button></form>
<div class="grid2"><section class="panel"><h3>Kill Pressure</h3>{{ .Activity }}</section><section class="panel"><h3>Kills Per Entry</h3>{{ .EffBars }}</section></div>
<table><thead><tr><th>Vehicle</th><th>Entries</th><th>Kills</th><th>Kills/Entry</th><th>Users</th><th>Rounds</th><th>Destroyed</th><th>Destroyed/Entry</th><th>TK</th></tr></thead><tbody>
{{ range .Vehicles }}<tr><td><a class="link" href="{{ vehicleURL .VehicleCode }}">{{ displayCode .VehicleCode }}</a> <span class="muted mono">{{ .VehicleCode }}</span></td><td>{{ .Entries }}</td><td>{{ .Kills }}</td><td>{{ printf "%.2f" (rate .Kills .Entries) }}</td><td>{{ .Users }}</td><td>{{ .Rounds }}</td><td>{{ .Destroyed }}</td><td>{{ printf "%.2f" (rate .Destroyed .Entries) }}</td><td>{{ .Teamkills }}</td></tr>{{ else }}<tr><td colspan="9" class="muted">No vehicle rows.</td></tr>{{ end }}
</tbody></table>
</main>{{ siteFooter }}</body></html>`))
