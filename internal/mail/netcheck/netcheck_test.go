package netcheck

import (
	"net"
	"testing"
)

func TestPublic(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true}, {"2a00:1450:4001::1", true}, {"::ffff:8.8.8.8", true},
		{"127.0.0.1", false}, {"::1", false}, {"10.1.2.3", false}, {"172.30.0.5", false},
		{"192.168.1.1", false}, {"fd12::1", false}, {"169.254.1.1", false}, {"fe80::1", false},
		{"0.0.0.0", false}, {"::", false}, {"100.64.0.1", false}, {"100.127.255.254", false},
		{"224.0.0.1", false}, {"ff02::1", false}, {"255.255.255.255", false}, {"240.0.0.1", false},
		{"0.1.2.3", false}, {"192.0.0.8", false}, {"198.18.0.1", false}, {"192.0.2.1", false},
		{"2001:db8::1", false}, {"::ffff:127.0.0.1", false}, {"::ffff:10.0.0.1", false},
		{"64:ff9b::808:808", true}, {"64:ff9b::7f00:1", false}, {"64:ff9b::a00:1", false},
		{"64:ff9b:1::808:808", true}, {"64:ff9b:1:ab::a00:1", false},
	} {
		if got := Public(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("Public(%s) = %v", tc.ip, got)
		}
	}
	if Public(nil) {
		t.Error("nil is public")
	}
}
