package domain

import "testing"

func TestExtractClanTag(t *testing.T) {
	cases := map[string]string{
		"[TAG]Player": "TAG",
		"Player[TAG]": "TAG",
		"-TAG-Player": "TAG",
		"Player-TAG-": "TAG",
		"=TAG=Player": "TAG",
		"Player=TAG=": "TAG",
		"{TAG}Player": "TAG",
		"Player{TAG}": "TAG",
		"[tag]player": "TAG",
		"PlainPlayer": "",
		"Player-Name": "",
		"":            "",
	}
	for name, want := range cases {
		if got := ExtractClanTag(name); got != want {
			t.Errorf("ExtractClanTag(%q) = %q, want %q", name, got, want)
		}
	}
}
