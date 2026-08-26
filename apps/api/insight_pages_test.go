package main

import (
	"io"
	"testing"
	"time"
)

func TestRivalryTemplateExecute(t *testing.T) {
	data := rivalryPageData{
		GeneratedAt: time.Now(), A: "id-1", B: "id-2", AName: "alice", BName: "bob",
		AKills: 20, BKills: 15,
		Weapons: []codeMapRow{{Name: "M1_GARAND", Count: 10}},
		Maps:    []codeMapRow{{Name: "berlin", Count: 8}},
		Trend: []seriesPoint{
			{Label: "2026-01", Kills: 5, Deaths: 3},
			{Label: "2026-02", Kills: 15, Deaths: 12},
		},
	}
	if err := rivalryTemplate.Execute(io.Discard, data); err != nil {
		t.Errorf("rivalryTemplate: %v", err)
	}

	empty := rivalryPageData{GeneratedAt: time.Now(), A: "id-1", B: "id-2", DBError: "boom"}
	if err := rivalryTemplate.Execute(io.Discard, empty); err != nil {
		t.Errorf("rivalryTemplate (empty/error state): %v", err)
	}
}
