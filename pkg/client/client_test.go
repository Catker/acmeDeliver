package client

import "testing"

func TestIsPlaintextRemote(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"ws://localhost:9090/ws", false},
		{"ws://LocalHost/ws", false},
		{"ws://127.0.0.1:9090/ws", false},
		{"ws://127.1.2.3:9090/ws", false},
		{"ws://[::1]:9090/ws", false},
		{"ws://example.com:9090/ws", true},
		{"ws://10.0.0.1:9090/ws", true},
		{"ws://192.168.1.10/ws", true},
		{"ws://[2001:db8::1]:9090/ws", true},
		{"wss://example.com:9443/ws", false},
		{"wss://10.0.0.1/ws", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := isPlaintextRemote(tt.url); got != tt.want {
				t.Errorf("isPlaintextRemote(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
