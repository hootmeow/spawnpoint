package main

import (
	"io"
	"testing"
	"time"
)

func TestStreaksTemplateExecute(t *testing.T) {
	data := streaksPageData{
		GeneratedAt: time.Now(),
		Rows: []streakRow{
			{PlayerID: "id-1", PlayerName: "alice", Streak: 12, RoundID: "round-1", MapName: "berlin", EndedAt: "2026-01-01 00:00:00 UTC"},
			{PlayerID: "id-2", PlayerName: "bob", Streak: 9, RoundID: "round-2", MapName: "wake", EndedAt: "2026-01-02 00:00:00 UTC"},
		},
		PageLinks: []filterLink{{Label: "1", URL: "/streaks", Active: true}, {Label: "2", URL: "/streaks?page=2"}},
	}
	if err := streaksTemplate.Execute(io.Discard, data); err != nil {
		t.Errorf("streaksTemplate: %v", err)
	}

	empty := streaksPageData{GeneratedAt: time.Now(), DBError: "boom"}
	if err := streaksTemplate.Execute(io.Discard, empty); err != nil {
		t.Errorf("streaksTemplate (empty/error state): %v", err)
	}
}
