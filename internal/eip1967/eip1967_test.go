package eip1967

import (
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// TestSlots checks the slots against the values in EIP-1967.
func TestSlots(t *testing.T) {
	for _, tt := range []struct {
		name string
		got  ethrpc.Hash
		want string
	}{
		{"implementation", ImplementationSlot, "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"},
		{"admin", AdminSlot, "0xb53127684a568b3173ae13b9f8a6016e243e63b6e8ee1178d6a717850b5d6103"},
		{"beacon", BeaconSlot, "0xa3f0ad74e5423aebfd80d3ef4346578335a9a72aeaee59ff6cb3582b35133d50"},
	} {
		if got := tt.got.String(); got != tt.want {
			t.Errorf("%s slot = %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestDecode(t *testing.T) {
	var value ethrpc.Hash
	value[12], value[31] = 0xe1, 0xa7
	s := decode(ImplementationSlot, value)
	if !s.Clean || s.Address[0] != 0xe1 || s.Address[19] != 0xa7 {
		t.Errorf("decode = %+v", s)
	}
	value[0] = 1
	if decode(ImplementationSlot, value).Clean {
		t.Errorf("decode of a value with high bytes is clean")
	}
}
