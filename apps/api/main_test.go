package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		cfHeader   string
		xffHeader  string
		want       string
	}{
		{name: "plain remote addr, no proxy", remoteAddr: "203.0.113.9:54321", want: "203.0.113.9"},
		{
			name:       "trusts CF-Connecting-IP whole",
			remoteAddr: "127.0.0.1:54321",
			cfHeader:   "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "single-hop X-Forwarded-For",
			remoteAddr: "127.0.0.1:54321",
			xffHeader:  "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "takes the LAST X-Forwarded-For entry, not the client-supplied first one",
			remoteAddr: "127.0.0.1:54321",
			xffHeader:  "198.51.100.1, 203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "CF-Connecting-IP wins over X-Forwarded-For when both present",
			remoteAddr: "127.0.0.1:54321",
			cfHeader:   "203.0.113.9",
			xffHeader:  "198.51.100.1",
			want:       "203.0.113.9",
		},
		{
			name:       "trusts a private-network peer too (e.g. an nginx sidecar container)",
			remoteAddr: "172.18.0.5:54321",
			cfHeader:   "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			// The critical case: a direct internet client (no reverse proxy
			// in front — the shipped docker-compose.prod.yml's default
			// shape) cannot spoof its IP just by setting these headers
			// itself. Both should be silently ignored.
			name:       "ignores forwarded headers from a non-local/non-private peer",
			remoteAddr: "198.51.100.50:54321",
			cfHeader:   "203.0.113.9",
			xffHeader:  "203.0.113.9",
			want:       "198.51.100.50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.cfHeader != "" {
				req.Header.Set("CF-Connecting-IP", tt.cfHeader)
			}
			if tt.xffHeader != "" {
				req.Header.Set("X-Forwarded-For", tt.xffHeader)
			}
			if got := remoteIP(req); got != tt.want {
				t.Errorf("remoteIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
