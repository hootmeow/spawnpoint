package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Admin authentication.
//
// Two credential kinds exist:
//
//   - Named accounts (admin_users, migration 0014): username + bcrypt
//     password via the login form. Each login opens a server-side session
//     (admin_sessions) whose random token travels in the cookie; only its
//     SHA-256 is stored, so a leaked database exposes no usable sessions.
//     Sessions are individually revocable and disabling a user cuts off
//     their sessions immediately.
//
//   - ADMIN_TOKEN: the break-glass credential. It bootstraps the first
//     account (sign in with it, then create users under /admin/users), keeps
//     curl/scripts working via `Authorization: Bearer`, and still opens a
//     cookie session from the login form. It must never appear in a URL —
//     query strings end up in request logs, browser history, and Referer
//     headers.
//
// Roles: "admin" can do everything; "moderator" gets the dashboards plus
// player moderation and identity tools, but not the server registry, ingest
// keys, user management, or the audit log.

const (
	adminSessionCookie = "bfps_admin"
	adminSessionTTL    = 7 * 24 * time.Hour
	// loginCSRFCookie/loginCSRFTTL back the double-submit-cookie CSRF check
	// on the login form itself, which (unlike every other admin POST) has no
	// authenticated session yet to key checkAdminCSRF's HMAC off.
	loginCSRFCookie = "bfps_login_csrf"
	loginCSRFTTL    = 15 * time.Minute
	// maxFormBodyBytes caps POST form bodies before ParseForm, which has no
	// size limit of its own for url-encoded bodies — without this, a large
	// request body would be read entirely into memory.
	maxFormBodyBytes = 64 << 10

	roleAdmin     = "admin"
	roleModerator = "moderator"
)

func adminToken() string {
	return strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))
}

// adminIdentity describes who is making an authenticated admin request.
type adminIdentity struct {
	// SessionID is the admin_sessions row id ("" for bearer requests).
	SessionID string
	// Actor is the username, or "admin-token" for ADMIN_TOKEN credentials.
	Actor string
	Role  string
}

func (id adminIdentity) allows(required string) bool {
	if id.Role == roleAdmin {
		return true
	}
	return id.Role == roleModerator && required == roleModerator
}

type adminIdentityContextKey struct{}

func withAdminIdentity(r *http.Request, id adminIdentity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), adminIdentityContextKey{}, id))
}

// requestAdminIdentity returns the authenticated identity attached by
// requireRole, or a zero identity for unauthenticated requests.
func requestAdminIdentity(r *http.Request) adminIdentity {
	id, _ := r.Context().Value(adminIdentityContextKey{}).(adminIdentity)
	return id
}

// sessionSigningSecret is an independent, random 32-byte key persisted to
// disk and loaded once. It keys the per-session CSRF tokens, so they are
// unforgeable without ever being stored.
var (
	sessionSecretOnce sync.Once
	sessionSecret     []byte
)

// sessionSecretPath is where the signing secret is persisted so CSRF tokens
// stay stable across restarts. Override with SESSION_KEY_FILE.
func sessionSecretPath() string {
	if p := strings.TrimSpace(os.Getenv("SESSION_KEY_FILE")); p != "" {
		return p
	}
	return filepath.Join("data", "session.key")
}

// loadSessionSecret reads the persisted signing secret, generating and storing
// a fresh one on first run. If it cannot be persisted (read-only data dir), it
// falls back to an ephemeral in-memory secret — still secure, but open admin
// sessions will fail CSRF checks after a restart and need a re-login.
func loadSessionSecret() []byte {
	path := sessionSecretPath()
	if data, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(string(bytes.TrimSpace(data))); err == nil && len(raw) >= 32 {
			return raw
		}
		log.Printf("session key at %s is unreadable; regenerating", path)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// Out of entropy is unrecoverable for issuing credentials.
		panic(err)
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		log.Printf("warning: could not persist session key to %s (%v); admin sessions will need a fresh login after a restart", path, err)
	}
	return secret
}

func sessionSigningSecret() []byte {
	sessionSecretOnce.Do(func() { sessionSecret = loadSessionSecret() })
	return sessionSecret
}

