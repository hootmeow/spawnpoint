package bflog

import (
	"strings"
	"testing"

	"github.com/hootmeow/spawnpoint/domain"
)

func TestParseBasicBattlefieldLog(t *testing.T) {
	xmlLog := `<?xml version="1.0" encoding="iso-8859-1"?>
<bf:log version="1.1" xmlns:bf="http://www.dice.se/xmlns/bf/1.1">
  <bf:round timestamp="20.0">
    <bf:server>
      <bf:setting name="server name">Test Server</bf:setting>
      <bf:setting name="modid">bf1942</bf:setting>
      <bf:setting name="mapid">Battleaxe</bf:setting>
      <bf:setting name="map">battleaxe</bf:setting>
      <bf:setting name="game mode">GPM_CQ</bf:setting>
    </bf:server>
    <bf:event name="createPlayer" timestamp="21.0">
      <bf:param type="int" name="player_id">1</bf:param>
      <bf:param type="string" name="name">Player One</bf:param>
      <bf:param type="int" name="is_ai">0</bf:param>
    </bf:event>
    <bf:event name="scoreEvent" timestamp="30.0">
      <bf:param type="int" name="player_id">1</bf:param>
      <bf:param type="string" name="score_type">Kill</bf:param>
      <bf:param type="int" name="victim_id">2</bf:param>
      <bf:param type="string" name="weapon">No4</bf:param>
    </bf:event>
  </bf:round>
</bf:log>`

	result, err := Parse(strings.NewReader(xmlLog), "fixture.xml")
	if err != nil {
		t.Fatal(err)
	}

	if result.Rounds != 1 {
		t.Fatalf("rounds = %d, want 1", result.Rounds)
	}
	if result.Game.ServerName != "Test Server" {
		t.Fatalf("server name = %q", result.Game.ServerName)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(result.Events))
	}
	if result.Events[1].Type != domain.EventScore {
		t.Fatalf("second event type = %q, want %q", result.Events[1].Type, domain.EventScore)
	}
	if result.Events[1].Weapon != "No4" {
		t.Fatalf("weapon = %q, want No4", result.Events[1].Weapon)
	}
}

func TestParseAttributesVehicleKills(t *testing.T) {
	xmlLog := `<?xml version="1.0" encoding="iso-8859-1"?>
<bf:log version="1.1" xmlns:bf="http://www.dice.se/xmlns/bf/1.1">
  <bf:round timestamp="20.0">
    <bf:event name="enterVehicle" timestamp="25.0">
      <bf:param type="int" name="player_id">1</bf:param>
      <bf:param type="string" name="vehicle">T34-85MG42_PCO1</bf:param>
    </bf:event>
    <bf:event name="scoreEvent" timestamp="30.0">
      <bf:param type="int" name="player_id">1</bf:param>
      <bf:param type="string" name="score_type">Kill</bf:param>
      <bf:param type="int" name="victim_id">2</bf:param>
      <bf:param type="string" name="weapon">(none)</bf:param>
    </bf:event>
    <bf:event name="scoreEvent" timestamp="30.5">
      <bf:param type="int" name="player_id">2</bf:param>
      <bf:param type="string" name="score_type">DeathNoMsg</bf:param>
      <bf:param type="string" name="weapon">(none)</bf:param>
    </bf:event>
    <bf:event name="exitVehicle" timestamp="40.0">
      <bf:param type="int" name="player_id">1</bf:param>
      <bf:param type="string" name="vehicle">GermanSoldier</bf:param>
    </bf:event>
    <bf:event name="scoreEvent" timestamp="50.0">
      <bf:param type="int" name="player_id">1</bf:param>
      <bf:param type="string" name="score_type">Kill</bf:param>
      <bf:param type="int" name="victim_id">3</bf:param>
      <bf:param type="string" name="weapon">(none)</bf:param>
    </bf:event>
  </bf:round>
</bf:log>`

	result, err := Parse(strings.NewReader(xmlLog), "fixture.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 5 {
		t.Fatalf("events = %d, want 5", len(result.Events))
	}

	killInVehicle := result.Events[1]
	if killInVehicle.Weapon != "T34-85MG42" {
		t.Fatalf("vehicle kill weapon = %q, want T34-85MG42", killInVehicle.Weapon)
	}
	if killInVehicle.Vehicle != "T34-85MG42_PCO1" {
		t.Fatalf("vehicle kill vehicle = %q, want T34-85MG42_PCO1", killInVehicle.Vehicle)
	}

	death := result.Events[2]
	if death.Weapon != "" {
		t.Fatalf("death weapon = %q, want empty", death.Weapon)
	}

	killOnFoot := result.Events[4]
	if killOnFoot.Weapon != "" {
		t.Fatalf("on-foot weaponless kill weapon = %q, want empty", killOnFoot.Weapon)
	}
}
