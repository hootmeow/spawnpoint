package main

import "testing"

func TestFormatRoundTimeRemaining(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: ""},
		{name: "seconds to minutes and seconds", value: "150", want: "2m 30s"},
		{name: "pads remaining seconds", value: "125", want: "2m 05s"},
		{name: "under one minute", value: "9", want: "0m 09s"},
		{name: "float seconds", value: "149.6", want: "2m 30s"},
		{name: "negative clamps to zero", value: "-5", want: "0m 00s"},
		{name: "unknown value passes through", value: "unknown", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatRoundTimeRemaining(tt.value); got != tt.want {
				t.Fatalf("formatRoundTimeRemaining(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