// --- server-side sessions --------------------------------------------------

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// createAdminSession opens a session row and returns the raw cookie token.
// userID is nil for break-glass ADMIN_TOKEN logins.
func (s *apiServer) createAdminSession(ctx context.Context, userID *string, role string, r *http.Request) (string, error) {
	if s.db == nil {
		return "", fmt.Errorf("DATABASE_URL is not configured (admin sessions are stored in the database)")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	token := hex.EncodeToString(buf)
	userAgent := r.Header.Get("User-Agent")
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	_, err := s.db.ExecContext(ctx, `
		insert into admin_sessions (token_sha256, user_id, role, expires_at, remote_ip, user_agent)
		values ($1, nullif($2,'')::uuid, $3, now() + $4 * interval '1 second', $5, $6)
	`, hashSessionToken(token), stringOrEmpty(userID), role, int64(adminSessionTTL/time.Second), remoteIP(r), userAgent)
	if err != nil {
		return "", err
	}
	// Opportunistically drop expired sessions; login is rare enough that
	// doing it here keeps the table tidy without a background job.
	_, _ = s.db.ExecContext(ctx, `delete from admin_sessions where expires_at < now()`)
	return token, nil
}

func stringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// lookupSession resolves the session cookie to an identity. Sessions for
// disabled users fail closed; last_seen_at is refreshed at most once a
// minute to avoid a write per admin page view.
func (s *apiServer) lookupSession(r *http.Request) (adminIdentity, bool) {
	if s.db == nil {
		return adminIdentity{}, false
	}
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return adminIdentity{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var id adminIdentity
	var actor sql.NullString
	var stale bool
	err = s.db.QueryRowContext(ctx, `
		select ses.id::text, u.username, ses.role, ses.last_seen_at < now() - interval '1 minute'
		from admin_sessions ses
		left join admin_users u on u.id = ses.user_id
		where ses.token_sha256 = $1
		  and ses.expires_at > now()
		  and (ses.user_id is null or not u.disabled)
	`, hashSessionToken(cookie.Value)).Scan(&id.SessionID, &actor, &id.Role, &stale)
	if err != nil {
		return adminIdentity{}, false
	}
	id.Actor = actor.String
	if id.Actor == "" {
		id.Actor = "admin-token"
	}
	if stale {
		_, _ = s.db.ExecContext(ctx, `update admin_sessions set last_seen_at = now() where id = $1::uuid`, id.SessionID)
	}
	return id, true
}

func (s *apiServer) deleteSession(ctx context.Context, sessionID string) {
	if s.db == nil || sessionID == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `delete from admin_sessions where id = $1::uuid`, sessionID)
}

func bearerAuthed(r *http.Request) bool {
	token := adminToken()
	if token == "" {
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return false
	}
	provided := strings.TrimSpace(auth[len("bearer "):])
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// adminAreaEnabled reports whether any admin credential exists at all: the
// break-glass token, or at least one enabled account. Cached briefly so the
// login page and requireRole don't query per request.
var adminEnabledCache = struct {
	sync.Mutex
	value   bool
	checked time.Time
}{}

func (s *apiServer) adminAreaEnabled(ctx context.Context) bool {
	if adminToken() != "" {
		return true
	}
	if s.db == nil {
		return false
	}
	adminEnabledCache.Lock()
	defer adminEnabledCache.Unlock()
	if time.Since(adminEnabledCache.checked) < 30*time.Second {
		return adminEnabledCache.value
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		select exists (select 1 from admin_users where not disabled)
	`).Scan(&exists)
	if err != nil {
		// Pre-0014 schema or database trouble: no accounts can exist.
		exists = false
	}
	adminEnabledCache.value = exists
	adminEnabledCache.checked = time.Now()
	return exists
}

// --- CSRF ------------------------------------------------------------------

// csrfForSession derives the CSRF token for a session from the persisted
// signing secret; it is embedded in admin forms and checked on every
// cookie-authenticated POST. Keyed by session id, so it is useless on any
// other session and unforgeable without the secret.
func csrfForSession(sessionID string) string {
	mac := hmac.New(sha256.New, sessionSigningSecret())
	fmt.Fprintf(mac, "csrf.%s", sessionID)
	return hex.EncodeToString(mac.Sum(nil))
}

// requestCSRF returns the CSRF token to embed in forms for this request, or
// "" when the request is not cookie-authenticated (bearer callers don't need
// CSRF because no browser credential is attached automatically).
func requestCSRF(r *http.Request) string {
	if id := requestAdminIdentity(r); id.SessionID != "" {
		return csrfForSession(id.SessionID)
	}
	return ""
}

// checkAdminCSRF validates state-changing admin requests. Bearer-authenticated
// calls pass; cookie sessions must present the matching csrf form value.
func checkAdminCSRF(r *http.Request) bool {
	if bearerAuthed(r) {
		return true
	}
	id := requestAdminIdentity(r)
	if id.SessionID == "" {
		return false
	}
	provided := strings.TrimSpace(r.PostFormValue("csrf"))
	return provided != "" && subtle.ConstantTimeCompare([]byte(csrfForSession(id.SessionID)), []byte(provided)) == 1
}

// --- route gating ----------------------------------------------------------

// requireRole gates a handler behind admin auth at a minimum role
// ("moderator" admits both roles, "admin" only admins). With no credentials
// configured anywhere it fails closed. Browsers are redirected to the login
// page; API callers get a 401; an authenticated but under-privileged user
// gets a 403.
func (s *apiServer) requireRole(required string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminAreaEnabled(r.Context()) {
			http.Error(w, "admin area disabled: set ADMIN_TOKEN in the environment (or create an admin account)", http.StatusServiceUnavailable)
			return
		}
		var id adminIdentity
		if bearerAuthed(r) {
			id = adminIdentity{Actor: "admin-token", Role: roleAdmin}
		} else if session, ok := s.lookupSession(r); ok {
			id = session
		} else {
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/admin/login?next="+queryEscape(safeNextPath(r.URL.RequestURI())), http.StatusSeeOther)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="spawnpoint"`)
			http.Error(w, "admin authentication required", http.StatusUnauthorized)
			return
		}
		if !id.allows(required) {
			http.Error(w, "this page requires the admin role", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, withAdminIdentity(r, id))
	}
}

// safeNextPath keeps post-login redirects on this site.
func safeNextPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/admin"
	}
	// Browsers strip ASCII control characters (tabs, CR, LF, ...) from a URL
	// before navigating, so "/\t/evil.com" — which passes every check above —
	// is parsed by the browser as "//evil.com", a protocol-relative redirect
	// to an attacker-controlled host. Reject any control character outright.
	for _, r := range next {
		if r <= 0x1f || r == 0x7f {
			return "/admin"
		}
	}
	return next
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// X-Forwarded-Proto is client-controllable and only meaningful when a
	// reverse proxy actually terminates TLS in front of this process — which
	// is exactly what SECURE_COOKIES signals (see secureCookies). Without
	// that, honoring the header would let anyone spoof "https" on a
	// plaintext request.
	if !envBool("SECURE_COOKIES") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// secureCookies reports whether the admin cookie should carry the Secure flag.
