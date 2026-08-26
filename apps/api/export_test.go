package main

import "testing"

func TestCSVSafe(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"Normal Player Name":   "Normal Player Name",
		"=cmd|'/c calc'!A1":    "'=cmd|'/c calc'!A1",
		"+1+1":                 "'+1+1",
		"-1+1":                 "'-1+1",
		"@SUM(A1:A9)":          "'@SUM(A1:A9)",
		"\tsneaky":             "'\tsneaky",
		"\rsneaky":             "'\rsneaky",
		"Player-With-Dash":     "Player-With-Dash",
		"Player=Equals=Inside": "Player=Equals=Inside",
	}
	for input, want := range cases {
		if got := csvSafe(input); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", input, got, want)
		}
	}
}
