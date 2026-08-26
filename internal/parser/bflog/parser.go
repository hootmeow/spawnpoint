package bflog

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hootmeow/spawnpoint/internal/domain"
)

const battlefieldNamespace = "http://www.dice.se/xmlns/bf/1.1"

// MaxEvents bounds how many <event> elements a single log may contain before
// Parse aborts. Each event holds a struct plus a params map, so an otherwise
// size-limited log made of millions of tiny <event/> elements could still
// amplify into gigabytes of resident memory. The default clears any realistic
// real-world log by a wide margin; callers may raise or lower it (e.g. from a
// MAX_LOG_EVENTS env var). A value <= 0 disables the cap.
var MaxEvents = 5_000_000

type xmlEvent struct {
	Name      string
	Timestamp float64
	Params    map[string]string
}

// Parse reads a Battlefield XML log and returns normalized events.
//
// The first milestone intentionally keeps this parser small and fixture-friendly:
// it streams XML, records raw params, and normalizes only the event names and
// core actor/target fields needed for legacy-compatible projections.
func Parse(reader io.Reader, sourceName string) (domain.ParseResult, error) {
	decoder := xml.NewDecoder(reader)
	decoder.CharsetReader = charsetReader

	var result domain.ParseResult
	result.Games = 1

	var roundIndex int
	var sequence int64
	var currentServerSetting string
	var inServer bool

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			switch localName(t.Name) {
			case "round":
				roundIndex++
				result.Rounds = roundIndex
			case "server":
				inServer = true
				if result.Game.Settings == nil {
					result.Game.Settings = map[string]string{}
				}
			case "setting":
				if inServer {
					currentServerSetting = attr(t, "name")
				}
			case "event":
				event, err := readEvent(decoder, t)
				if err != nil {
					return result, err
				}
				sequence++
				result.Events = append(result.Events, normalizeEvent(event, sequence, roundIndex))
				if MaxEvents > 0 && len(result.Events) > MaxEvents {
					return result, fmt.Errorf("log exceeds maximum of %d events", MaxEvents)
				}
			case "roundstats":
				stats, err := readRoundStats(decoder, t, roundIndex)
				if err != nil {
					return result, err
				}
				result.RoundStats = append(result.RoundStats, stats)
			}
		case xml.EndElement:
			if localName(t.Name) == "server" {
				inServer = false
			}
		case xml.CharData:
			if inServer && currentServerSetting != "" {
				value := strings.TrimSpace(string(t))
				if value != "" {
					result.Game.Settings[currentServerSetting] = value
					applyGameSetting(&result.Game, currentServerSetting, value)
				}
				currentServerSetting = ""
			}
		}
	}

	attributeVehicleKills(result.Events)

	_ = sourceName
	return result, nil
}

// noWeaponValue is what the engine logs on scoreEvent when the kill was not
// made with a handheld weapon (vehicle guns, stationary guns, road kills) and
// on every death event, where the weapon field carries no information.
const noWeaponValue = "(none)"

// attributeVehicleKills resolves kills the engine logged with weapon "(none)".
// The XML log only names handheld weapons; kills from a vehicle or stationary
// gun must be attributed by tracking which vehicle each slot occupies when the
// kill happens.
func attributeVehicleKills(events []domain.NormalizedEvent) {
	vehicleBySlot := map[int]string{}
	currentRound := 0

	for i := range events {
		event := &events[i]
		if event.RoundIndex != currentRound {
			vehicleBySlot = map[int]string{}
			currentRound = event.RoundIndex
		}
		slot := event.ActorSlotID
		if slot == nil {
			continue
		}

		switch event.RawName {
		case "enterVehicle":
			if isSoldierVehicle(event.Vehicle) || event.Vehicle == "" {
				delete(vehicleBySlot, *slot)
			} else {
				vehicleBySlot[*slot] = event.Vehicle
			}
		case "exitVehicle", "spawnEvent", "createPlayer", "disconnectPlayer":
			delete(vehicleBySlot, *slot)
		case "scoreEvent":
			switch strings.ToLower(event.Params["score_type"]) {
			case "kill", "tk", "teamkill":
				if event.Weapon == "" {
					if vehicle := vehicleBySlot[*slot]; vehicle != "" {
						event.Weapon = vehicleWeaponCode(vehicle)
						if event.Vehicle == "" {
							event.Vehicle = vehicle
						}
					}
				}
			case "death", "deathnomsg":
				delete(vehicleBySlot, *slot)
			}
		}
	}
}

// isSoldierVehicle reports whether a vehicle code is a soldier pawn: the
// engine treats infantry as a "vehicle" (e.g. GermanSoldier, RussianSoldier),
// so entering one means the player is back on foot.
func isSoldierVehicle(vehicle string) bool {
	return strings.HasSuffix(vehicle, "Soldier")
}