// It is true when the request is HTTPS, or when SECURE_COOKIES is set — the
// latter is for deployments that terminate TLS at a reverse proxy and forward
// plain HTTP to this process, where r.TLS is nil but the browser-facing
// connection is still encrypted. Enabling it ensures admin credentials and
// session cookies are never sent over cleartext.
func secureCookies(r *http.Request) bool {
	return requestIsHTTPS(r) || envBool("SECURE_COOKIES")
}

// loginThrottle applies a small, memory-bounded per-IP delay to brute-force
// attempts against the login form.
var loginThrottle = struct {
	sync.Mutex
	failures map[string]int
	window   time.Time
}{failures: map[string]int{}}

func loginFailureDelay(ip string) time.Duration {
	loginThrottle.Lock()
	defer loginThrottle.Unlock()
	now := time.Now()
	if now.After(loginThrottle.window) {
		loginThrottle.failures = map[string]int{}
		loginThrottle.window = now.Add(15 * time.Minute)
	}
	loginThrottle.failures[ip]++
	n := loginThrottle.failures[ip]
	if n > 10 {
		n = 10
	}
	return time.Duration(n) * 500 * time.Millisecond
}

// usernameLoginThrottle mirrors loginThrottle but keys on the attempted
// username instead of the source IP, so a distributed brute force spread
// across many IPs (or a shared/NAT IP producing false positives on the
// per-IP throttle) still slows down against a single targeted account. It
// deliberately only ever adds delay, never a hard lockout: a hard per-account
// lockout would let anyone lock out a real admin just by failing their
// username a few times from anywhere.
var usernameLoginThrottle = struct {
	sync.Mutex
	failures map[string]int
	window   time.Time
}{failures: map[string]int{}}

func loginFailureDelayForUsername(username string) time.Duration {
	if username == "" {
		return 0
	}
	usernameLoginThrottle.Lock()
	defer usernameLoginThrottle.Unlock()
	now := time.Now()
	if now.After(usernameLoginThrottle.window) {
		usernameLoginThrottle.failures = map[string]int{}
		usernameLoginThrottle.window = now.Add(15 * time.Minute)
	}
	key := strings.ToLower(username)
	usernameLoginThrottle.failures[key]++
	n := usernameLoginThrottle.failures[key]
	if n > 10 {
		n = 10
	}
	return time.Duration(n) * 500 * time.Millisecond
}

