package domain

import "time"

type EventType string

const (
	EventCreatePlayer     EventType = "create_player"
	EventDisconnectPlayer EventType = "disconnect_player"
	EventPlayerKeyHash    EventType = "player_key_hash"
	EventChangePlayerName EventType = "change_player_name"
	EventScore            EventType = "score"
	EventBeginHeal        EventType = "begin_heal"
	EventEndHeal          EventType = "end_heal"
	EventBeginRepair      EventType = "begin_repair"
	EventEndRepair        EventType = "end_repair"
	EventRoundInit        EventType = "round_init"
	EventEnterVehicle     EventType = "enter_vehicle"
	EventExitVehicle      EventType = "exit_vehicle"
	EventRestartMap       EventType = "restart_map"
	EventPickupKit        EventType = "pickup_kit"
	EventChat             EventType = "chat"
	EventSpawn            EventType = "spawn"
	EventDestroyVehicle   EventType = "destroy_vehicle"
	EventDestroyPlayer    EventType = "destroy_player"
	EventRadioMessage     EventType = "radio_message"
	EventSetTeam          EventType = "set_team"
	EventIgnored          EventType = "ignored"
	EventUnknown          EventType = "unknown"
)

type NormalizedEvent struct {
	SequenceNo   int64             `json:"sequence_no"`
	RawName      string            `json:"raw_name"`
	Type         EventType         `json:"type"`
	RoundIndex   int               `json:"round_index"`
	TimestampSec float64           `json:"timestamp_sec"`
	OccurredAt   *time.Time        `json:"occurred_at,omitempty"`
	ActorSlotID  *int              `json:"actor_slot_id,omitempty"`
	TargetSlotID *int              `json:"target_slot_id,omitempty"`
	Weapon       string            `json:"weapon,omitempty"`
	Vehicle      string            `json:"vehicle,omitempty"`
	Kit          string            `json:"kit,omitempty"`
	Text         string            `json:"text,omitempty"`
	Params       map[string]string `json:"params"`
}

type GameInfo struct {
	ServerName string            `json:"server_name"`
	ModID      string            `json:"mod_id"`
	MapID      string            `json:"map_id"`
	Map        string            `json:"map"`
	GameMode   string            `json:"game_mode"`
	Settings   map[string]string `json:"settings"`
}

type RoundStats struct {
	RoundIndex    int
	TimestampSec  float64
	WinningTeamID *int
	VictoryType   *int
	EndTickets    map[int]int
	Players       []PlayerRoundStats
}

type PlayerRoundStats struct {
	SlotID             *int
	Name               string
	IsBot              *bool
	TeamID             *int
	Score              int
	Kills              int
	Deaths             int
	Teamkills          int
	Captures           int
	Attacks            int
	Defences           int
	Objectives         int
	ObjectiveTeamkills int
	Heals              int
	SelfHeals          int
	Repairs            int
	OtherRepairs       int
	RankInRound        *int
	Raw                map[string]string
}

type ParseResult struct {
	Games      int               `json:"games"`
	Rounds     int               `json:"rounds"`
	Game       GameInfo          `json:"game"`
	Events     []NormalizedEvent `json:"events"`
	RoundStats []RoundStats      `json:"round_stats"`
}