// vehicleWeaponCode strips the player-controlled-object suffix so all
// positions of one mount group together (e.g. Hanomag_MG42_PCO1 -> Hanomag_MG42).
func vehicleWeaponCode(vehicle string) string {
	if i := strings.LastIndex(vehicle, "_PCO"); i > 0 && isDigits(vehicle[i+len("_PCO"):]) {
		return vehicle[:i]
	}
	return vehicle
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "latin1", "latin-1":
		return &latin1Reader{reader: input}, nil
	case "", "utf-8", "utf8":
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported charset %q", charset)
	}
}

type latin1Reader struct {
	reader  io.Reader
	pending []byte
}

func (r *latin1Reader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}

	buffer := make([]byte, len(p))
	n, err := r.reader.Read(buffer)
	if n == 0 {
		return 0, err
	}
	out := make([]byte, 0, n*2)
	for _, b := range buffer[:n] {
		if b < 0x80 {
			out = append(out, b)
			continue
		}
		out = append(out, 0xc0|byte(b>>6), 0x80|byte(b&0x3f))
	}
	copied := copy(p, out)
	if copied < len(out) {
		r.pending = append(r.pending, out[copied:]...)
	}
	return copied, err
}

func readEvent(decoder *xml.Decoder, start xml.StartElement) (xmlEvent, error) {
	event := xmlEvent{
		Name:   attr(start, "name"),
		Params: map[string]string{},
	}
	event.Timestamp, _ = strconv.ParseFloat(attr(start, "timestamp"), 64)

	for {
		token, err := decoder.Token()
		if err != nil {
			return event, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			if localName(t.Name) == "param" {
				name := attr(t, "name")
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return event, err
				}
				event.Params[name] = strings.TrimSpace(value)
			}
		case xml.EndElement:
			if localName(t.Name) == "event" {
				return event, nil
			}
		}
	}
}

func readRoundStats(decoder *xml.Decoder, start xml.StartElement, roundIndex int) (domain.RoundStats, error) {
	stats := domain.RoundStats{
		RoundIndex: roundIndex,
		EndTickets: map[int]int{},
		Players:    []domain.PlayerRoundStats{},
	}
	stats.TimestampSec, _ = strconv.ParseFloat(attr(start, "timestamp"), 64)
	stats.WinningTeamID = parseIntPtr(firstNonEmptyAttr(start, "winning_team", "winningTeam", "winning_team_id", "winningTeamId", "winningteam"))

	for {
		token, err := decoder.Token()
		if err != nil {
			return stats, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			name := localName(t.Name)
			switch name {
			case "player", "playerstat", "playerstats", "playerStats":
				player, err := readPlayerRoundStats(decoder, t)
				if err != nil {
					return stats, err
				}
				stats.Players = append(stats.Players, player)
			default:
				if key := statKey(name); key != "" {
					var value string
					if err := decoder.DecodeElement(&value, &t); err != nil {
						return stats, err
					}
					if key == "winning_team" || key == "winningteam" {
						stats.WinningTeamID = parseIntPtr(strings.TrimSpace(value))
					}
					if key == "victorytype" || key == "victory_type" {
						stats.VictoryType = parseIntPtr(strings.TrimSpace(value))
					}
					if key == "teamtickets" {
						teamID := parseInt(firstNonEmptyAttr(t, "team", "team_id", "teamid"))
						if teamID != 0 {
							stats.EndTickets[teamID] = parseInt(strings.TrimSpace(value))
						}
					}
				}
			}
		case xml.EndElement:
			if localName(t.Name) == "roundstats" {
				return stats, nil
			}
		}
	}
}

func readPlayerRoundStats(decoder *xml.Decoder, start xml.StartElement) (domain.PlayerRoundStats, error) {
	raw := map[string]string{}
	for _, attribute := range start.Attr {
		raw[statKey(attribute.Name.Local)] = attribute.Value
	}

	for {
		token, err := decoder.Token()
		if err != nil {
			return domain.PlayerRoundStats{Raw: raw}, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			key := localName(t.Name)
			if statName := attr(t, "name"); statName != "" {
				key = statName
			}
			var value string
			if err := decoder.DecodeElement(&value, &t); err != nil {
				return domain.PlayerRoundStats{Raw: raw}, err
			}
			raw[statKey(key)] = strings.TrimSpace(value)
		case xml.EndElement:
			name := localName(t.Name)
			if name == "player" || name == "playerstat" || name == "playerstats" || name == "playerStats" {
				return buildPlayerRoundStats(raw), nil
			}
		}
	}
}

