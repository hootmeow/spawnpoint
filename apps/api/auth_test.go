package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminIdentityAllows(t *testing.T) {
	tests := []struct {
		role     string
		required string
		want     bool
	}{
		{roleAdmin, roleAdmin, true},
		{roleAdmin, roleModerator, true},
		{roleModerator, roleModerator, true},
		{roleModerator, roleAdmin, false},
		{"", roleModerator, false},
		{"", roleAdmin, false},
	}
	for _, tt := range tests {
		id := adminIdentity{Role: tt.role}
		if got := id.allows(tt.required); got != tt.want {
			t.Errorf("role %q allows(%q) = %v, want %v", tt.role, tt.required, got, tt.want)
		}
	}
}

func TestRequireRole(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "test-token-abcdefghijklmnop")
	s := &apiServer{}
	var sawIdentity adminIdentity
	handler := s.requireRole(roleAdmin, func(w http.ResponseWriter, r *http.Request) {
		sawIdentity = requestAdminIdentity(r)
		w.WriteHeader(http.StatusOK)
	})

	t.Run("bearer token grants admin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/servers", nil)
		req.Header.Set("Authorization", "Bearer test-token-abcdefghijklmnop")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if sawIdentity.Actor != "admin-token" || sawIdentity.Role != roleAdmin {
			t.Errorf("identity = %+v, want admin-token/admin", sawIdentity)
		}
	})

	t.Run("wrong bearer token is unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/servers", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("browser request redirects to login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/servers", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc == "" || loc[:12] != "/admin/login" {
			t.Errorf("Location = %q, want /admin/login...", loc)
		}
	})
}

func TestRequireRoleFailsClosedWithNoCredentials(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "")
	s := &apiServer{} // no database either: no token, no accounts possible
	handler := s.requireRole(roleModerator, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached")
	})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestSafeNextPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/admin/servers", "/admin/servers"},
		{"//evil.example", "/admin"},
		{"https://evil.example", "/admin"},
		{`/\evil`, "/admin"},
		{"", "/admin"},
	}
	for _, tt := range tests {
		if got := safeNextPath(tt.in); got != tt.want {
			t.Errorf("safeNextPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestAdminTemplatesExecute renders the new admin templates with realistic
// data so a template/data-field mismatch fails in CI, not in production
// (Execute errors are only logged at runtime).
func TestAdminTemplatesExecute(t *testing.T) {
	users := adminUsersData{
		GeneratedAt: time.Now(),
		Role:        roleAdmin,
		Actor:       "alice",
		CSRF:        "x",
		Migrated:    true,
		Users: []adminUserRow{
			{ID: "id-1", Username: "alice", Role: "admin", Sessions: 2, CreatedAt: "2026-01-01", LastLogin: "never"},
			{ID: "id-2", Username: "bob", Role: "moderator", Disabled: true, CreatedAt: "2026-01-01", LastLogin: "never"},
		},
	}
	if err := adminUsersTemplate.Execute(io.Discard, users); err != nil {
		t.Errorf("adminUsersTemplate: %v", err)
	}

	audit := struct {
		GeneratedAt time.Time
		DBError     string
		Role        string
		Query       string
		Rows        []auditRow
	}{
		GeneratedAt: time.Now(),
		Role:        roleAdmin,
		Rows:        []auditRow{{At: "2026-01-01 00:00:00", Actor: "alice", Action: "login"}},
	}
	if err := adminAuditTemplate.Execute(io.Discard, audit); err != nil {
		t.Errorf("adminAuditTemplate: %v", err)
	}

	login := struct {
		Next        string
		Failed      bool
		FailMessage string
		Bootstrap   bool
		LoginCSRF   string
	}{Next: "/admin", Failed: true, FailMessage: "Invalid credentials.", Bootstrap: true, LoginCSRF: "x"}
	if err := adminLoginTemplate.Execute(io.Discard, login); err != nil {
		t.Errorf("adminLoginTemplate: %v", err)
	}

	// The pre-existing admin pages now reference .Role for adminNav; execute
	// each with realistic data so a missing field fails here, not live.
	truthy := true
	home := adminHomeData{
		GeneratedAt: time.Now(), Version: "test", Role: roleModerator,
		Totals: []keyValueRow{{Label: "a", Value: "b", Detail: "c"}},
		Ingest: adminIngestHealth{LastJobAt: "never", LastJobAge: "never", Stale: true},
		Jobs:   []keyValueRow{{Label: "parsed", Value: "x.xml"}},
	}
	if err := adminHomeTemplate.Execute(io.Discard, home); err != nil {
		t.Errorf("adminHomeTemplate: %v", err)
	}

	status := statusPageData{
		GeneratedAt: time.Now(), Role: roleAdmin,
		Rows: []keyValueRow{{Label: "db", Value: "ok"}},
		Jobs: []keyValueRow{{Label: "parsed", Value: "x.xml"}},
	}
	if err := statusTemplate.Execute(io.Discard, status); err != nil {
		t.Errorf("statusTemplate: %v", err)
	}

	ingest := adminIngestData{
		GeneratedAt: time.Now(), Role: roleAdmin, StatusFilter: "all",
		StatusFilters: []filterLink{{Label: "All", URL: "/admin/ingest", Active: true}},
		Jobs:          []ingestJobRow{{StartedAt: "2026-01-01", Status: "parsed", Name: "x.xml", SizeKB: 1, Error: "boom"}},
	}
	if err := adminIngestTemplate.Execute(io.Discard, ingest); err != nil {
		t.Errorf("adminIngestTemplate: %v", err)
	}

	players := adminPlayersData{
		GeneratedAt: time.Now(), Role: roleAdmin, CSRF: "x", Migrated: true,
		Players: []moderationPlayerRow{{ID: "id-1", Name: "n", Aliases: "a", IsBot: &truthy, Hidden: true, BotOverride: &truthy}},
	}
	if err := adminPlayersTemplate.Execute(io.Discard, players); err != nil {
		t.Errorf("adminPlayersTemplate: %v", err)
	}

	identity := struct {
		GeneratedAt time.Time
		DBError     string
		Role        string
		Query       string
		CSRF        string
		Message     string
		Players     []adminPlayerRow
	}{
		GeneratedAt: time.Now(), Role: roleAdmin, CSRF: "x", Message: "ok",
		Players: []adminPlayerRow{{ID: "id-1", Name: "n", Aliases: "a", IsBot: &truthy}},
	}
	if err := adminIdentityTemplate.Execute(io.Discard, identity); err != nil {
		t.Errorf("adminIdentityTemplate: %v", err)
	}

	servers := adminServersData{
		GeneratedAt: time.Now(), Role: roleAdmin, CSRF: "x", Migrated: true,
		Message: "ok", NewKey: "bfps_x", NewKeyFor: "srv",
		Servers: []adminServerRow{{ID: "id-1", Name: "srv", Game: "bf1942", Enabled: true, HasKey: true, LastGame: "never"}},
	}
	if err := adminServersTemplate.Execute(io.Discard, servers); err != nil {
		t.Errorf("adminServersTemplate: %v", err)
	}
}
