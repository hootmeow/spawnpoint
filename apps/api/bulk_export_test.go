package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBulkExportFilename(t *testing.T) {
	cases := map[string]string{
		"":         "bf1942-rounds.csv",
		"[TAG]":    "bf1942-tag.csv",
		"Clan 42!": "bf1942-clan-42.csv",
	}
	for input, want := range cases {
		if got := bulkExportFilename(input, "csv"); got != want {
			t.Errorf("bulkExportFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWriteBulkExportCSV(t *testing.T) {
	rows := []roundExportRow{
		{RoundID: "r1", MapName: "berlin", ServerName: "Server One", EndedAt: "2026-01-01 00:00:00 UTC", PlayerID: "p1", PlayerName: "alice", Kills: 10, Deaths: 2, Score: 100},
	}
	rec := httptest.NewRecorder()
	writeBulkExportCSV(rec, rows)
	body := rec.Body.String()
	if !strings.Contains(body, "round_id,map_name,server_name") {
		t.Errorf("missing CSV header, got %q", body)
	}
	if !strings.Contains(body, "r1,berlin,Server One") {
		t.Errorf("missing row data, got %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}
