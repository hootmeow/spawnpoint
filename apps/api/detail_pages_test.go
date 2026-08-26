package main

import (
	"io"
	"testing"
	"time"
)

func TestWeaponTemplateExecute(t *testing.T) {
	data := weaponPageData{
		GeneratedAt: time.Now(), Code: "M1_GARAND", Name: "M1 Garand",
		Kills: 100, Teamkills: 2, Users: 10, Rounds: 20,
		FirstUsed: "2026-01-01", LastUsed: "2026-02-01",
		KillsPerUser: 10, KillsPerRound: 5, TKRate: 2,
		Killers: []codePlayerRow{{ID: "id-1", DisplayName: "alice", Count: 40, Extra: 5}},
		Victims: []codePlayerRow{{ID: "id-2", DisplayName: "bob", Count: 30, Extra: 5}},
		Maps:    []codeMapRow{{Name: "berlin", Count: 40}},
		Trend:   []codeMapRow{{Name: "2026-01", Count: 30}, {Name: "2026-02", Count: 70}},
	}
	if err := weaponTemplate.Execute(io.Discard, data); err != nil {
		t.Errorf("weaponTemplate: %v", err)
	}

	empty := weaponPageData{GeneratedAt: time.Now(), DBError: "boom"}
	if err := weaponTemplate.Execute(io.Discard, empty); err != nil {
		t.Errorf("weaponTemplate (empty/error state): %v", err)
	}
}

func TestVehicleTemplateExecute(t *testing.T) {
	data := vehiclePageData{
		GeneratedAt: time.Now(), Code: "willys_jeep", Name: "Willys Jeep",
		Entries: 50, Kills: 30, Teamkills: 1, Users: 8, Rounds: 20, Destroyed: 12,
		KillsPerEntry: 0.6, DestroyedPerEntry: 0.24, KillsPerUser: 3.75,
		Operators:  []codePlayerRow{{ID: "id-1", DisplayName: "alice", Count: 10, Extra: 5}},
		Destroyers: []codePlayerRow{{ID: "id-2", DisplayName: "bob", Count: 4, Extra: 5}},
		Maps:       []codeMapRow{{Name: "berlin", Count: 20}},
		EventMix:   []codeMapRow{{Name: "enter_vehicle", Count: 50}},
		KillMaps:   []codeMapRow{{Name: "berlin", Count: 20}},
		Trend:      []codeMapRow{{Name: "2026-01", Count: 20}, {Name: "2026-02", Count: 30}},
	}
	if err := vehicleTemplate.Execute(io.Discard, data); err != nil {
		t.Errorf("vehicleTemplate: %v", err)
	}

	empty := vehiclePageData{GeneratedAt: time.Now(), DBError: "boom"}
	if err := vehicleTemplate.Execute(io.Discard, empty); err != nil {
		t.Errorf("vehicleTemplate (empty/error state): %v", err)
	}
}
