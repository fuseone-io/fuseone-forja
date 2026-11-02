package egress

import (
	"errors"
	"testing"
)

// The dial guard decides on the address a name resolved to. Everything on
// the node's side of the gateway is refused: the shared bridge, the host,
// the cloud metadata endpoint and carrier-grade NAT space, in either
// family and in IPv4-mapped form. Only a public address passes.
func TestRefuseNonPublic_resolvedAddress_onlyPublicPasses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		address string
		refused bool
	}{
		{address: "127.0.0.1:443", refused: true},
		{address: "10.0.0.1:443", refused: true},
		{address: "172.17.0.2:3128", refused: true},
		{address: "192.168.5.2:443", refused: true},
		{address: "100.64.0.1:443", refused: true},
		{address: "100.127.255.254:443", refused: true},
		{address: "169.254.169.254:80", refused: true},
		{address: "0.0.0.0:443", refused: true},
		{address: "[::1]:443", refused: true},
		{address: "[fd00::1]:443", refused: true},
		{address: "[fe80::1]:443", refused: true},
		{address: "[::ffff:172.17.0.2]:3128", refused: true},
		{address: "1.1.1.1:443", refused: false},
		{address: "100.128.0.1:443", refused: false},
		{address: "[2606:4700:4700::1111]:443", refused: false},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			t.Parallel()
			err := refuseNonPublic("tcp", tt.address, nil)
			if got := errors.Is(err, ErrNonPublicDestination); got != tt.refused {
				t.Fatalf("refuseNonPublic(%s) = %v, want refused %v", tt.address, err, tt.refused)
			}
			if !tt.refused && err != nil {
				t.Fatalf("refuseNonPublic(%s) = %v, want nil for a public address", tt.address, err)
			}
		})
	}
}
