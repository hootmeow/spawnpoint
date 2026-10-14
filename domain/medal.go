package domain

// MedalLabel maps a round_awards category (a round_player_stats column name)
// to the display name shown on medal chips and in round-complete
// notifications. Shared between apps/api (profile/round pages) and
// internal/notify (Discord messages).
func MedalLabel(category string) string {
	switch category {
	case "kills":
		return "Round Butcher"
	case "score":
		return "Round MVP"
	case "captures":
		return "Flag Runner"
	case "heals":
		return "Field Medic"
	case "repairs":
		return "Engineer"
	case "deaths":
		return "Cannon Fodder"
	}
	return category
}
