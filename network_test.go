package main

import (
	"net/netip"
	"testing"
)

func TestLANSelectionExcludesPublicLoopbackAndVPN(t *testing.T) {
	for _, tc := range []struct {
		name, ip string
		want     bool
	}{
		{"wlan0", "192.168.1.2", true}, {"enp1s0", "10.0.1.2", true},
		{"wg0", "10.0.1.2", false}, {"tailscale0", "100.100.1.2", false},
		{"wlan0", "8.8.8.8", false}, {"lo", "127.0.0.1", false},
		{"docker0", "172.17.0.1", false}, {"wlan0", "::1", false},
	} {
		if got := eligibleLAN(tc.name, netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("%s %s: %v", tc.name, tc.ip, got)
		}
	}
}