// --- login / logout --------------------------------------------------------

// authenticateLogin checks the submitted form against admin accounts first,
// then the break-glass token. It returns the user id (nil for token logins),
// the actor name, and the role.
func (s *apiServer) authenticateLogin(ctx context.Context, username, password, token string) (*string, string, string, bool) {
	if username != "" && password != "" && s.db != nil {
		var id, hash, role string
		err := s.db.QueryRowContext(ctx, `
			select id::text, password_hash, role from admin_users
			where username = $1 and not disabled
		`, username).Scan(&id, &hash, &role)
		if err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil {
			_, _ = s.db.ExecContext(ctx, `update admin_users set last_login_at = now() where id = $1::uuid`, id)
			return &id, username, role, true
		}
		return nil, "", "", false
	}
	if token != "" && adminToken() != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminToken())) == 1 {
		return nil, "admin-token", roleAdmin, true
	}
	return nil, "", "", false
}

// generateLoginCSRFToken returns a fresh random token for the login page's
// double-submit-cookie CSRF check (see loginCSRFCookie).
func generateLoginCSRFToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func (s *apiServer) adminLoginPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if !s.adminAreaEnabled(ctx) {
		http.Error(w, "admin area disabled: set ADMIN_TOKEN in the environment", http.StatusServiceUnavailable)
		return
	}
	next := safeNextPath(r.URL.Query().Get("next"))
	if _, ok := s.lookupSession(r); ok {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}

	var failed bool
	var failMessage string
	if r.Method == http.MethodPost {
		// This endpoint is unauthenticated by definition (it's how auth is
		// obtained), so it's reachable by anyone on the public internet —
		// cap the body before ParseForm reads it all into memory, since
		// ParseForm has no size limit of its own for url-encoded bodies.
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		next = safeNextPath(r.PostFormValue("next"))

		// Double-submit cookie CSRF check. checkAdminCSRF (used by every
		// other admin POST) keys its token off an authenticated session,
		// which doesn't exist yet here — this is the request that creates
		// one. Instead, a random token handed out on render is required to
		// come back both as this cookie and as the hidden form field; a
		// cross-site form can forge the field but, thanks to SameSite=Lax,
		// can't attach the cookie, so the two won't match.
		cookieToken := ""
		if c, err := r.Cookie(loginCSRFCookie); err == nil {
			cookieToken = c.Value
		}
		formToken := r.PostFormValue("login_csrf")
		if cookieToken == "" || formToken == "" || subtle.ConstantTimeCompare([]byte(cookieToken), []byte(formToken)) != 1 {
			failed = true
			failMessage = "Your form expired. Please try again."
		} else {
			username := strings.TrimSpace(r.PostFormValue("username"))
			password := r.PostFormValue("password")
			token := strings.TrimSpace(r.PostFormValue("token"))

			userID, actor, role, ok := s.authenticateLogin(ctx, username, password, token)
			if ok {
				sessionToken, err := s.createAdminSession(ctx, userID, role, r)
				if err != nil {
					log.Printf("admin login: create session failed: %v", err)
					http.Error(w, "could not create a session: "+err.Error(), http.StatusServiceUnavailable)
					return
				}
				http.SetCookie(w, &http.Cookie{
					Name:     adminSessionCookie,
					Value:    sessionToken,
					Path:     "/",
					HttpOnly: true,
					Secure:   secureCookies(r),
					SameSite: http.SameSiteLaxMode,
					MaxAge:   int(adminSessionTTL / time.Second),
				})
				log.Printf("admin login succeeded actor=%s role=%s remote=%s", actor, role, remoteIP(r))
				s.audit(r, actor, "login", "", "role "+role)
				http.Redirect(w, r, next, http.StatusSeeOther)
				return
			}
			failed = true
			failMessage = "Invalid credentials."
			if username != "" && s.db == nil {
				failMessage = "Account logins need DATABASE_URL configured."
			}
			delay := loginFailureDelay(remoteIP(r))
			if userDelay := loginFailureDelayForUsername(username); userDelay > delay {
				delay = userDelay
			}
			log.Printf("admin login failed username=%q remote=%s delay_ms=%d", username, remoteIP(r), delay.Milliseconds())
			s.audit(r, "-", "login_failed", username, "")
			time.Sleep(delay)
		}
	}

	// Surface the bootstrap situation: token configured but no accounts yet.
	bootstrap := adminToken() != "" && s.db != nil && !s.anyAdminUsers(ctx)

	loginCSRF := generateLoginCSRFToken()
	http.SetCookie(w, &http.Cookie{
		Name:     loginCSRFCookie,
		Value:    loginCSRF,
		Path:     "/admin/login",
		HttpOnly: true,
		Secure:   secureCookies(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(loginCSRFTTL / time.Second),
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if failed {
		w.WriteHeader(http.StatusUnauthorized)
	}
	data := struct {
		Next        string
		Failed      bool
		FailMessage string
		Bootstrap   bool
		LoginCSRF   string
	}{Next: next, Failed: failed, FailMessage: failMessage, Bootstrap: bootstrap, LoginCSRF: loginCSRF}
	if err := adminLoginTemplate.Execute(w, data); err != nil {
		log.Printf("render admin login failed: %v", err)
	}
}

func (s *apiServer) anyAdminUsers(ctx context.Context) bool {
	var exists bool
	err := s.db.QueryRowContext(ctx, `select exists (select 1 from admin_users)`).Scan(&exists)
	return err == nil && exists
}

func (s *apiServer) adminLogout(w http.ResponseWriter, r *http.Request) {
	if id, ok := s.lookupSession(r); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		s.deleteSession(ctx, id.SessionID)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookies(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

var adminLoginTemplate = template.Must(template.New("adminLogin").Funcs(detailFuncMap()).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Admin Login - Spawnpoint</title><style>{{ css }}</style>{{ favicon }}</head>
<body class="auth-page">{{ siteHeader "" }}<main>
<div class="auth-shell">
<div class="auth-mark"><svg viewBox="0 0 32 32" fill="none" xmlns="http://www.w3.org/2000/svg"><circle cx="16" cy="16" r="9" stroke="#d7a33b" stroke-width="2.5"/><path d="M16 3v6M16 23v6M3 16h6M23 16h6" stroke="#d7a33b" stroke-width="2.5" stroke-linecap="round"/><circle cx="16" cy="16" r="2.5" fill="#d7a33b"/></svg></div>
<div class="page-title"><strong>Admin Sign-In</strong><span class="muted">Spawnpoint</span></div>
{{ if .Failed }}<div class="error">{{ .FailMessage }}</div>{{ end }}
{{ if .Bootstrap }}<div class="callout">No admin accounts exist yet — open <strong>Sign in with admin token</strong> below to bootstrap one, then create your account under Admin &rarr; Users.</div>{{ end }}
<section class="panel auth-card">
<form method="post" action="/admin/login">
<input type="hidden" name="next" value="{{ .Next }}">
<input type="hidden" name="login_csrf" value="{{ .LoginCSRF }}">
<div class="input-group"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"><circle cx="8" cy="5.2" r="2.8" stroke="#a3aca0" stroke-width="1.3"/><path d="M2.6 14c.7-3 2.9-4.6 5.4-4.6s4.7 1.6 5.4 4.6" stroke="#a3aca0" stroke-width="1.3" stroke-linecap="round"/></svg>
<input type="text" name="username" placeholder="Username" autofocus autocomplete="username"></div>
<div class="input-group"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"><rect x="3" y="7.2" width="10" height="6.6" rx="1.4" stroke="#a3aca0" stroke-width="1.3"/><path d="M5.2 7.2V5a2.8 2.8 0 0 1 5.6 0v2.2" stroke="#a3aca0" stroke-width="1.3" stroke-linecap="round"/></svg>
<input type="password" name="password" placeholder="Password" autocomplete="current-password"></div>
<button type="submit">Sign in</button>
</form>
</section>
<details class="auth-token"{{ if .Bootstrap }} open{{ end }}>
<summary>Sign in with admin token instead</summary>
<section class="panel auth-card">
<div class="award-blurb">Break-glass sign-in with the ADMIN_TOKEN from the server's environment. Not needed once you have an account.</div>
<form method="post" action="/admin/login">
<input type="hidden" name="next" value="{{ .Next }}">
<input type="hidden" name="login_csrf" value="{{ .LoginCSRF }}">
<div class="input-group"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"><rect x="3" y="7.2" width="10" height="6.6" rx="1.4" stroke="#a3aca0" stroke-width="1.3"/><path d="M5.2 7.2V5a2.8 2.8 0 0 1 5.6 0v2.2" stroke="#a3aca0" stroke-width="1.3" stroke-linecap="round"/></svg>
<input type="password" name="token" placeholder="Admin token" autocomplete="off"></div>
<button type="submit">Sign in</button>
</form>
</section>
</details>
</div>
</main>{{ siteFooter }}</body></html>`))
