package main

import "testing"

func TestParseServerDir(t *testing.T) {
	tests := []struct {
		name           string
		value          string
		defaultAddress string
		defaultPort    int
		want           serverDir
		wantErr        bool
	}{
		{
			name:           "name and path",
			value:          "Coop Bots=/srv/bf1942/coop/logs",
			defaultAddress: "127.0.0.1",
			defaultPort:    14567,
			want:           serverDir{name: "Coop Bots", address: "127.0.0.1", port: 14567, path: "/srv/bf1942/coop/logs"},
		},
		{
			name:           "name host port and path",
			value:          "Infantry@192.0.2.10:14568=/srv/bf1942/inf/logs",
			defaultAddress: "127.0.0.1",
			defaultPort:    14567,
			want:           serverDir{name: "Infantry", address: "192.0.2.10", port: 14568, path: "/srv/bf1942/inf/logs"},
		},
		{
			name:           "host without port",
			value:          "Desert@stats.local=/srv/bf1942/desert/logs",
			defaultAddress: "127.0.0.1",
			defaultPort:    14567,
			want:           serverDir{name: "Desert", address: "stats.local", port: 14567, path: "/srv/bf1942/desert/logs"},
		},
		{name: "missing separator", value: "Coop Bots", defaultAddress: "127.0.0.1", defaultPort: 14567, wantErr: true},
		{name: "missing name", value: "=/srv/bf1942/logs", defaultAddress: "127.0.0.1", defaultPort: 14567, wantErr: true},
		{name: "missing path", value: "Coop Bots=", defaultAddress: "127.0.0.1", defaultPort: 14567, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseServerDir(tt.value, tt.defaultAddress, tt.defaultPort)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseServerDir(%q) succeeded, want error", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseServerDir(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("parseServerDir(%q) = %#v, want %#v", tt.value, got, tt.want)
			}
		})
	}
}