func buildPlayerRoundStats(raw map[string]string) domain.PlayerRoundStats {
	return domain.PlayerRoundStats{
		SlotID:             parseIntPtr(firstRaw(raw, "player_id", "playerid", "id", "slot_id", "slot")),
		Name:               firstRaw(raw, "name", "player_name", "playername", "nick", "nickname"),
		IsBot:              parseBoolPtr(firstRaw(raw, "is_ai", "isai", "bot", "is_bot")),
		TeamID:             parseIntPtr(firstRaw(raw, "team", "team_id", "teamid")),
		Score:              parseInt(firstRaw(raw, "score")),
		Kills:              parseInt(firstRaw(raw, "kills")),
		Deaths:             parseInt(firstRaw(raw, "deaths")),
		Teamkills:          parseInt(firstRaw(raw, "teamkills", "team_kills", "tks")),
		Captures:           parseInt(firstRaw(raw, "captures")),
		Attacks:            parseInt(firstRaw(raw, "attacks")),
		Defences:           parseInt(firstRaw(raw, "defences", "defenses")),
		Objectives:         parseInt(firstRaw(raw, "objectives")),
		ObjectiveTeamkills: parseInt(firstRaw(raw, "objective_teamkills", "objective_team_kills", "objectivetks")),
		Heals:              parseInt(firstRaw(raw, "heals")),
		SelfHeals:          parseInt(firstRaw(raw, "self_heals", "selfheals")),
		Repairs:            parseInt(firstRaw(raw, "repairs")),
		OtherRepairs:       parseInt(firstRaw(raw, "other_repairs", "otherrepairs")),
		RankInRound:        parseIntPtr(firstRaw(raw, "rank", "rank_in_round", "rankinround")),
		Raw:                raw,
	}
}

func normalizeEvent(event xmlEvent, sequence int64, roundIndex int) domain.NormalizedEvent {
	normalized := domain.NormalizedEvent{
		SequenceNo:   sequence,
		RawName:      event.Name,
		Type:         normalizeEventType(event.Name),
		RoundIndex:   roundIndex,
		TimestampSec: event.Timestamp,
		Params:       event.Params,
	}

	if actor := firstParam(event.Params, "player_id"); actor != "" {
		normalized.ActorSlotID = parseIntPtr(actor)
	}
	if target := firstParam(event.Params, "victim_id", "healed_player", "vehicle_player"); target != "" {
		normalized.TargetSlotID = parseIntPtr(target)
	}

	normalized.Weapon = normalizeCode(firstParam(event.Params, "weapon"))
	normalized.Vehicle = normalizeCode(firstParam(event.Params, "vehicle", "vechicle", "vehicle_type", "vechicle_type"))
	normalized.Kit = normalizeCode(firstParam(event.Params, "kit"))
	normalized.Text = firstParam(event.Params, "text")

	return normalized
}

func normalizeEventType(name string) domain.EventType {
	switch name {
	case "createPlayer":
		return domain.EventCreatePlayer
	case "disconnectPlayer":
		return domain.EventDisconnectPlayer
	case "playerKeyHash":
		return domain.EventPlayerKeyHash
	case "changePlayerName":
		return domain.EventChangePlayerName
	case "scoreEvent":
		return domain.EventScore
	case "beginMedPack":
		return domain.EventBeginHeal
	case "endMedPack":
		return domain.EventEndHeal
	case "beginRepair":
		return domain.EventBeginRepair
	case "endRepair":
		return domain.EventEndRepair
	case "roundInit":
		return domain.EventRoundInit
	case "enterVehicle":
		return domain.EventEnterVehicle
	case "exitVehicle":
		return domain.EventExitVehicle
	case "restartMap":
		return domain.EventRestartMap
	case "pickupKit":
		return domain.EventPickupKit
	case "chat":
		return domain.EventChat
	case "spawnEvent":
		return domain.EventSpawn
	case "destroyVehicle":
		return domain.EventDestroyVehicle
	case "destroyPlayer":
		return domain.EventDestroyPlayer
	case "radioMessage":
		return domain.EventRadioMessage
	case "setTeam":
		return domain.EventSetTeam
	default:
		return domain.EventUnknown
	}
}

func applyGameSetting(game *domain.GameInfo, name string, value string) {
	switch name {
	case "server name":
		game.ServerName = value
	case "modid":
		game.ModID = value
	case "mapid":
		game.MapID = value
	case "map":
		game.Map = value
	case "game mode":
		game.GameMode = value
	}
}

func localName(name xml.Name) string {
	return name.Local
}

func attr(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

// normalizeCode maps the engine's "(none)" placeholder to an empty value so
// it never surfaces as a weapon, vehicle, or kit of its own.
func normalizeCode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), noWeaponValue) {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstParam(params map[string]string, names ...string) string {
	for _, name := range names {
		if value, ok := params[name]; ok {
			return value
		}
	}
	return ""
}

func parseIntPtr(value string) *int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func parseBoolPtr(value string) *bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		parsed := true
		return &parsed
	case "0", "false", "no":
		parsed := false
		return &parsed
	default:
		return nil
	}
}

func firstNonEmptyAttr(element xml.StartElement, names ...string) string {
	for _, name := range names {
		if value := attr(element, name); value != "" {
			return value
		}
	}
	return ""
}

func firstRaw(raw map[string]string, names ...string) string {
	for _, name := range names {
		if value := raw[statKey(name)]; value != "" {
			return value
		}
	}
	return ""
}

func statKey(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
}

func Namespace() string {
	return battlefieldNamespace
}
